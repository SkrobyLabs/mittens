package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// MCPProxySpec is a pin-verified, user-approved MCP server the broker may run on
// the host on behalf of a container shim.
type MCPProxySpec struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	Dir     string
}

// mcpChild tracks one running host-side MCP process for a server name.
type mcpChild struct {
	cmd      *exec.Cmd
	pgid     int
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	waitOnce sync.Once
}

// wait reaps the child exactly once, no matter how many callers invoke it.
func (c *mcpChild) wait() {
	c.waitOnce.Do(func() { _ = c.cmd.Wait() })
}

// RegisterMCPServers records the proxy specs the broker will honour. Only names
// registered here are reachable via /mcp/<name>; the toggle list in policy is
// therefore the complete, enforced set of host processes reachable from the
// sandbox. Call before Serve() (or before the container starts). Each spec's
// working directory is resolved here (falling back to the session workspace) so
// spawnMCPChild never reads shared broker state without a lock.
func (b *HostBroker) RegisterMCPServers(specs []MCPProxySpec) {
	b.mcpMu.Lock()
	defer b.mcpMu.Unlock()
	if b.mcpSpecs == nil {
		b.mcpSpecs = map[string]MCPProxySpec{}
	}
	for _, s := range specs {
		if s.Dir == "" {
			s.Dir = b.mcpWorkspace
		}
		b.mcpSpecs[s.Name] = s
		b.blog("mcp: registered proxy endpoint /mcp/%s", s.Name)
	}
}

// mcpNameLock returns the per-name mutex used to serialize kill-and-respawn for
// a single server, so a slow teardown of one server never blocks streams for
// other servers.
func (b *HostBroker) mcpNameLock(name string) *sync.Mutex {
	b.mcpMu.Lock()
	defer b.mcpMu.Unlock()
	if b.mcpLocks == nil {
		b.mcpLocks = map[string]*sync.Mutex{}
	}
	nl := b.mcpLocks[name]
	if nl == nil {
		nl = &sync.Mutex{}
		b.mcpLocks[name] = nl
	}
	return nl
}

// handleMCPStream hijacks the connection and pipes MCP stdio bytes between the
// container shim and a host-side child process for the named server.
func (b *HostBroker) handleMCPStream(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if name == "" || strings.ContainsRune(name, '/') {
		http.Error(w, "invalid mcp server name", http.StatusBadRequest)
		return
	}

	b.mcpMu.Lock()
	spec, ok := b.mcpSpecs[name]
	b.mcpMu.Unlock()
	if !ok {
		b.blog("mcp: %q not registered -> 404", name)
		http.Error(w, "mcp server not registered", http.StatusNotFound)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		b.blog("mcp: hijack failed for %q: %v", name, err)
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n"); err != nil {
		conn.Close()
		return
	}
	b.runMCPChild(name, spec, conn)
}

// runMCPChild kills any prior child for name (serialized by a per-name lock so
// two children for one name never coexist), spawns a fresh one, and pipes bytes
// until the stream ends. Neither the global mcpMu nor any blocking wait is held
// across stream I/O; the per-name lock is held only for the state transition
// {kill old -> reap -> spawn -> record}, so a slow teardown blocks only
// reconnects of the SAME name.
func (b *HostBroker) runMCPChild(name string, spec MCPProxySpec, conn net.Conn) {
	// Closing conn also unblocks the stdin pump goroutine's read below.
	defer conn.Close()

	nl := b.mcpNameLock(name)
	nl.Lock()

	b.mcpMu.Lock()
	old := b.mcpChildren[name]
	if old != nil {
		delete(b.mcpChildren, name)
	}
	b.mcpMu.Unlock()
	if old != nil {
		b.blog("mcp: %q reconnect, terminating previous child", name)
		killChildGroup(old)
		old.wait()
	}

	child, err := b.spawnMCPChild(spec)
	if err != nil {
		nl.Unlock()
		b.blog("mcp: %q spawn failed: %v", name, err)
		return
	}
	b.mcpMu.Lock()
	if b.mcpChildren == nil {
		b.mcpChildren = map[string]*mcpChild{}
	}
	b.mcpChildren[name] = child
	b.mcpMu.Unlock()
	nl.Unlock()
	b.blog("mcp: %q started (pid %d)", name, child.pgid)

	// stdin: shim -> child. On EOF (shim CloseWrite), propagate half-close.
	go func() {
		_, _ = io.Copy(child.stdin, conn)
		_ = child.stdin.Close()
	}()

	// stdout: child -> shim. Ending here (child stdout EOF or conn write error)
	// is the teardown signal for both graceful and abrupt disconnects.
	_, _ = io.Copy(conn, child.stdout)

	b.mcpMu.Lock()
	if b.mcpChildren[name] == child {
		delete(b.mcpChildren, name)
	}
	b.mcpMu.Unlock()

	killChildGroup(child)
	child.wait()
	b.blog("mcp: %q stream ended", name)
}

// spawnMCPChild starts the host MCP process in its own process group. Child cwd
// comes from the spec (resolved at registration time, so no shared broker state
// is read here).
func (b *HostBroker) spawnMCPChild(spec MCPProxySpec) (*mcpChild, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	// Stream stderr to the broker log via an io.Writer. Using cmd.Stderr (rather
	// than StderrPipe) lets cmd.Wait manage the copy's lifetime, avoiding the
	// documented StderrPipe-vs-Wait read race.
	cmd.Stderr = &mcpStderrLogger{broker: b, name: spec.Name}
	setChildProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &mcpChild{cmd: cmd, pgid: cmd.Process.Pid, stdin: stdin, stdout: stdout}, nil
}

// mcpStderrLogger writes a child's stderr to the broker log, line by line, with
// values never logged beyond what the server itself emits to stderr.
type mcpStderrLogger struct {
	broker *HostBroker
	name   string
	buf    []byte
}

func (w *mcpStderrLogger) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.broker.blog("mcp[%s] stderr: %s", w.name, line)
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// closeMCPChildren terminates all live MCP children. Called from Close().
func (b *HostBroker) closeMCPChildren() {
	b.mcpMu.Lock()
	children := make([]*mcpChild, 0, len(b.mcpChildren))
	for name, child := range b.mcpChildren {
		children = append(children, child)
		delete(b.mcpChildren, name)
	}
	b.mcpMu.Unlock()
	for _, child := range children {
		killChildGroup(child)
		child.wait()
	}
}

// mergeEnv overlays overlay onto base (overlay wins), matching how agent CLIs
// spawn MCP servers. Redaction happens at log time; values are not logged.
func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overlay))
	skip := map[string]struct{}{}
	for k := range overlay {
		skip[k] = struct{}{}
	}
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			if _, ok := skip[kv[:i]]; ok {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overlay))
	for k := range overlay {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overlay[k])
	}
	return out
}

// redactEnvNames returns the sorted env variable names in an env map for
// logging; values are never logged.
func redactEnvNames(env map[string]string) string {
	if len(env) == 0 {
		return "none"
	}
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
