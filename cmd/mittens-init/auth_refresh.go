package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

var (
	authProbeTimeout      = 30 * time.Second
	authProbePollInterval = 500 * time.Millisecond
	authRetryTimeout      = 30 * time.Second
	authRetryPollInterval = 2 * time.Second
)

type credEventLogger interface {
	event(event, format string, args ...interface{})
}

type callbackEventLogger struct {
	logFn func(string, ...interface{})
}

func (l callbackEventLogger) event(event, format string, args ...interface{}) {
	if l.logFn == nil {
		return
	}
	l.logFn("%s — %s", strings.TrimSpace(event), fmt.Sprintf(format, args...))
}

func credFilePath(cfg *config) string {
	if cfg == nil {
		return ""
	}
	return filepath.Join(cfg.AIDir, cfg.AICredFile)
}

func sandboxEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
	}
	if term := os.Getenv("TERM"); term != "" {
		env = append(env, "TERM="+term)
	}
	return env
}

func runAuthProbe(binary, credFile string, logFn func(string, ...interface{})) bool {
	binary = strings.TrimSpace(binary)
	credFile = strings.TrimSpace(credFile)
	if binary == "" || credFile == "" {
		if logFn != nil {
			logFn("auth-probe: skipped (missing binary or credential file)")
		}
		return false
	}

	beforeExp := credExpiresAtFile(credFile)
	if beforeExp <= 0 {
		if logFn != nil {
			logFn("auth-probe: before=%d refreshed=false (missing local expiresAt)", beforeExp)
		}
		return false
	}

	tmpHome, err := os.MkdirTemp("", "auth-probe-")
	if err != nil {
		if logFn != nil {
			logFn("auth-probe: temp home: %v", err)
		}
		return false
	}
	defer os.RemoveAll(tmpHome)

	configDirName := filepath.Base(filepath.Dir(credFile))
	credFileName := filepath.Base(credFile)
	tmpConfigDir := filepath.Join(tmpHome, configDirName)
	if err := os.MkdirAll(tmpConfigDir, 0o755); err != nil {
		if logFn != nil {
			logFn("auth-probe: create config dir: %v", err)
		}
		return false
	}

	tmpCredFile := filepath.Join(tmpConfigDir, credFileName)
	if err := fileutil.CopyFile(credFile, tmpCredFile); err != nil {
		if logFn != nil {
			logFn("auth-probe: stage credentials: %v", err)
		}
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--print", ".")
	cmd.Dir = tmpHome
	cmd.Env = sandboxEnv(tmpHome)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		if logFn != nil {
			logFn("auth-probe: start %s: %v", binary, err)
		}
		return false
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	refreshed := false
	ticker := time.NewTicker(authProbePollInterval)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-ticker.C:
			if credExpiresAtFile(tmpCredFile) > beforeExp {
				refreshed = true
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			break loop
		case <-waitCh:
			if credExpiresAtFile(tmpCredFile) > beforeExp {
				refreshed = true
			}
			break loop
		}
		if refreshed {
			<-waitCh
			break
		}
	}

	if refreshed {
		if err := copyCredFileAtomic(tmpCredFile, credFile); err != nil {
			if logFn != nil {
				logFn("auth-probe: copy refreshed credentials: %v", err)
			}
			return false
		}
	}

	afterExp := credExpiresAtFile(credFile)
	ok := afterExp > beforeExp
	if logFn != nil {
		logFn("auth-probe: before=%d after=%d refreshed=%t", beforeExp, afterExp, ok)
	}
	return ok
}

func restoreCredFile(credFile string, data []byte) error {
	info, err := os.Stat(credFile)
	mode := os.FileMode(0o600)
	if err == nil {
		mode = info.Mode()
	}
	tmp := credFile + fmt.Sprintf(".restore.%d", os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, credFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func triggerRefreshProbe(binary, credFile string, logger credEventLogger, logFn func(string, ...interface{})) bool {
	original, err := os.ReadFile(credFile)
	if err != nil {
		if logFn != nil {
			logFn("auth-probe: read original credentials: %v", err)
		}
		return false
	}
	triggerTokenRefresh(credFile, logger)
	if runAuthProbe(binary, credFile, logFn) {
		return true
	}
	if err := restoreCredFile(credFile, original); err != nil && logFn != nil {
		logFn("auth-probe: restore original credentials: %v", err)
	}
	return false
}

func copyCredFileAtomic(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	tmp := dst + fmt.Sprintf(".tmp.%d", os.Getpid())
	if err := os.WriteFile(tmp, data, info.Mode()); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func pushFreshCreds(bc *brokerClient, credFile string) error {
	if bc == nil {
		return fmt.Errorf("broker client is nil")
	}
	data, err := os.ReadFile(credFile)
	if err != nil {
		return err
	}
	if exp := credExpiresAt(data); exp <= 0 {
		return fmt.Errorf("credentials missing expiresAt")
	}
	code, err := bc.put("/", string(data))
	if err != nil {
		return err
	}
	if code != 204 && code != 409 {
		return fmt.Errorf("unexpected broker PUT status %d", code)
	}
	return nil
}

func attemptAuthRefresh(bc *brokerClient, cfg *config) bool {
	if bc == nil || cfg == nil {
		return false
	}

	credFile := credFilePath(cfg)
	baselineExp := credExpiresAtFile(credFile)
	if baselineExp <= 0 {
		logWarn("worker: auth refresh skipped (no valid credential expiry)")
		return false
	}

	action := brokerRefreshRequest(bc)
	switch action {
	case "refresh":
		logInfo("worker: auth refresh coordinator for %s", cfg.ProviderName)
		if !triggerRefreshProbe(cfg.AIBinary, credFile, callbackEventLogger{logFn: logInfo}, logInfo) {
			return false
		}
		if err := pushFreshCreds(bc, credFile); err != nil {
			logWarn("worker: push refreshed credentials: %v", err)
			return false
		}
		return credExpiresAtFile(credFile) > baselineExp
	case "wait":
		logInfo("worker: waiting for coordinated auth refresh")
		timeout := time.NewTimer(authRetryTimeout)
		defer timeout.Stop()
		ticker := time.NewTicker(authRetryPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-timeout.C:
				return false
			case <-ticker.C:
				remote, code, err := bc.get("/")
				if err != nil || code != 200 || strings.TrimSpace(remote) == "" {
					continue
				}
				remoteExp := credExpiresAt([]byte(remote))
				if remoteExp <= baselineExp {
					continue
				}
				tmp := credFile + fmt.Sprintf(".tmp.%d", os.Getpid())
				if err := os.WriteFile(tmp, []byte(remote), 0o600); err != nil {
					logWarn("worker: write refreshed credentials: %v", err)
					continue
				}
				if err := os.Rename(tmp, credFile); err != nil {
					_ = os.Remove(tmp)
					logWarn("worker: install refreshed credentials: %v", err)
					continue
				}
				return true
			}
		}
	default:
		return false
	}
}
