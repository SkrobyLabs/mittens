// team-mcp is the MCP server binary that runs inside the leader container.
// It manages the PoolManager, WorkerBroker, and WAL for the team session.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

const workerBrokerAddr = ":8080"

func startWorkerBroker(pm *pool.PoolManager, addr, token string, stderr io.Writer) (*WorkerBroker, error) {
	broker := NewWorkerBroker(pm, addr, token)
	if err := broker.Listen(); err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "startup: worker broker bind failed on %s: %v\n", addr, err)
		}
		return nil, err
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "startup: worker broker ready on %s\n", broker.ln.Addr().String())
	}
	return broker, nil
}

func serveWorkerBroker(broker *WorkerBroker, stderr io.Writer) {
	go func() {
		if err := broker.Serve(); err != nil && stderr != nil {
			fmt.Fprintf(stderr, "worker broker: %v\n", err)
		}
	}()
}

func main() {
	stateDir := os.Getenv("MITTENS_STATE_DIR")
	sessionID := os.Getenv("MITTENS_SESSION_ID")
	brokerPort := os.Getenv("MITTENS_BROKER_PORT")
	brokerToken := os.Getenv("MITTENS_BROKER_TOKEN")
	maxWorkersEnv := os.Getenv("MITTENS_MAX_WORKERS")

	teamConfigPath := os.Getenv("MITTENS_TEAM_CONFIG")

	if stateDir == "" {
		fmt.Fprintln(os.Stderr, "MITTENS_STATE_DIR is required")
		os.Exit(1)
	}
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "MITTENS_SESSION_ID is required")
		os.Exit(1)
	}
	if brokerToken == "" {
		fmt.Fprintln(os.Stderr, "MITTENS_BROKER_TOKEN is required (empty token is insecure)")
		os.Exit(1)
	}

	maxWorkers := 4
	if maxWorkersEnv != "" {
		fmt.Sscanf(maxWorkersEnv, "%d", &maxWorkers)
	}

	// Ensure state directory exists.
	os.MkdirAll(stateDir, 0755)

	runtimeLock, err := acquireRuntimeLock(stateDir, sessionID, os.Stderr)
	if err != nil {
		os.Exit(1)
	}
	defer runtimeLock.Close()

	walPath := filepath.Join(stateDir, "events.jsonl")
	wal, err := pool.OpenWAL(walPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open WAL: %v\n", err)
		os.Exit(1)
	}
	defer wal.Close()

	// Load per-role model routing from team.yaml if configured.
	var router *pool.ModelRouter
	if teamConfigPath != "" {
		tc, err := pool.LoadTeamConfig(teamConfigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load team config: %v\n", err)
			os.Exit(1)
		}
		if len(tc.Models) > 0 {
			router = pool.NewModelRouter(tc.Models)
		}
	}

	// Initialize PlanStore from MITTENS_PLANS_DIR.
	var planStore *pool.PlanStore
	if plansDir := os.Getenv("MITTENS_PLANS_DIR"); plansDir != "" {
		planStore = pool.NewPlanStore(plansDir)
	}

	cfg := pool.PoolConfig{
		SessionID:  sessionID,
		MaxWorkers: maxWorkers,
		StateDir:   stateDir,
		Router:     router,
		PlanStore:  planStore,
	}

	poolToken := os.Getenv("MITTENS_POOL_TOKEN")
	var hostAPI pool.HostAPI
	if brokerPort != "" && brokerPort != "0" {
		hostAPI = newHostAPIClient(brokerPort, brokerToken, poolToken)
	}

	pm, err := pool.RecoverPoolManager(cfg, wal, hostAPI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recover pool: %v\n", err)
		os.Exit(1)
	}

	// Crash recovery: reconcile WAL state against running containers.
	if hostAPI != nil {
		containers, err := hostAPI.ListContainers(context.Background(), sessionID)
		if err == nil {
			reconciled, killed := pool.Reconcile(pm, containers)
			if reconciled > 0 || killed > 0 {
				fmt.Fprintf(os.Stderr, "recovery: reconciled %d workers, killed %d orphans\n", reconciled, killed)
			}
		} else {
			fmt.Fprintf(os.Stderr, "recovery: list containers: %v\n", err)
		}
	}

	// Re-queue tasks stranded on dead workers.
	if requeued := pool.RequeueOrphanedTasks(pm); requeued > 0 {
		fmt.Fprintf(os.Stderr, "recovery: re-queued %d orphaned tasks\n", requeued)
	}

	// Start WorkerBroker HTTP server for workers. The bind must succeed
	// before MCP stdio starts so split-brain runtimes fail fast.
	broker, err := startWorkerBroker(pm, workerBrokerAddr, brokerToken, os.Stderr)
	if err != nil {
		os.Exit(1)
	}
	serveWorkerBroker(broker, os.Stderr)

	// Create MCP server for leader communication.
	srv := &mcpServer{pm: pm}

	// Start heartbeat reaper to detect crashed workers.
	stopReaper := pool.StartReaper(pm, 30*time.Second, 90*time.Second)
	defer stopReaper()

	// Pipeline executor consumes notifications and drives stage advancement.
	// The single goroutine reads pm.Notify() and both drives the executor
	// and pushes MCP notifications to the leader (fan-out).
	executor := pool.NewPipelineExecutor(pm)

	// Scan for stuck pipelines after crash recovery.
	executor.ScanStuckPipelines()

	go func() {
		for n := range pm.Notify() {
			// Drive pipeline executor.
			switch n.Type {
			case "task_completed", "task_failed":
				t, ok := pm.Task(n.ID)
				if ok {
					executor.OnTaskEvent(n.ID, t.Status)
				}
			case "review_pass", "escalation_accept":
				t, ok := pm.Task(n.ID)
				if ok {
					executor.OnTaskEvent(n.ID, t.Status)
				}
			case "escalation_retry":
				// Task re-enters dispatch cycle; no pipeline action needed.
			case "escalation_abort":
				t, ok := pm.Task(n.ID)
				if ok {
					executor.OnTaskEvent(n.ID, t.Status)
				}
			}

			// Push notification to leader via MCP.
			srv.pushNotification(n)
		}
	}()

	// MCP stdio server: read JSON-RPC from stdin, respond on stdout.
	srv.serve()
}

