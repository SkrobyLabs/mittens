package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type Scheduler struct {
	pm                *pool.PoolManager
	hostAPI           pool.RuntimeAPI
	router            *ComplexityRouter
	git               *GitManager
	plans             *PlanStore
	lineages          *LineageManager
	cfg               ConcurrencyConfig
	failurePolicy     map[string]FailurePolicyRule
	sessionID         string
	kitchenAddr       string
	notify            func(pool.Notification)
	activatePlan      func(string) error
	pendingSpawn      map[string]string
	reconcileInterval time.Duration
	reapInterval      time.Duration
	reapTimeout       time.Duration
	nowFunc           func() time.Time
	stderr            io.Writer
}

func NewScheduler(pm *pool.PoolManager, hostAPI pool.RuntimeAPI, router *ComplexityRouter, git *GitManager, plans *PlanStore, lineages *LineageManager, cfg ConcurrencyConfig, sessionID string) *Scheduler {
	return &Scheduler{
		pm:                pm,
		hostAPI:           hostAPI,
		router:            router,
		git:               git,
		plans:             plans,
		lineages:          lineages,
		cfg:               cfg,
		failurePolicy:     DefaultKitchenConfig().FailurePolicy,
		sessionID:         sessionID,
		pendingSpawn:      make(map[string]string),
		reconcileInterval: 5 * time.Second,
		reapInterval:      30 * time.Second,
		reapTimeout:       90 * time.Second,
		nowFunc:           time.Now,
		stderr:            os.Stderr,
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	if s == nil || s.pm == nil {
		return
	}

	stopReaper := pool.StartReaper(s.pm, s.reapInterval, s.reapTimeout)
	defer stopReaper()

	if err := s.reconcile(); err != nil {
		s.logf("scheduler reconcile: %v", err)
	}
	if err := s.enforceTaskTimeouts(); err != nil {
		s.logf("scheduler timeouts: %v", err)
	}
	if err := s.schedule(); err != nil {
		s.logf("scheduler: %v", err)
	}

	ticker := time.NewTicker(s.reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case n := <-s.pm.Notify():
			s.handleNotification(n)
			if err := s.schedule(); err != nil {
				s.logf("scheduler: %v", err)
			}
		case <-ticker.C:
			if err := s.reconcile(); err != nil {
				s.logf("scheduler reconcile: %v", err)
			}
			if err := s.enforceTaskTimeouts(); err != nil {
				s.logf("scheduler timeouts: %v", err)
			}
			if err := s.schedule(); err != nil {
				s.logf("scheduler: %v", err)
			}
		}
	}
}

func (s *Scheduler) handleNotification(n pool.Notification) {
	switch n.Type {
	case "task_completed":
		if err := s.onTaskCompleted(n.ID); err != nil {
			s.logf("task %s completion handling: %v", n.ID, err)
		}
	case "task_failed":
		task, ok := s.pm.Task(n.ID)
		if !ok {
			return
		}
		var reported string
		if task.Result != nil {
			reported = task.Result.Error
		}
		if err := s.onTaskFailed(n.ID, ClassifyFailure(reported, nil, KitchenSignals{})); err != nil {
			s.logf("task %s failure handling: %v", n.ID, err)
		}
	case "task_requeued":
		_ = s.onWorkerIdle("")
	case "task_canceled":
		task, ok := s.pm.Task(n.ID)
		if ok && strings.TrimSpace(task.PlanID) != "" {
			if err := s.syncPlanExecution(task.PlanID); err != nil {
				s.logf("task %s cancel handling: %v", n.ID, err)
			}
		}
	}
}

