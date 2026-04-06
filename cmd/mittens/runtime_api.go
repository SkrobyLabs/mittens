package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const runtimeAssignmentFile = "assignment.json"
const runtimeActivityScannerMaxToken = 1 << 20

type runtimeWorkerRecord struct {
	ID                string           `json:"id"`
	ContainerID       string           `json:"containerId,omitempty"`
	ContainerName     string           `json:"containerName,omitempty"`
	Provider          string           `json:"provider,omitempty"`
	Model             string           `json:"model,omitempty"`
	Adapter           string           `json:"adapter,omitempty"`
	Role              string           `json:"role,omitempty"`
	WorkspacePath     string           `json:"workspacePath,omitempty"`
	CurrentAssignment *pool.Assignment `json:"currentAssignment,omitempty"`
	Status            string           `json:"status,omitempty"`
}

func (a *App) runtimeWorkersDir() string {
	return filepath.Join(a.poolStateDir, "runtime", "workers")
}

func (a *App) runtimeWorkerRecordPath(workerID string) string {
	return filepath.Join(a.runtimeWorkersDir(), workerID+".json")
}

func (a *App) loadRuntimeWorkerRecord(workerID string) (runtimeWorkerRecord, error) {
	data, err := os.ReadFile(a.runtimeWorkerRecordPath(workerID))
	if err != nil {
		return runtimeWorkerRecord{}, err
	}
	var record runtimeWorkerRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return runtimeWorkerRecord{}, fmt.Errorf("decode runtime worker %q: %w", workerID, err)
	}
	if record.ID == "" {
		record.ID = workerID
	}
	return record, nil
}

func (a *App) saveRuntimeWorkerRecord(record runtimeWorkerRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("runtime worker record requires id")
	}
	if err := os.MkdirAll(a.runtimeWorkersDir(), 0o755); err != nil {
		return fmt.Errorf("create runtime worker dir: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime worker %q: %w", record.ID, err)
	}
	path := a.runtimeWorkerRecordPath(record.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write runtime worker temp %q: %w", record.ID, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename runtime worker %q: %w", record.ID, err)
	}
	return nil
}

