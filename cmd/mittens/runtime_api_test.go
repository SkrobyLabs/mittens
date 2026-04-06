package main

import (
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