func (s *Scheduler) schedule() error {
	if s == nil || s.pm == nil {
		return nil
	}

	s.refreshPendingSpawns()

	for taskID, workerID := range s.pendingSpawn {
		worker, wok := s.pm.Worker(workerID)
		task, tok := s.pm.Task(taskID)
		if !wok || !tok || task.Status != pool.TaskQueued || worker.Status != pool.WorkerIdle {
			continue
		}
		if err := s.pm.DispatchTask(taskID, workerID); err != nil {
			return err
		}
		delete(s.pendingSpawn, taskID)
	}

	idleWorkers := idleWorkerIDs(s.pm.Workers(), s.pendingSpawn)
	for _, workerID := range idleWorkers {
		if err := s.dispatchReadyTaskToWorker(workerID); err != nil {
			return err
		}
	}

	queued := s.pm.QueuedTasks()
	if len(queued) == 0 {
		return nil
	}

	availableCapacity := s.cfg.MaxWorkersTotal - s.pm.AliveWorkers()
	if availableCapacity <= 0 {
		return nil
	}

	sort.Slice(queued, func(i, j int) bool {
		return queued[i].Priority < queued[j].Priority
	})
	for _, task := range queued {
		if availableCapacity <= 0 {
			break
		}
		if _, exists := s.pendingSpawn[task.ID]; exists {
			continue
		}
		allowed, err := s.lineageHasCapacity(task)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		if err := s.spawnWorkerForTask(task); err != nil {
			return err
		}
		availableCapacity--
	}
	return nil
}

func (s *Scheduler) onTaskCompleted(taskID string) error {
	if s.plans == nil {
		return nil
	}
	task, ok := s.pm.Task(taskID)
	if !ok || task.PlanID == "" {
		return nil
	}
	if task.Role == plannerTaskRole {
		return s.onPlannerTaskCompleted(*task)
	}
	if isPlanReviewTask(*task) {
		return s.onPlanReviewCompleted(*task)
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	if bundle.Plan.Lineage == "" {
		return s.syncPlanExecution(task.PlanID)
	}
	if err := s.git.MergeChild(bundle.Plan.Lineage, taskID); err != nil {
		return s.onTaskMergeFailed(*task, bundle.Plan.Lineage, err)
	}
	if err := s.git.DiscardChild(bundle.Plan.Lineage, taskID); err != nil {
		return err
	}
	return s.syncPlanExecution(task.PlanID)
}

func (s *Scheduler) onTaskMergeFailed(task pool.Task, lineage string, mergeErr error) error {
	class := FailureInfrastructure
	if strings.Contains(strings.ToLower(mergeErr.Error()), "merge conflict") {
		class = FailureConflict
	}
	if err := s.pm.FailCompletedTask(task.ID, mergeErr.Error()); err != nil {
		return err
	}
	if err := s.git.DiscardChild(lineage, task.ID); err != nil {
		s.logf("task %s merge failure cleanup: %v", task.ID, err)
	}
	if err := s.syncPlanExecution(task.PlanID); err != nil {
		return err
	}
	if err := s.onTaskFailed(task.ID, class); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) onTaskFailed(taskID string, class FailureClass) error {
	if s.plans == nil {
		return nil
	}
	task, ok := s.pm.Task(taskID)
	if !ok || task.PlanID == "" {
		return nil
	}
	if task.Status != pool.TaskFailed {
		return nil
	}
	if task.Role == plannerTaskRole {
		return s.onPlannerTaskFailed(*task)
	}
	if isPlanReviewTask(*task) {
		return s.onPlanReviewFailed(*task)
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	if bundle.Plan.Lineage == "" {
		return s.syncPlanExecution(task.PlanID)
	}
	if class == FailureConflict && s.shouldRetryConflict(*task) {
		failedWorkerID := strings.TrimSpace(task.WorkerID)
		if failedWorkerID != "" {
			if err := s.pm.KillWorker(failedWorkerID); err != nil {
				return err
			}
		}
		if err := s.git.DiscardChild(bundle.Plan.Lineage, task.ID); err != nil {
			return err
		}
		if err := s.pm.ReviveFailedTask(task.ID, true); err != nil {
			return err
		}
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryConflictRetried,
			TaskID:  task.ID,
			Summary: "Retrying task from current lineage head after merge conflict.",
		})
		if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
			return err
		}
		revivedTask, ok := s.pm.Task(task.ID)
		if ok {
			if err := s.spawnWorkerForTask(*revivedTask); err != nil {
				return err
			}
		}
		return s.syncPlanExecution(task.PlanID)
	}
	if class == FailureConflict || class == FailureInfrastructure || class == FailureEnvironment || class == FailureCapability || class == FailureAuth || class == FailureTimeout || class == FailureUnknown {
		if err := s.git.DiscardChild(bundle.Plan.Lineage, taskID); err != nil {
			return err
		}
	}
	return s.syncPlanExecution(task.PlanID)
}