// mcpServer wraps the PoolManager and synchronizes stdout writes between
// tool call responses (synchronous) and notification pushes (async).
type mcpServer struct {
	pm *pool.PoolManager
	mu sync.Mutex // protects stdout writes
}

// writeJSON marshals v as JSON and writes it to stdout, holding the mutex.
func (s *mcpServer) writeJSON(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(v)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

// serve runs the MCP stdio read loop.
func (s *mcpServer) serve() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			s.writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities": map[string]any{
						"tools": map[string]any{
							"listChanged": false,
						},
					},
					"serverInfo": map[string]any{
						"name":    "team-mcp",
						"version": "0.2.0",
					},
				},
			})

		case "notifications/initialized":
			// No response needed for notifications.

		case "tools/list":
			s.handleToolsList(msg.ID)

		case "tools/call":
			go s.handleToolsCall(msg.ID, msg.Params)

		case "ping":
			s.writeJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{},
			})
		}
	}
}

// handleToolsList returns all registered tool definitions.
func (s *mcpServer) handleToolsList(id json.RawMessage) {
	tools := make([]map[string]any, 0, len(mcpTools))
	for _, t := range mcpTools {
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema,
		})
	}
	s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"tools": tools},
	})
}

// handleToolsCall dispatches a tool call to the appropriate handler.
func (s *mcpServer) handleToolsCall(id json.RawMessage, rawParams json.RawMessage) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rawParams, &call); err != nil {
		s.writeJSON(mcpError(id, -32602, "invalid params: "+err.Error()))
		return
	}

	// Find the tool handler.
	var handler func(*pool.PoolManager, json.RawMessage) (any, error)
	for _, t := range mcpTools {
		if t.Name == call.Name {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		s.writeJSON(mcpError(id, -32601, fmt.Sprintf("unknown tool: %s", call.Name)))
		return
	}

	// Default to empty object if no arguments provided.
	args := call.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}

	result, err := handler(s.pm, args)
	if err != nil {
		code := -32603 // internal error
		if strings.Contains(err.Error(), "missing required") || strings.Contains(err.Error(), "invalid params") {
			code = -32602
		}
		s.writeJSON(mcpError(id, code, err.Error()))
		return
	}

	// MCP tools/call returns content as an array of content blocks.
	resultJSON, _ := json.Marshal(result)
	s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(resultJSON)},
			},
		},
	})
}

// mcpError creates an MCP JSON-RPC error response.
func mcpError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}
