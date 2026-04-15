package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	firewallext "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/firewall"
	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (a *App) runPoolHost() error {
	if a.brokerPort == 0 || a.brokerToken == "" || a.poolToken == "" {
		return fmt.Errorf("pool mode requires broker port, broker token, and pool token")
	}

	fmt.Fprintf(os.Stderr, "Kitchen pool broker ready\n")
	fmt.Fprintf(os.Stderr, "MITTENS_BROKER_PORT=%d\n", a.brokerPort)
	fmt.Fprintf(os.Stderr, "MITTENS_BROKER_TOKEN=%s\n", a.brokerToken)
	fmt.Fprintf(os.Stderr, "MITTENS_POOL_TOKEN=%s\n", a.poolToken)
	fmt.Fprintf(os.Stderr, "MITTENS_SESSION_ID=%s\n", a.poolSession)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	<-sigCh
	return nil
}

func (a *App) spawnWorkerContainer(spec pool.WorkerSpec) (string, string, error) {
	wid := strings.TrimSpace(spec.ID)
	if wid == "" {
		return "", "", fmt.Errorf("spawn worker: spec.ID is required")
	}

	if strings.TrimSpace(spec.Provider) != "" && canonicalProviderName(spec.Provider) != canonicalProviderName(a.Provider.Name) {
		return "", "", fmt.Errorf("spawn worker: provider %q does not match host provider %q", spec.Provider, a.Provider.Name)
	}

	// In daemon mode the image build is deferred to first spawn. Block
	// here until it completes (ensureImage is once-safe and shared with
	// the background build kicked off in runRuntimeDaemon).
	if !a.NoBuild {
		if err := a.ensureImage(); err != nil {
			return "", "", fmt.Errorf("spawn worker: build image: %w", err)
		}
	}

	sessionID := strings.TrimSpace(spec.Environment["MITTENS_SESSION_ID"])
	if sessionID == "" {
		sessionID = a.poolSession
	}
	if sessionID == "" {
		return "", "", fmt.Errorf("spawn worker: missing session ID")
	}

	workspacePath := strings.TrimSpace(spec.WorkspacePath)
	if workspacePath == "" {
		workspacePath = a.WorkspaceMountSrc
	}
	workspacePath, err := validateWorkerWorkspacePath(workspacePath)
	if err != nil {
		return "", "", fmt.Errorf("spawn worker: %w", err)
	}
	spec.WorkspacePath = workspacePath

	kitchenAddr := strings.TrimSpace(spec.Environment["MITTENS_KITCHEN_ADDR"])
	if kitchenAddr == "" {
		kitchenAddr = strings.TrimSpace(os.Getenv("MITTENS_KITCHEN_ADDR"))
	}
	if kitchenAddr == "" {
		return "", "", fmt.Errorf("spawn worker: MITTENS_KITCHEN_ADDR is required")
	}

	containerName := fmt.Sprintf("mittens-%s-%s", sessionID, wid)
	if err := removeStaleExactNameContainers(containerName); err != nil {
		return "", "", fmt.Errorf("spawn worker: %w", err)
	}

	workerDir := filepath.Join(a.poolStateDir, "workers", wid)
	if _, err := os.Stat(workerDir); err == nil {
		_ = os.RemoveAll(workerDir)
	}
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		return "", "", fmt.Errorf("create worker dir: %w", err)
	}
	if err := os.Chmod(workerDir, 0777); err != nil {
		return "", "", fmt.Errorf("chmod worker dir: %w", err)
	}

	args := []string{
		"run", "-d",
		"--name", containerName,
		"-l", "mittens.pool=" + sessionID,
		"-l", "mittens.role=worker",
		"-l", "mittens.worker_id=" + wid,
		"-l", "mittens.workspace=" + workspacePath,
		"-v", workspacePath + ":" + workspacePath,
		"-v", workerDir + ":/team",
		"--add-host=host.docker.internal:host-gateway",
		"-e", "AI_USERNAME=" + a.Provider.Username,
		"-e", "MITTENS_WORKER_ID=" + wid,
		"-e", "MITTENS_KITCHEN_ADDR=" + kitchenAddr,
		"-e", "MITTENS_TEAM_DIR=/team",
		"-e", "MITTENS_PROVIDER=" + a.Provider.Name,
	}
	if a.Provider.ContainerHostname != "" {
		args = append(args, "--hostname", a.Provider.ContainerHostname)
	}
	if _, ok := spec.Environment["MITTENS_SKIP_PERMS_FLAG"]; !ok && a.Provider.SkipPermsFlag != "" {
		args = append(args, "-e", "MITTENS_SKIP_PERMS_FLAG="+a.Provider.SkipPermsFlag)
	}

	if spec.Adapter != "" {
		args = append(args, "-e", "MITTENS_ADAPTER="+spec.Adapter)
	}
	if spec.Model != "" {
		args = append(args, "-e", "MITTENS_MODEL="+spec.Model)
	}

	if a.Provider.APIKeyEnv != "" {
		args = append(args, "-e", a.Provider.APIKeyEnv+"="+os.Getenv(a.Provider.APIKeyEnv))
	}
	if a.Provider.BaseURLEnv != "" && os.Getenv(a.Provider.BaseURLEnv) != "" {
		args = append(args, "-e", a.Provider.BaseURLEnv+"="+os.Getenv(a.Provider.BaseURLEnv))
	}
	for k, v := range a.Provider.ContainerEnv {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range spec.Environment {
		args = append(args, "-e", k+"="+v)
	}

	if a.Credentials != nil && a.Credentials.TmpFile() != "" && a.Provider.CredentialFile != "" {
		args = append(args, "-v", a.Credentials.TmpFile()+":"+a.Provider.StagingCredentialPath()+":ro")
	}

	home := os.Getenv("HOME")
	hostConfigDir := a.Provider.HostConfigDir(home)
	ensureDir(hostConfigDir)
	args = append(args, "-v", hostConfigDir+":"+a.Provider.StagingConfigDir()+":ro")
	if a.Provider.UserPrefsFile != "" {
		hostPrefs := a.Provider.HostUserPrefsPath(home)
		if _, err := os.Stat(hostPrefs); err == nil {
			args = append(args, "-v", hostPrefs+":"+a.Provider.StagingUserPrefsPath()+":ro")
		}
	}
	gitconfig := filepath.Join(home, ".gitconfig")
	if fileExists(gitconfig) {
		args = append(args, "-v", gitconfig+":"+a.Provider.StagingGitconfigPath()+":ro")
	}

	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}

	if commonGitDir, err := externalWorktreeCommonGitDir(workspacePath); err != nil {
		return "", "", fmt.Errorf("resolve worktree git metadata: %w", err)
	} else if commonGitDir != "" {
		args = append(args, "-v", commonGitDir+":"+commonGitDir)
	}

	args = append(args,
		"--cap-drop", "ALL",
		"--cap-add", "SETUID",
		"--cap-add", "SETGID",
		"--security-opt", "no-new-privileges",
	)

	for _, m := range expandedExtensionMounts(a.Extensions, home, a.Provider) {
		mountStr := m.Src + ":" + m.Dst
		if m.Mode != "" {
			mountStr += ":" + m.Mode
		}
		args = append(args, "-v", mountStr)
		for k, v := range m.Env {
			args = append(args, "-e", k+"="+v)
		}
	}
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}
		for k, v := range ext.Env {
			args = append(args, "-e", k+"="+v)
		}
		for _, cap := range ext.Capabilities {
			args = append(args, "--cap-add", cap)
		}
	}

	var resolvedExtraDirs []string
	if wsInfo, err := os.Stat(workspacePath); err == nil {
		for _, dirSpec := range a.ExtraDirs {
			dir := parseExtraDirSpec(dirSpec)
			resolved, err := filepath.Abs(dir.Path)
			if err != nil {
				continue
			}
			resolvedInfo, err := os.Stat(resolved)
			if err != nil || os.SameFile(resolvedInfo, wsInfo) {
				continue
			}
			mount := resolved + ":" + resolved
			if dir.ReadOnly {
				mount += ":ro"
			}
			args = append(args, "-v", mount)
			resolvedExtraDirs = append(resolvedExtraDirs, resolved)
		}
	}

	firewallConfPath := firewallext.DefaultConfPath
	if firewallConfPath != "" && fileExists(firewallConfPath) {
		args = append(args, "-v", firewallConfPath+":/mnt/mittens-staging/firewall.conf:ro")
	}

	workerCfg := a.buildWorkerInitConfig(a.Provider, containerName, workspacePath)
	if len(resolvedExtraDirs) > 0 {
		workerCfg.ExtraDirs = resolvedExtraDirs
	}
	if a.brokerSock != "" {
		sockDir := filepath.Dir(a.brokerSock)
		containerSockDir := "/tmp/mittens-broker"
		containerSockPath := filepath.Join(containerSockDir, filepath.Base(a.brokerSock))
		args = append(args, "-v", sockDir+":"+containerSockDir)
		workerCfg.Broker.Sock = containerSockPath
		workerCfg.Broker.Port = 0
		workerCfg.Broker.Token = a.brokerToken
	}
	cfgFile, err := os.CreateTemp("", "mittens-worker-*.json")
	if err != nil {
		return "", "", fmt.Errorf("create worker config temp file: %w", err)
	}
	cfgPath := cfgFile.Name()
	_ = cfgFile.Close()
	if err := workerCfg.Write(cfgPath); err != nil {
		_ = os.Remove(cfgPath)
		return "", "", fmt.Errorf("write worker config: %w", err)
	}
	if err := os.Chmod(cfgPath, 0644); err != nil {
		_ = os.Remove(cfgPath)
		return "", "", fmt.Errorf("chmod worker config: %w", err)
	}
	a.tempDirs = append(a.tempDirs, cfgPath)
	args = append(args,
		"-v", cfgPath+":"+initcfg.ConfigPath+":ro",
		"-e", "MITTENS_CONFIG="+initcfg.ConfigPath,
	)

	image := a.ImageName + ":" + a.ImageTag
	args = append(args, image, a.Provider.Binary, "--print")

	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("spawn worker container: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	if err := a.recordRuntimeWorkerSpawn(spec, containerName, containerID); err != nil {
		return "", "", fmt.Errorf("record runtime worker: %w", err)
	}
	return containerName, containerID, nil
}