func (s *Scheduler) onWorkerIdle(_ string) error {
	return s.schedule()
}

func (s *Scheduler) onWorkerDead(_ string) error {
	return s.reconcile()
}

func (s *Scheduler) reconcile() error {
	if s.hostAPI == nil || s.sessionID == "" {
		return nil
	}
	containers, err := s.hostAPI.ListContainers(context.Background(), s.sessionID)
	if err != nil {
		return err
	}
	pool.Reconcile(s.pm, containers)
	pool.RequeueOrphanedTasks(s.pm)
	s.refreshPendingSpawns()
	return nil
}

func (s *Scheduler) enforceTaskTimeouts() error {
	if s == nil || s.pm == nil {
		return nil
	}
	now := s.currentTime()
	for _, task := range s.pm.Tasks() {
		if task.PlanID == "" || task.Status != pool.TaskDispatched || task.DispatchedAt == nil || task.WorkerID == "" {
			continue
		}
		timeoutMinutes := task.TimeoutMinutes
		if timeoutMinutes <= 0 {
			continue
		}
		if now.Sub(task.DispatchedAt.UTC()) < time.Duration(timeoutMinutes)*time.Minute {
			continue
		}
		if err := s.pm.FailTask(task.WorkerID, task.ID, fmt.Sprintf("task exceeded time budget of %d minutes", timeoutMinutes)); err != nil && !strings.Contains(err.Error(), "expected dispatched") {
			return err
		}
	}
	return nil
}

func (s *Scheduler) refreshPendingSpawns() {
	for taskID, workerID := range s.pendingSpawn {
		task, tok := s.pm.Task(taskID)
		if !tok || task.Status != pool.TaskQueued {
			delete(s.pendingSpawn, taskID)
			continue
		}
		worker, wok := s.pm.Worker(workerID)
		if !wok || worker.Status == pool.WorkerDead {
			delete(s.pendingSpawn, taskID)
		}
	}
}

func (s *Scheduler) spawnWorkerForTask(task pool.Task) error {
	spec, err := s.workerSpecForTask(task)
	if err != nil {
		return err
	}
	worker, err := s.pm.SpawnWorker(spec)
	if err != nil {
		return err
	}
	s.pendingSpawn[task.ID] = worker.ID
	return nil
}

func (s *Scheduler) workerSpecForTask(task pool.Task) (pool.WorkerSpec, error) {
	spec := pool.WorkerSpec{
		Role:          task.Role,
		WorkspacePath: "",
		Environment: map[string]string{
			"MITTENS_TASK_ID": task.ID,
		},
	}
	if task.PlanID != "" {
		spec.Environment["MITTENS_PLAN_ID"] = task.PlanID
	}
	if kitchenAddr := strings.TrimSpace(s.kitchenAddr); kitchenAddr != "" {
		spec.Environment["MITTENS_KITCHEN_ADDR"] = kitchenAddr
	} else if kitchenAddr := os.Getenv("MITTENS_KITCHEN_ADDR"); kitchenAddr != "" {
		spec.Environment["MITTENS_KITCHEN_ADDR"] = kitchenAddr
	}

	keys := []PoolKey(nil)
	if s.router != nil {
		keys = s.router.Resolve(Complexity(task.Complexity))
	}
	if len(keys) > 0 {
		spec.Provider = keys[0].Provider
		spec.Model = keys[0].Model
		spec.Adapter = keys[0].Adapter
	}
	if spec.Adapter == "" {
		spec.Adapter = adapter.DefaultAdapterForProvider(spec.Provider)
	}
	if isPlanControlTask(task) {
		return spec, nil
	}

	if s.git == nil || s.plans == nil || task.PlanID == "" {
		return spec, nil
	}

	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return spec, err
	}
	if bundle.Plan.Lineage == "" {
		return spec, nil
	}

	anchor := bundle.Plan.Anchor.Commit
	if anchor == "" {
		anchor = "HEAD"
	}
	if err := s.git.CreateLineageBranch(bundle.Plan.Lineage, anchor); err != nil {
		return spec, err
	}
	if s.lineages != nil {
		if err := s.lineages.ActivatePlan(bundle.Plan.Lineage, task.PlanID); err != nil {
			return spec, err
		}
	}
	worktreePath, err := s.git.CreateChildWorktree(bundle.Plan.Lineage, task.ID)
	if err != nil {
		return spec, err
	}
	spec.WorkspacePath = worktreePath
	return spec, nil
}

