package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func newTestHostBrokerWithPool(t *testing.T) *HostBroker {
	t.Helper()
	b := NewHostBroker("", "", nil)
	b.OnPoolSpawn = func(spec pool.WorkerSpec) (string, string, error) {
		return "mittens-pool-" + spec.ID, "sha256:" + spec.ID, nil
	}
	b.OnPoolKill = func(workerID string) error {
		return nil
	}
	return b
}

func doPoolReq(t *testing.T, b *HostBroker, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)
	return rr
}

func TestPoolSpawn(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/spawn", pool.WorkerSpec{ID: "w-1", Role: "impl"})

	if rr.Code != http.StatusOK {
		t.Fatalf("spawn: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp poolSpawnResp
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ContainerName != "mittens-pool-w-1" {
		t.Errorf("containerName = %q", resp.ContainerName)
	}
	if resp.ContainerID != "sha256:w-1" {
		t.Errorf("containerID = %q", resp.ContainerID)
	}
}

func TestPoolSpawn_NotConfigured(t *testing.T) {
	b := NewHostBroker("", "", nil)
	rr := doPoolReq(t, b, "POST", "/pool/spawn", pool.WorkerSpec{ID: "w-1"})

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured spawn: got %d, want 503", rr.Code)
	}
}

func TestPoolKill(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/kill", map[string]string{"workerId": "w-1"})

	if rr.Code != http.StatusOK {
		t.Fatalf("kill: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestPoolKill_MissingID(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/kill", map[string]string{})

	if rr.Code != http.StatusBadRequest {
		t.Errorf("kill no id: got %d, want 400", rr.Code)
	}
}

func TestPoolSpawn_WrongMethod(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "GET", "/pool/spawn", nil)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: got %d, want 405", rr.Code)
	}
}

func TestPoolContainers_IncludesNonRunningContainerState(t *testing.T) {
	b := newTestHostBrokerWithPool(t)

	tmp := t.TempDir()
	argsPath := filepath.Join(tmp, "docker-args.txt")
	dockerPath := filepath.Join(tmp, "docker")
	script := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > '" + argsPath + "'\n" +
		"printf 'cid-running\tw-1\trunning\tUp 5 minutes\ncid-exited\tw-2\texited\tExited (0) 1 minute ago\n'\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	rr := doPoolReq(t, b, http.MethodGet, "/pool/containers?sessionId=team-123", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list containers: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read docker args: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "ps\n-a\n") {
		t.Fatalf("docker args = %q, want ps with -a", args)
	}
	if !strings.Contains(args, "label=mittens.pool=team-123") {
		t.Fatalf("docker args = %q, want session filter", args)
	}

	var containers []pool.ContainerInfo
	if err := json.NewDecoder(rr.Body).Decode(&containers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("len(containers) = %d, want 2", len(containers))
	}
	if containers[0].State != "running" {
		t.Fatalf("containers[0].State = %q, want running", containers[0].State)
	}
	if containers[1].State != "exited" {
		t.Fatalf("containers[1].State = %q, want exited", containers[1].State)
	}
	if containers[1].Status != "Exited (0) 1 minute ago" {
		t.Fatalf("containers[1].Status = %q, want exited status", containers[1].Status)
	}
}

func TestPoolContainers_AllowsDottedSessionID(t *testing.T) {
	b := newTestHostBrokerWithPool(t)

	tmp := t.TempDir()
	argsPath := filepath.Join(tmp, "docker-args.txt")
	dockerPath := filepath.Join(tmp, "docker")
	script := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > '" + argsPath + "'\n" +
		"printf 'cid-running\tw-1\trunning\tUp 5 minutes\n'\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	rr := doPoolReq(t, b, http.MethodGet, "/pool/containers?sessionId=release.v1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list containers: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read docker args: %v", err)
	}
	if got := string(argsData); !strings.Contains(got, "label=mittens.pool=release.v1") {
		t.Fatalf("docker args = %q, want dotted session filter", got)
	}
}

func TestPoolContainers_AllowsLeadingPunctuationSessionID(t *testing.T) {
	b := newTestHostBrokerWithPool(t)

	tmp := t.TempDir()
	argsPath := filepath.Join(tmp, "docker-args.txt")
	dockerPath := filepath.Join(tmp, "docker")
	script := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > '" + argsPath + "'\n" +
		"printf 'cid-running\tw-1\trunning\tUp 5 minutes\n'\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	rr := doPoolReq(t, b, http.MethodGet, "/pool/containers?sessionId=.scratch", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list containers: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read docker args: %v", err)
	}
	if got := string(argsData); !strings.Contains(got, "label=mittens.pool=.scratch") {
		t.Fatalf("docker args = %q, want dotted-leading session filter", got)
	}
}

func TestSessionAlive_AllowsDottedSessionID(t *testing.T) {
	b := newTestHostBrokerWithPool(t)

	tmp := t.TempDir()
	argsPath := filepath.Join(tmp, "docker-args.txt")
	dockerPath := filepath.Join(tmp, "docker")
	script := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > '" + argsPath + "'\n" +
		"printf 'leader-123\n'\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	rr := doPoolReq(t, b, http.MethodGet, "/pool/session-alive?sessionId=release.v1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("session alive: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read docker args: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "label=mittens.pool=release.v1") {
		t.Fatalf("docker args = %q, want dotted session filter", args)
	}
	if !strings.Contains(args, "label=mittens.role=leader") {
		t.Fatalf("docker args = %q, want leader filter", args)
	}
}