func (a *App) listRuntimeWorkerIDs() ([]string, error) {
	entries, err := os.ReadDir(a.runtimeWorkersDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

func (a *App) recordRuntimeWorkerSpawn(spec pool.WorkerSpec, containerName, containerID string) error {
	record := runtimeWorkerRecord{
		ID:            spec.ID,
		ContainerID:   containerID,
		ContainerName: containerName,
		Provider:      spec.Provider,
		Model:         spec.Model,
		Adapter:       spec.Adapter,
		Role:          spec.Role,
		WorkspacePath: spec.WorkspacePath,
		Status:        pool.WorkerSpawning,
	}
	return a.saveRuntimeWorkerRecord(record)
}

func (a *App) markRuntimeWorkerDead(workerID string) error {
	record, err := a.loadRuntimeWorkerRecord(workerID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	record.Status = pool.WorkerDead
	record.CurrentAssignment = nil
	return a.saveRuntimeWorkerRecord(record)
}

func (a *App) runtimeContainerInfo(workerID string) (pool.ContainerInfo, bool, error) {
	containerName := fmt.Sprintf("mittens-%s-%s", a.poolSession, workerID)
	containers, err := discoverExactNameContainers(containerName)
	if err != nil {
		return pool.ContainerInfo{}, false, err
	}
	if len(containers) == 0 {
		return pool.ContainerInfo{}, false, nil
	}
	return containers[0].ContainerInfo, true, nil
}

func (a *App) runtimeActivity(workerID string) (*pool.WorkerActivity, []pool.WorkerActivityRecord, error) {
	workerDir := pool.WorkerStateDir(a.poolStateDir, workerID)
	paths := []string{
		filepath.Join(workerDir, pool.WorkerActivityArchiveFile),
		filepath.Join(workerDir, pool.WorkerActivityLogFile),
	}
	var transcript []pool.WorkerActivityRecord
	for _, path := range paths {
		records, err := readActivityTranscript(path)
		if err != nil {
			return nil, nil, err
		}
		transcript = append(transcript, records...)
	}
	if len(transcript) == 0 {
		return nil, nil, nil
	}
	last := transcript[len(transcript)-1].Activity
	return &last, transcript, nil
}

func readActivityTranscript(path string) ([]pool.WorkerActivityRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), runtimeActivityScannerMaxToken)
	var out []pool.WorkerActivityRecord
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record pool.WorkerActivityRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("decode activity record %s: %w", path, err)
		}
		out = append(out, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func summarizeAssignment(assignment *pool.Assignment) *pool.AssignmentSummary {
	if assignment == nil {
		return nil
	}
	return &pool.AssignmentSummary{
		ID:   assignment.ID,
		Type: assignment.Type,
	}
}

func runtimeWorkerStatus(record runtimeWorkerRecord, container pool.ContainerInfo, hasContainer bool, activity *pool.WorkerActivity) string {
	if record.Status == pool.WorkerDead {
		return pool.WorkerDead
	}
	if hasContainer && !container.IsRunning() {
		if container.State != "" {
			return container.State
		}
		if container.Status != "" {
			return container.Status
		}
		return pool.WorkerDead
	}
	// Container was previously recorded (ContainerID set) but has
	// disappeared from the host (e.g. operator ran `docker rm -f`).
	// Without this, the stale record.Status ("spawning" or "working")
	// would leak through and the scheduler's runtime client would keep
	// reporting the worker as running.
	if !hasContainer && strings.TrimSpace(record.ContainerID) != "" {
		return pool.WorkerDead
	}
	if record.CurrentAssignment != nil {
		return pool.WorkerWorking
	}
	if activity != nil {
		return pool.WorkerWorking
	}
	if hasContainer && container.IsRunning() {
		return pool.WorkerIdle
	}
	if record.Status != "" {
		return record.Status
	}
	return "unknown"
}

func (a *App) runtimeWorkerView(workerID string) (*pool.RuntimeWorker, error) {
	record, err := a.loadRuntimeWorkerRecord(workerID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	container, hasContainer, err := a.runtimeContainerInfo(workerID)
	if err != nil {
		return nil, err
	}
	activity, _, err := a.runtimeActivity(workerID)
	if err != nil {
		return nil, err
	}

	status := runtimeWorkerStatus(record, container, hasContainer, activity)
	// Persist dead state so subsequent reads short-circuit and so other
	// consumers (status listings, reconcile loops) don't have to
	// recompute the transition each time.
	if status == pool.WorkerDead && record.Status != pool.WorkerDead {
		if err := a.markRuntimeWorkerDead(record.ID); err != nil {
			return nil, err
		}
	}

	view := &pool.RuntimeWorker{
		ID:                record.ID,
		ContainerID:       record.ContainerID,
		Status:            status,
		Provider:          record.Provider,
		Model:             record.Model,
		Adapter:           record.Adapter,
		Role:              record.Role,
		WorkspacePath:     record.WorkspacePath,
		CurrentAssignment: summarizeAssignment(record.CurrentAssignment),
		CurrentActivity:   activity,
	}
	if hasContainer && container.ContainerID != "" {
		view.ContainerID = container.ContainerID
	}
	return view, nil
}

func (a *App) runtimeWorkers() ([]pool.RuntimeWorker, error) {
	ids, err := a.listRuntimeWorkerIDs()
	if err != nil {
		return nil, err
	}
	workers := make([]pool.RuntimeWorker, 0, len(ids))
	for _, workerID := range ids {
		worker, err := a.runtimeWorkerView(workerID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		workers = append(workers, *worker)
	}
	return workers, nil
}

// recycleRuntimeWorker clears runtime metadata and worker artifacts for a
// worker. The paired runtime event lets Kitchen request an adapter
// force-clean on the worker's next broker poll, so recycle remains
// asynchronous and non-interrupting for in-flight tasks.
func (a *App) recycleRuntimeWorker(workerID string) error {
	record, err := a.loadRuntimeWorkerRecord(workerID)
	if err != nil {
		return err
	}
	record.CurrentAssignment = nil
	record.Status = pool.WorkerIdle
	if err := a.saveRuntimeWorkerRecord(record); err != nil {
		return err
	}

	workerDir := pool.WorkerStateDir(a.poolStateDir, workerID)
	for _, name := range []string{
		pool.WorkerTaskFile,
		pool.WorkerResultFile,
		pool.WorkerPlanFile,
		pool.WorkerHandoverFile,
		pool.WorkerErrorFile,
		pool.WorkerActivityLogFile,
		pool.WorkerActivityArchiveFile,
		runtimeAssignmentFile,
	} {
		err := os.Remove(filepath.Join(workerDir, name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// submitRuntimeAssignment persists assignment intent for runtime inspection and
// future control-path work. Workers do not consume this file today; active work
// still flows through the Kitchen WorkerBroker polling model.
func (a *App) submitRuntimeAssignment(workerID string, assignment pool.Assignment) error {
	record, err := a.loadRuntimeWorkerRecord(workerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(assignment.ID) == "" || strings.TrimSpace(assignment.Type) == "" {
		return fmt.Errorf("assignment requires assignmentId and type")
	}
	record.CurrentAssignment = &assignment
	record.Status = pool.WorkerWorking
	if err := a.saveRuntimeWorkerRecord(record); err != nil {
		return err
	}

	workerDir := pool.WorkerStateDir(a.poolStateDir, workerID)
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(assignment, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workerDir, runtimeAssignmentFile), append(data, '\n'), 0o644)
}