func idleWorkerIDs(workers []pool.Worker, pending map[string]string) []string {
	reserved := make(map[string]bool, len(pending))
	for _, workerID := range pending {
		reserved[workerID] = true
	}
	var ids []string
	for _, worker := range workers {
		if worker.Status == pool.WorkerIdle && !reserved[worker.ID] {
			ids = append(ids, worker.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *Scheduler) dispatchReadyTaskToWorker(workerID string) error {
	worker, ok := s.pm.Worker(workerID)
	if !ok || worker.Status != pool.WorkerIdle {
		return nil
	}

	queued := s.pm.QueuedTasks()
	sort.Slice(queued, func(i, j int) bool {
		return queued[i].Priority < queued[j].Priority
	})

	for _, task := range queued {
		if _, reserved := s.pendingSpawn[task.ID]; reserved {
			continue
		}
		allowed, err := s.lineageHasCapacity(task)
		if err != nil {
			return err
		}
		if !allowed || !s.workerCanRunTask(*worker, task) || !taskReadyForDispatch(s.pm, task) {
			continue
		}
		return s.pm.DispatchTask(task.ID, workerID)
	}
	return nil
}

func (s *Scheduler) lineageHasCapacity(task pool.Task) (bool, error) {
	if s == nil || s.plans == nil {
		return true, nil
	}
	if s.cfg.MaxWorkersPerLineage <= 0 || task.PlanID == "" {
		return true, nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return false, err
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	if lineage == "" {
		return true, nil
	}
	count, err := s.lineageWorkerCount(lineage)
	if err != nil {
		return false, err
	}
	return count < s.cfg.MaxWorkersPerLineage, nil
}

func (s *Scheduler) lineageWorkerCount(lineage string) (int, error) {
	if s == nil || s.pm == nil || s.plans == nil || strings.TrimSpace(lineage) == "" {
		return 0, nil
	}
	count := 0
	for _, worker := range s.pm.Workers() {
		if worker.Status == pool.WorkerDead || strings.TrimSpace(worker.CurrentTaskID) == "" {
			continue
		}
		task, ok := s.pm.Task(worker.CurrentTaskID)
		if !ok || task.PlanID == "" {
			continue
		}
		bundle, err := s.plans.Get(task.PlanID)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(bundle.Plan.Lineage) == lineage {
			count++
		}
	}
	for taskID := range s.pendingSpawn {
		task, ok := s.pm.Task(taskID)
		if !ok || task.PlanID == "" {
			continue
		}
		bundle, err := s.plans.Get(task.PlanID)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(bundle.Plan.Lineage) == lineage {
			count++
		}
	}
	return count, nil
}

func (s *Scheduler) workerCanRunTask(worker pool.Worker, task pool.Task) bool {
	workerRole := strings.TrimSpace(worker.Role)
	taskRole := strings.TrimSpace(task.Role)

	if workerRole == "" || workerRole == "general" || taskRole == "" {
	} else if workerRole != taskRole {
		return false
	}
	keys := []PoolKey(nil)
	if s != nil && s.router != nil {
		keys = s.router.Resolve(Complexity(task.Complexity))
	}
	if len(keys) == 0 || strings.TrimSpace(worker.Provider) == "" {
		return true
	}
	for _, key := range keys {
		if sameProvider(worker.Provider, key.Provider) {
			return true
		}
	}
	return false
}

func taskReadyForDispatch(pm *pool.PoolManager, task pool.Task) bool {
	if task.RequireFreshWorker {
		return false
	}
	for _, depID := range task.DependsOn {
		dep, ok := pm.Task(depID)
		if !ok {
			return false
		}
		if dep.Status != pool.TaskCompleted && dep.Status != pool.TaskAccepted {
			return false
		}
	}
	return task.Status == pool.TaskQueued
}

func (s *Scheduler) currentTime() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func (s *Scheduler) shouldRetryConflict(task pool.Task) bool {
	if s == nil {
		return false
	}
	rule, ok := s.failurePolicy[string(FailureConflict)]
	if !ok {
		return false
	}
	if strings.TrimSpace(rule.Action) != "retry_merge" {
		return false
	}
	if rule.Max <= 0 {
		return false
	}
	return task.RetryCount < rule.Max
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.stderr != nil {
		fmt.Fprintf(s.stderr, format+"\n", args...)
	}
}

func (s *Scheduler) syncPlanExecution(planID string) error {
	if s == nil || s.plans == nil || s.pm == nil || planID == "" {
		return nil
	}

	bundle, err := s.plans.Get(planID)
	if err != nil {
		return err
	}
	wasCompleted := bundle.Execution.State == planStateCompleted

	active, completed, failed := summarizePlanTasks(s.pm.Tasks(), planID)
	bundle.Execution.ActiveTaskIDs = active
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed

	if len(active) == 0 && len(failed) == 0 {
		now := time.Now().UTC()
		bundle.Plan.State = planStateCompleted
		bundle.Execution.State = planStateCompleted
		bundle.Execution.CompletedAt = &now
	} else {
		bundle.Plan.State = planStateActive
		bundle.Execution.State = planStateActive
		bundle.Execution.CompletedAt = nil
	}

	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	if !wasCompleted && bundle.Execution.State == planStateCompleted && s.notify != nil {
		title := bundle.Plan.Title
		if title == "" {
			title = planID
		}
		s.notify(pool.Notification{Type: "plan_completed", ID: planID, Message: title})
	}
	return nil
}

func (s *Scheduler) onPlannerTaskCompleted(task pool.Task) error {
	if s.plans == nil || task.PlanID == "" {
		return nil
	}
	if task.WorkerID == "" {
		return s.markPlanningFailed(task, "planner task finished without a worker assignment")
	}

	data, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), task.WorkerID, pool.WorkerPlanFile))
	if err != nil {
		return s.markPlanningFailed(task, fmt.Sprintf("read planner artifact: %v", err))
	}
	var artifact adapter.PlanArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return s.markPlanningFailed(task, fmt.Sprintf("decode planner artifact: %v", err))
	}

	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	planned := planFromArtifact(bundle.Plan, &artifact)
	if err := validatePlanRecord(planned, s.lineages); err != nil {
		return s.markPlanningFailed(task, fmt.Sprintf("validate planner artifact: %v", err))
	}

	bundle.Plan = planned
	bundle.Plan.State = planStatePendingApproval
	bundle.Execution.State = planStatePendingApproval
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = appendUniqueIDs(bundle.Execution.CompletedTaskIDs, task.ID)
	bundle.Execution.FailedTaskIDs = nil
	bundle.Affinity.PlannerWorkerID = task.WorkerID
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryPlanningCompleted,
		Cycle:   plannerCycleForTask(task.PlanID, task.ID),
		TaskID:  task.ID,
		Summary: strings.TrimSpace(bundle.Plan.Title),
	})

	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if err := s.plans.UpdateAffinity(task.PlanID, bundle.Affinity); err != nil {
		return err
	}
	if err := s.seedPlannerQuestions(task, artifact.Questions); err != nil {
		return err
	}
	if bundle.Execution.ReviewRequested {
		return s.enqueuePlanReview(bundle)
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_ready", ID: task.PlanID, Message: bundle.Plan.Title})
	}
	if bundle.Execution.AutoApproved && s.activatePlan != nil && len(pendingQuestionsForPlan(s.pm, task.PlanID)) == 0 {
		return s.activatePlan(task.PlanID)
	}
	return nil
}