func (a *App) buildWorkerInitConfig(provider *Provider, containerName, workspacePath string) *initcfg.ContainerConfig {
	firewallEnabled := false
	var firewallDomains []string
	for _, ext := range a.Extensions {
		if ext.Name == "firewall" && ext.Enabled {
			firewallEnabled = true
		}
		if ext.Enabled {
			firewallDomains = append(firewallDomains, ext.FirewallDomains()...)
		}
	}
	firewallDomains = append(firewallDomains, provider.FirewallDomains...)

	cfg := &initcfg.ContainerConfig{
		AI: initcfg.AIConfig{
			Binary:          provider.Binary,
			ConfigDir:       provider.ConfigDir,
			CredFile:        provider.CredentialFile,
			PrefsFile:       provider.UserPrefsFile,
			SettingsFile:    provider.SettingsFile,
			ProjectFile:     provider.ProjectFile,
			TrustedDirsKey:  provider.TrustedDirsKey,
			YoloKey:         provider.YoloKey,
			MCPServersKey:   provider.MCPServersKey,
			TrustedDirsFile: provider.TrustedDirsFile,
			InitSettingsJQ:  provider.InitSettingsJQ,
			StopHookEvent:   provider.StopHookEvent,
			PersistFiles:    provider.PersistFiles,
			SettingsFormat:  provider.SettingsFormat,
			ConfigSubdirs:   provider.ConfigSubdirs,
			PluginDir:       provider.PluginDir,
			PluginFiles:     provider.PluginFiles,
		},
		Flags: initcfg.Flags{
			Verbose:   a.Verbose,
			Yolo:      true,
			PrintMode: true,
			Firewall:  firewallEnabled,
			NoNotify:  true,
		},
		ProviderName:  provider.Name,
		ContainerName: containerName,
		HostWorkspace: workspacePath,
		Broker: initcfg.BrokerConfig{
			Token: a.brokerToken,
		},
	}
	if a.brokerSock != "" {
		cfg.Broker.Sock = filepath.Join("/tmp/mittens-broker", filepath.Base(a.brokerSock))
	} else if a.brokerPort > 0 {
		cfg.Broker.Port = a.brokerPort
	}
	if len(firewallDomains) > 0 {
		cfg.FirewallExtra = firewallDomains
	}
	return cfg
}

