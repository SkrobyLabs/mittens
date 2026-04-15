package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestRuntimeWorkerStatusMissingContainerReportsDead(t *testing.T) {
	tests := []struct {
		name   string
		record runtimeWorkerRecord
	}{
		{
			name:   "stuck in spawning",
			record: runtimeWorkerRecord{ID: "w-1", ContainerID: "cid-1", Status: pool.WorkerSpawning},
		},
		{
			name:   "stuck in working",
			record: runtimeWorkerRecord{ID: "w-1", ContainerID: "cid-1", Status: pool.WorkerWorking},
		},
		{
			name:   "previously idle",
			record: runtimeWorkerRecord{ID: "w-1", ContainerID: "cid-1", Status: pool.WorkerIdle},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runtimeWorkerStatus(tc.record, pool.ContainerInfo{}, false, nil)
			if got != pool.WorkerDead {
				t.Fatalf("status = %q, want %q (container was externally removed)", got, pool.WorkerDead)
			}
		})
	}
}

func TestRuntimeWorkerStatusMissingContainerBeforeSpawnLeavesStatusUnchanged(t *testing.T) {
	// No ContainerID yet: the worker is mid-spawn and the host just
	// hasn't reported a container name back. We must not flip it to
	// dead in that state or we'd race legitimate startups.
	record := runtimeWorkerRecord{ID: "w-1", Status: pool.WorkerSpawning}
	got := runtimeWorkerStatus(record, pool.ContainerInfo{}, false, nil)
	if got != pool.WorkerSpawning {
		t.Fatalf("status = %q, want %q (pre-spawn record must not flip to dead)", got, pool.WorkerSpawning)
	}
}

func TestRuntimeWorkerStatusRunningContainerIsIdle(t *testing.T) {
	record := runtimeWorkerRecord{ID: "w-1", ContainerID: "cid-1", Status: pool.WorkerIdle}
	container := pool.ContainerInfo{ContainerID: "cid-1", State: "running"}
	got := runtimeWorkerStatus(record, container, true, nil)
	if got != pool.WorkerIdle {
		t.Fatalf("status = %q, want %q", got, pool.WorkerIdle)
	}
}

func TestRuntimeWorkerStatusDeadRecordSticks(t *testing.T) {
	record := runtimeWorkerRecord{ID: "w-1", ContainerID: "cid-1", Status: pool.WorkerDead}
	got := runtimeWorkerStatus(record, pool.ContainerInfo{ContainerID: "cid-1", State: "running"}, true, nil)
	if got != pool.WorkerDead {
		t.Fatalf("status = %q, want %q (dead must not un-flip)", got, pool.WorkerDead)
	}
}

func TestRuntimeWorkerViewPersistsDerivedIdleStatus(t *testing.T) {
	tmp := t.TempDir()
	dockerDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerPath := filepath.Join(dockerDir, "docker")
	script := `#!/bin/sh
case "$1" in
  ps)
    printf 'cid-1\tmittens-kitchen-test-w-1\trunning\tUp 10 seconds\n'
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := &App{
		poolSession:  "kitchen-test",
		poolStateDir: filepath.Join(tmp, "pool-state"),
	}
	if err := os.MkdirAll(app.poolStateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := app.saveRuntimeWorkerRecord(runtimeWorkerRecord{
		ID:          "w-1",
		ContainerID: "cid-1",
		Status:      pool.WorkerSpawning,
	}); err != nil {
		t.Fatalf("saveRuntimeWorkerRecord: %v", err)
	}

	view, err := app.runtimeWorkerView("w-1")
	if err != nil {
		t.Fatalf("runtimeWorkerView: %v", err)
	}
	if view.Status != pool.WorkerIdle {
		t.Fatalf("view status = %q, want %q", view.Status, pool.WorkerIdle)
	}

	record, err := app.loadRuntimeWorkerRecord("w-1")
	if err != nil {
		t.Fatalf("loadRuntimeWorkerRecord: %v", err)
	}
	if record.Status != pool.WorkerIdle {
		t.Fatalf("persisted status = %q, want %q", record.Status, pool.WorkerIdle)
	}
}