func (s *Scheduler) onPlannerTaskFailed(task pool.Task) error {
	message := "planner task failed"
	if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
		message = task.Result.Error
	}
	return s.markPlanningFailed(task, message)
}

func (s *Scheduler) markPlanningFailed(task pool.Task, message string) error {
	if s.plans == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStatePlanningFailed
	bundle.Execution.State = planStatePlanningFailed
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = nil
	bundle.Execution.FailedTaskIDs = []string{task.ID}
	bundle.Execution.CompletedAt = &now
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryPlanningFailed,
		Cycle:    plannerCycleForTask(task.PlanID, task.ID),
		TaskID:   task.ID,
		Findings: []string{strings.TrimSpace(message)},
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		title := bundle.Plan.Title
		if strings.TrimSpace(title) == "" {
			title = task.PlanID
		}
		s.notify(pool.Notification{Type: "plan_failed", ID: task.PlanID, Message: title})
	}
	return nil
}

func (s *Scheduler) seedPlannerQuestions(task pool.Task, questions []adapter.PlanArtifactQuestion) error {
	if s == nil || s.pm == nil || task.WorkerID == "" || len(questions) == 0 {
		return nil
	}
	for _, question := range questions {
		text := strings.TrimSpace(question.Question)
		if text == "" {
			continue
		}
		if _, err := s.pm.AskQuestion(task.WorkerID, pool.Question{
			TaskID:   task.ID,
			Question: text,
			Category: "planning",
			Context:  strings.TrimSpace(question.Context),
			Blocking: false,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) enqueuePlanReview(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	reviewTaskID := planReviewRuntimeID(bundle.Plan.PlanID, bundle.Execution.ReviewAttempts+1)
	if _, exists := s.pm.Task(reviewTaskID); !exists {
		prompt, err := buildPlanReviewPrompt(bundle.Plan, bundle.Execution.ReviewRounds)
		if err != nil {
			return err
		}
		if _, err := s.pm.EnqueueTask(pool.TaskSpec{
			ID:         reviewTaskID,
			PlanID:     bundle.Plan.PlanID,
			Prompt:     prompt,
			Complexity: string(reviewComplexityForPlan(bundle.Plan)),
			Priority:   len(bundle.Plan.Tasks) + 1,
			Role:       "reviewer",
		}); err != nil {
			return err
		}
	}
	bundle.Plan.State = planStateReviewing
	bundle.Execution.State = planStateReviewing
	bundle.Execution.ActiveTaskIDs = []string{reviewTaskID}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewRequested,
		Cycle:   reviewCycleForTask(bundle.Plan.PlanID, reviewTaskID),
		TaskID:  reviewTaskID,
		Summary: "Plan queued for review.",
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_review_requested", ID: bundle.Plan.PlanID, Message: bundle.Plan.Title})
	}
	return nil
}

func (s *Scheduler) onPlanReviewCompleted(task pool.Task) error {
	if s.plans == nil || s.pm == nil || task.PlanID == "" || task.WorkerID == "" {
		return nil
	}
	raw, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), task.WorkerID, pool.WorkerResultFile))
	if err != nil {
		return s.onPlanReviewFailed(pool.Task{
			ID:       task.ID,
			PlanID:   task.PlanID,
			Result:   &pool.TaskResult{Error: fmt.Sprintf("read review output: %v", err)},
			WorkerID: task.WorkerID,
		})
	}
	verdict, feedback, severity := adapter.ExtractReviewVerdict(string(raw))
	if verdict == "" {
		verdict = pool.ReviewFail
		feedback = "review verdict not found in output"
		if severity == "" {
			severity = pool.SeverityMajor
		}
	}

	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStatePendingApproval
	bundle.Execution.State = planStatePendingApproval
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = appendUniqueIDs(bundle.Execution.CompletedTaskIDs, task.ID)
	bundle.Execution.FailedTaskIDs = nil
	bundle.Execution.ReviewedAt = &now
	bundle.Execution.ReviewAttempts++
	if verdict == pool.ReviewPass {
		bundle.Execution.ReviewStatus = planReviewStatusPassed
	} else {
		bundle.Execution.ReviewStatus = planReviewStatusFailed
	}
	bundle.Execution.ReviewFindings = planReviewFindings(verdict, feedback, severity)
	historyType := planHistoryReviewPassed
	if verdict != pool.ReviewPass {
		historyType = planHistoryReviewFailed
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     historyType,
		Cycle:    reviewCycleForTask(task.PlanID, task.ID),
		TaskID:   task.ID,
		Verdict:  verdict,
		Findings: bundle.Execution.ReviewFindings,
	})
	if verdict != pool.ReviewPass && bundle.Execution.ReviewRevisions < bundle.Execution.MaxReviewRevisions {
		return s.enqueuePlanRevision(bundle)
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		nType := "plan_review_passed"
		if bundle.Execution.ReviewStatus != planReviewStatusPassed {
			nType = "plan_review_failed"
		}
		s.notify(pool.Notification{Type: nType, ID: task.PlanID, Message: bundle.Plan.Title})
	}
	if bundle.Execution.AutoApproved && bundle.Execution.ReviewStatus == planReviewStatusPassed && s.activatePlan != nil && len(pendingQuestionsForPlan(s.pm, task.PlanID)) == 0 {
		return s.activatePlan(task.PlanID)
	}
	return nil
}