func (a *App) killWorkerContainer(workerID string) error {
	containerName := fmt.Sprintf("mittens-%s-%s", a.poolSession, workerID)
	_ = exec.Command("docker", "stop", "-t", "10", containerName).Run()
	if err := exec.Command("docker", "rm", "-f", containerName).Run(); err != nil {
		return err
	}
	return a.markRuntimeWorkerDead(workerID)
}

func validateWorkerWorkspacePath(workspacePath string) (string, error) {
	if strings.TrimSpace(workspacePath) == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("workspace path %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path %q is not a directory", absPath)
	}
	return absPath, nil
}

func externalWorktreeCommonGitDir(workspacePath string) (string, error) {
	commonDir, err := captureCommand("git", "-C", workspacePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", nil
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return "", nil
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Clean(filepath.Join(workspacePath, commonDir))
	}
	rel, err := filepath.Rel(workspacePath, commonDir)
	if err != nil {
		return "", err
	}
	if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		return "", nil
	}
	return commonDir, nil
}

func expandedExtensionMounts(exts []*registry.Extension, home string, provider *Provider) []registry.ResolvedMount {
	if provider == nil {
		return nil
	}
	var mounts []registry.ResolvedMount
	for _, ext := range exts {
		if !ext.Enabled {
			continue
		}
		mounts = append(mounts, ext.ExpandedMounts(home, provider.HomePath())...)
	}
	return mounts
}

