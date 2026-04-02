package pool

import (
	"path/filepath"
	"time"
)

const (
	WorkerStateDirName        = "workers"
	WorkerTaskFile            = "task.md"
	WorkerResultFile          = "result.txt"
	WorkerHandoverFile        = "handover.json"
	WorkerErrorFile           = "error.txt"
	WorkerActivityLogFile     = "activity.jsonl"
	WorkerActivityArchiveFile = "activity.prev.jsonl"

	// WorkerActivityLogMaxEntries caps each activity log generation. Older
	// entries roll into the archive file so recent history remains inspectable
	// without unbounded growth.
	WorkerActivityLogMaxEntries = 128
)

// WorkerActivityRecord is one persisted activity snapshot in the worker-side
// JSONL history file.
type WorkerActivityRecord struct {
	RecordedAt time.Time      `json:"recordedAt"`
	TaskID     string         `json:"taskId,omitempty"`
	Activity   WorkerActivity `json:"activity"`
}

// WorkerStateDir returns the per-worker state directory under the pool state dir.
func WorkerStateDir(stateDir, workerID string) string {
	return filepath.Join(stateDir, WorkerStateDirName, workerID)
}

// WorkerStatePath returns a file path within the per-worker state directory.
func WorkerStatePath(stateDir, workerID, name string) string {
	return filepath.Join(WorkerStateDir(stateDir, workerID), name)
}