func (s *Scheduler) onPlanReviewFailed(task pool.Task) error {
	if s.plans == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStatePendingApproval
	bundle.Execution.State = planStatePendingApproval
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.FailedTaskIDs = []string{task.ID}
	bundle.Execution.ReviewedAt = &now
	bundle.Execution.ReviewAttempts++
	bundle.Execution.ReviewStatus = planReviewStatusFailed
	msg := "plan review task failed"
	if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
		msg = strings.TrimSpace(task.Result.Error)
	}
	bundle.Execution.ReviewFindings = []string{msg}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryReviewFailed,
		Cycle:    reviewCycleForTask(task.PlanID, task.ID),
		TaskID:   task.ID,
		Verdict:  pool.ReviewFail,
		Findings: bundle.Execution.ReviewFindings,
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
	}
	return nil
}

func isPlanReviewTask(task pool.Task) bool {
	return task.PlanID != "" && strings.HasPrefix(task.ID, planTaskRuntimeID(task.PlanID, planReviewTaskID+"-"))
}

func isPlanControlTask(task pool.Task) bool {
	return task.Role == plannerTaskRole || isPlanReviewTask(task)
}

func reviewComplexityForPlan(plan PlanRecord) Complexity {
	maxLevel := ComplexityLow
	for _, task := range plan.Tasks {
		level := task.ReviewComplexity
		if level == "" {
			level = task.Complexity
		}
		switch level {
		case ComplexityHigh:
			return ComplexityHigh
		case ComplexityMedium:
			maxLevel = ComplexityMedium
		}
	}
	return maxLevel
}