type discoveredNamedContainer struct {
	pool.ContainerInfo
	Name string
}

func discoverExactNameContainers(containerName string) ([]discoveredNamedContainer, error) {
	out, err := captureCommand("docker", "ps", "-a",
		"--filter", "name=^/"+regexp.QuoteMeta(containerName)+"$",
		"--format", `{{.ID}}\t{{.Names}}\t{{.State}}\t{{.Status}}`)
	if err != nil {
		return nil, err
	}

	var containers []discoveredNamedContainer
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 || parts[1] != containerName {
			continue
		}
		containers = append(containers, discoveredNamedContainer{
			ContainerInfo: pool.ContainerInfo{
				ContainerID: parts[0],
				State:       parts[2],
				Status:      parts[3],
			},
			Name: parts[1],
		})
	}
	return containers, nil
}

func removeStaleExactNameContainers(containerName string) error {
	containers, err := discoverExactNameContainers(containerName)
	if err != nil {
		return fmt.Errorf("discover existing container %q: %w", containerName, err)
	}

	for _, c := range containers {
		if c.IsRunning() {
			state := strings.TrimSpace(c.State)
			if state == "" {
				state = strings.TrimSpace(c.Status)
			}
			if state == "" {
				state = "active"
			}
			return fmt.Errorf("worker container %q already exists in %q state", containerName, state)
		}
	}

	for _, c := range containers {
		target := c.ContainerID
		if target == "" {
			target = c.Name
		}
		if err := exec.Command("docker", "rm", "-f", target).Run(); err != nil {
			return fmt.Errorf("remove stale container %q: %w", containerName, err)
		}
	}
	return nil
}

var poolProjectNonSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func kitchenPoolSessionID(repoPath string) string {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}
	base := strings.ToLower(filepath.Base(absPath))
	base = poolProjectNonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	sum := sha256.Sum256([]byte(absPath))
	return "kitchen-" + fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:10])
}

func poolEnvValue(args []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], prefix) {
			return strings.TrimPrefix(args[i+1], prefix)
		}
	}
	return ""
}

func poolLabels(args []string) []string {
	var labels []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-l" {
			labels = append(labels, args[i+1])
		}
	}
	sort.Strings(labels)
	return labels
}

func poolCPULimit(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--cpus" {
			return args[i+1]
		}
	}
	return ""
}

func poolMemoryLimit(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--memory" {
			return args[i+1]
		}
	}
	return ""
}

func poolInt(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}