func (s *Scheduler) enqueuePlanRevision(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	revision := bundle.Execution.ReviewRevisions + 1
	revisionTaskID := planRevisionRuntimeID(bundle.Plan.PlanID, revision)
	if _, exists := s.pm.Task(revisionTaskID); !exists {
		prompt, err := buildPlanRevisionPrompt(bundle.Plan, bundle.Execution.ReviewFindings)
		if err != nil {
			return err
		}
		if _, err := s.pm.EnqueueTask(pool.TaskSpec{
			ID:         revisionTaskID,
			PlanID:     bundle.Plan.PlanID,
			Prompt:     prompt,
			Complexity: string(ComplexityMedium),
			Priority:   1,
			Role:       plannerTaskRole,
		}); err != nil {
			return err
		}
	}
	bundle.Plan.State = planStatePlanning
	bundle.Execution.State = planStatePlanning
	bundle.Execution.ActiveTaskIDs = []string{revisionTaskID}
	bundle.Execution.FailedTaskIDs = nil
	bundle.Execution.ReviewRevisions = revision
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryPlanningStarted,
		Cycle:    plannerCycleForTask(bundle.Plan.PlanID, revisionTaskID),
		TaskID:   revisionTaskID,
		Findings: append([]string(nil), bundle.Execution.ReviewFindings...),
		Summary:  "Planner revision task queued.",
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_revising", ID: bundle.Plan.PlanID, Message: bundle.Plan.Title})
	}
	return nil
}

func summarizePlanTasks(tasks []pool.Task, planID string) (active []string, completed []string, failed []string) {
	for _, task := range tasks {
		if task.PlanID != planID {
			continue
		}
		switch task.Status {
		case pool.TaskCompleted, pool.TaskAccepted:
			completed = append(completed, task.ID)
		case pool.TaskFailed, pool.TaskRejected, pool.TaskEscalated:
			failed = append(failed, task.ID)
		case pool.TaskCanceled:
			// canceled tasks are terminal but not successful; do not count them as completed.
		default:
			active = append(active, task.ID)
		}
	}
	sort.Strings(active)
	sort.Strings(completed)
	sort.Strings(failed)
	return active, completed, failed
}
