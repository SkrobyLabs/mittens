package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const (
	authActionRetrySameProvider              = "retry_same_provider"
	authActionRecycleWorkerRetrySameProvider = "recycle_worker_retry_same_provider"
	authActionTryNextProvider                = "try_next_provider"
	authActionFail                           = "fail"
)

type Scheduler struct {
	pm                               *pool.PoolManager
	hostAPI                          pool.RuntimeAPI
	router                           *ComplexityRouter
	git                              *GitManager
	plans                            *PlanStore
	lineages                         *LineageManager
	cfg                              ConcurrencyConfig
	failurePolicy                    map[string]FailurePolicyRule
	sessionID                        string
	kitchenAddr                      string
	notify                           func(pool.Notification)
	activatePlan                     func(string) error
	activateWaitingPlans             func()
	pendingSpawn                     map[string]string
	reconcileInterval                time.Duration
	reapInterval                     time.Duration
	reapTimeout                      time.Duration
	nowFunc                          func() time.Time
	stderr                           io.Writer
	keepDeadWorkers                  bool
	retainedDeadWorkers              []string
	runtimeDiscoveryFailures         int
	runtimeDiscoveryFailureThreshold int
	runtimeDiscoveryOutage           bool
	runtimeDiscoveryAlerted          bool
	deferredTaskFailures             map[string]FailureClass
	routeNotices                     map[string]string
}

func NewScheduler(pm *pool.PoolManager, hostAPI pool.RuntimeAPI, router *ComplexityRouter, git *GitManager, plans *PlanStore, lineages *LineageManager, cfg ConcurrencyConfig, sessionID string) *Scheduler {
	return &Scheduler{
		pm:                               pm,
		hostAPI:                          hostAPI,
		router:                           router,
		git:                              git,
		plans:                            plans,
		lineages:                         lineages,
		cfg:                              cfg,
		failurePolicy:                    DefaultKitchenConfig().FailurePolicy,
		sessionID:                        sessionID,
		pendingSpawn:                     make(map[string]string),
		reconcileInterval:                5 * time.Second,
		reapInterval:                     30 * time.Second,
		reapTimeout:                      90 * time.Second,
		nowFunc:                          time.Now,
		stderr:                           os.Stderr,
		runtimeDiscoveryFailureThreshold: 3,
		deferredTaskFailures:             make(map[string]FailureClass),
		routeNotices:                     make(map[string]string),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	if s == nil || s.pm == nil {
		return
	}

	stopReaper := pool.StartReaperWithReservations(s.pm, s.reapInterval, s.reapTimeout, s.reservedWorkerIDs)
	defer stopReaper()

	if err := s.recoverAutoRemediationIntents(); err != nil {
		s.logf("scheduler auto-remediation recovery: %v", err)
	}
	startupReconcileErr := s.reconcile()
	if startupReconcileErr != nil {
		s.logf("scheduler reconcile: %v", startupReconcileErr)
	}
	if startupReconcileErr == nil {
		if err := s.runRecoverySuite(); err != nil {
			s.logf("scheduler startup recovery: %v", err)
		}
		if err := s.enforceTaskTimeouts(); err != nil {
			s.logf("scheduler timeouts: %v", err)
		}
		if err := s.schedule(); err != nil {
			s.logf("scheduler: %v", err)
		}
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
		if s.runtimeDiscoveryOutage {
			if s.deferredTaskFailures != nil {
				s.deferredTaskFailures[n.ID] = s.taskFailureClass(task)
			}
			return
		}
		if err := s.onTaskFailed(n.ID, s.taskFailureClass(task)); err != nil {
			s.logf("task %s failure handling: %v", n.ID, err)
		}
	case "task_requeued":
		_ = s.onWorkerIdle("")
	case "task_canceled":
		task, ok := s.pm.Task(n.ID)
		if ok && strings.TrimSpace(task.PlanID) != "" {
			var err error
			switch {
			case task.Role == plannerTaskRole:
				err = s.recoverCouncilPlansOnStartup()
			case reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID) > 0:
				err = s.recoverReviewCouncilPlansOnStartup()
			default:
				err = s.syncPlanExecution(task.PlanID)
			}
			if err != nil {
				s.logf("task %s cancel handling: %v", n.ID, err)
			}
		}
	}
}

func (s *Scheduler) taskFailurePayload(task *pool.Task) (string, json.RawMessage) {
	if task == nil || task.Result == nil {
		return "", nil
	}
	return strings.TrimSpace(task.Result.Error), append(json.RawMessage(nil), task.Result.Detail...)
}

func (s *Scheduler) taskFailureClass(task *pool.Task) FailureClass {
	if task == nil {
		return FailureUnknown
	}
	if task.Result != nil && strings.TrimSpace(task.Result.FailureClass) != "" {
		return FailureClass(strings.TrimSpace(task.Result.FailureClass))
	}
	reported, detail := s.taskFailurePayload(task)
	return ClassifyFailure(reported, detail, KitchenSignals{})
}

func (s *Scheduler) taskFailureDetail(task *pool.Task) pool.FailureDetail {
	if task == nil || task.Result == nil || len(task.Result.Detail) == 0 {
		return pool.FailureDetail{}
	}
	var detail pool.FailureDetail
	if json.Unmarshal(task.Result.Detail, &detail) != nil {
		return pool.FailureDetail{}
	}
	return detail
}

func (s *Scheduler) taskFailureSummary(task pool.Task) string {
	if detail := s.taskFailureDetail(&task); strings.TrimSpace(detail.Summary) != "" {
		return strings.TrimSpace(detail.Summary)
	}
	if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
		return strings.TrimSpace(task.Result.Error)
	}
	if s != nil && s.pm != nil {
		workerID := strings.TrimSpace(task.WorkerID)
		if workerID == "" && task.Result != nil {
			workerID = strings.TrimSpace(task.Result.WorkerID)
		}
		if workerID != "" {
			if data, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), workerID, pool.WorkerErrorFile)); err == nil {
				if msg := strings.TrimSpace(string(data)); msg != "" {
					return msg
				}
			}
			if s.hostAPI != nil {
				if transcript, err := s.hostAPI.GetWorkerTranscript(context.Background(), workerID); err == nil {
					for i := len(transcript) - 1; i >= 0; i-- {
						if strings.TrimSpace(transcript[i].TaskID) != task.ID {
							continue
						}
						if msg := strings.TrimSpace(transcript[i].Activity.Summary); msg != "" {
							return msg
						}
					}
				}
			}
		}
	}
	return "task failed"
}

func (s *Scheduler) authFailureRule() FailurePolicyRule {
	if s == nil {
		return FailurePolicyRule{Action: authActionFail}
	}
	rule, ok := s.failurePolicy[string(FailureAuth)]
	if !ok || strings.TrimSpace(rule.Action) == "" {
		return FailurePolicyRule{Action: authActionFail}
	}
	return rule
}

func (s *Scheduler) failedTaskRetryRoute(task pool.Task) *pool.RetryRouteHint {
	if task.RetryRoute != nil {
		return clonePoolRetryRouteHint(task.RetryRoute)
	}
	workerID := strings.TrimSpace(task.WorkerID)
	if workerID == "" && task.Result != nil {
		workerID = strings.TrimSpace(task.Result.WorkerID)
	}
	if workerID == "" || s == nil || s.pm == nil {
		return nil
	}
	worker, ok := s.pm.Worker(workerID)
	if !ok {
		return nil
	}
	if strings.TrimSpace(worker.Provider) == "" {
		return nil
	}
	return &pool.RetryRouteHint{
		Provider: worker.Provider,
		Model:    worker.Model,
		Adapter:  worker.Adapter,
	}
}

func clonePoolRetryRouteHint(route *pool.RetryRouteHint) *pool.RetryRouteHint {
	if route == nil {
		return nil
	}
	cp := *route
	return &cp
}

func retryRoutePoolKey(task pool.Task) (PoolKey, bool) {
	if task.RetryRoute == nil || strings.TrimSpace(task.RetryRoute.Provider) == "" {
		return PoolKey{}, false
	}
	return PoolKey{
		Provider: task.RetryRoute.Provider,
		Model:    task.RetryRoute.Model,
		Adapter:  task.RetryRoute.Adapter,
	}, true
}

func (s *Scheduler) authRetryAllowed(task pool.Task, rule FailurePolicyRule) bool {
	if rule.Max <= 0 {
		return false
	}
	return task.RetryCount < rule.Max
}

func (s *Scheduler) applyAuthRouteCooldown(route *pool.RetryRouteHint, cooldown string) error {
	if route == nil || s == nil || s.router == nil || s.router.health == nil {
		return nil
	}
	cooldown = strings.TrimSpace(cooldown)
	if cooldown == "" {
		cooldown = "60s"
	}
	dur, err := time.ParseDuration(cooldown)
	if err != nil {
		return err
	}
	if dur <= 0 {
		return nil
	}
	return s.router.health.SetCooldown(route.Provider+"/"+route.Model, s.currentTime().Add(dur))
}

func (s *Scheduler) recordAuthRetryHistory(planID, taskID, summary string) error {
	if s == nil || s.plans == nil || strings.TrimSpace(planID) == "" {
		return nil
	}
	bundle, err := s.plans.Get(planID)
	if err != nil {
		return err
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryManualRetried,
		TaskID:  taskID,
		Summary: strings.TrimSpace(summary),
	})
	return s.plans.UpdateExecution(planID, bundle.Execution)
}

func (s *Scheduler) retryAuthFailedTask(task *pool.Task, bundle StoredPlan) (bool, error) {
	if task == nil || strings.TrimSpace(task.PlanID) == "" {
		return false, nil
	}
	rule := s.authFailureRule()
	action := strings.TrimSpace(rule.Action)
	summary := s.taskFailureSummary(*task)
	switch action {
	case authActionRetrySameProvider, authActionRecycleWorkerRetrySameProvider:
		if !s.authRetryAllowed(*task, rule) {
			return false, nil
		}
		retryRoute := s.failedTaskRetryRoute(*task)
		requireFreshWorker := action == authActionRecycleWorkerRetrySameProvider
		if bundle.Plan.Lineage != "" && !isLineageMergeTask(*task) {
			if err := s.git.DiscardChild(bundle.Plan.Lineage, task.ID); err != nil {
				return true, err
			}
			s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
			requireFreshWorker = true
		} else if requireFreshWorker {
			if err := s.pm.KillWorker(task.WorkerID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
				return true, err
			}
		}
		if err := s.pm.ReviveFailedTaskWithRoute(task.ID, requireFreshWorker, retryRoute); err != nil {
			return true, err
		}
		if err := s.recordAuthRetryHistory(task.PlanID, task.ID, "Retrying task on the same provider after auth failure: "+summary); err != nil {
			return true, err
		}
		if revivedTask, ok := s.pm.Task(task.ID); ok && requireFreshWorker {
			if _, err := s.spawnWorkerForTask(*revivedTask); err != nil {
				return true, err
			}
		}
		return true, s.syncPlanExecution(task.PlanID)
	case authActionTryNextProvider:
		if err := s.applyAuthRouteCooldown(s.failedTaskRetryRoute(*task), rule.Cooldown); err != nil {
			return true, err
		}
		if bundle.Plan.Lineage != "" && !isLineageMergeTask(*task) {
			if err := s.git.DiscardChild(bundle.Plan.Lineage, task.ID); err != nil {
				return true, err
			}
			s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
		} else if err := s.pm.KillWorker(task.WorkerID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return true, err
		}
		if err := s.pm.ReviveFailedTaskWithRoute(task.ID, true, nil); err != nil {
			return true, err
		}
		if err := s.recordAuthRetryHistory(task.PlanID, task.ID, "Retrying task on the next provider after auth failure: "+summary); err != nil {
			return true, err
		}
		if revivedTask, ok := s.pm.Task(task.ID); ok {
			if _, err := s.spawnWorkerForTask(*revivedTask); err != nil {
				return true, err
			}
		}
		return true, s.syncPlanExecution(task.PlanID)
	default:
		return false, nil
	}
}

func (s *Scheduler) schedule() error {
	if s == nil || s.pm == nil {
		return nil
	}
	if err := s.recoverAutoRemediationIntents(); err != nil {
		return err
	}

	s.reapOrphanPlanTasks()
	s.refreshPendingSpawns()
	s.pruneRouteNotices()

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
	if s.runtimeDiscoveryOutage {
		return nil
	}

	availableCapacity := s.cfg.MaxWorkersTotal - s.pm.HealthyWorkers(s.reapTimeout)
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
		if spawned, err := s.spawnWorkerForTask(task); err != nil {
			return err
		} else if !spawned {
			continue
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
	if task.Role == researcherTaskRole {
		return s.onResearchTaskCompleted(*task)
	}
	if task.Role == plannerTaskRole {
		return s.onCouncilTurnCompleted(*task)
	}
	if task.Role == lineageFixMergeRole {
		return s.onLineageFixMergeCompleted(*task)
	}
	if isLineageMergeTask(*task) {
		return s.onLineageMergeCompleted(*task)
	}
	if isReviewCouncilTask(*task) {
		return s.onReviewCouncilTurnCompleted(*task)
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	if bundle.Plan.Lineage == "" {
		return s.syncPlanExecution(task.PlanID)
	}
	// Workers run inside a child git worktree but aren't instructed to
	// commit their own work. Auto-commit anything left dirty before
	// merging back into the lineage branch, otherwise DiscardChild would
	// erase the edits.
	commitMsg := "Kitchen task " + taskID
	if summary := strings.TrimSpace(strings.SplitN(task.Prompt, "\n", 2)[0]); summary != "" {
		if len(summary) > 72 {
			summary = summary[:72]
		}
		commitMsg = fmt.Sprintf("Kitchen task %s: %s", taskID, summary)
	}
	if _, err := s.git.CommitChildIfDirty(bundle.Plan.Lineage, taskID, commitMsg); err != nil {
		return s.onTaskMergeFailed(*task, bundle.Plan.Lineage, err)
	}
	if err := s.git.MergeChild(bundle.Plan.Lineage, taskID); err != nil {
		return s.onTaskMergeFailed(*task, bundle.Plan.Lineage, err)
	}
	if err := s.syncPlanExecution(task.PlanID); err != nil {
		return err
	}
	if err := s.git.DiscardChild(bundle.Plan.Lineage, taskID); err != nil {
		s.logf("discard child worktree %s/%s after completion: %v", bundle.Plan.Lineage, taskID, err)
	}
	// Kitchen workers are single-use: the container's bind mount is
	// pinned to this task's child worktree with this task's child
	// branch checked out, and DiscardChild has now wiped the path.
	// Kill the worker so the next task spawns a fresh container with
	// a fresh worktree forked from the updated lineage head instead
	// of inheriting a stale cwd and the previous child branch.
	s.killWorkerForDiscardedWorktree(task.WorkerID, taskID)
	return nil
}

func (s *Scheduler) onResearchTaskCompleted(task pool.Task) error {
	if s.plans == nil {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}

	var researchOutput string
	if task.Result != nil && strings.TrimSpace(task.Result.Summary) != "" {
		researchOutput = task.Result.Summary
	}

	now := time.Now().UTC()
	bundle.Execution.ResearchOutput = researchOutput
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = []string{task.ID}
	bundle.Execution.State = planStateResearchComplete
	bundle.Execution.CompletedAt = &now
	bundle.Plan.State = planStateResearchComplete
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "research_complete", ID: task.PlanID})
	}
	s.killWorkerIDs(task.WorkerID)
	return nil
}

func (s *Scheduler) onResearchTaskFailed(task pool.Task) error {
	if s.plans == nil {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.FailedTaskIDs = []string{task.ID}
	bundle.Execution.State = planStatePlanningFailed
	bundle.Plan.State = planStatePlanningFailed
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "research_failed", ID: task.PlanID})
	}
	return nil
}

func (s *Scheduler) onLineageFixMergeCompleted(task pool.Task) error {
	if s.plans == nil || s.git == nil {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	baseBranch := strings.TrimSpace(bundle.Plan.Anchor.Branch)
	if lineage == "" || baseBranch == "" {
		s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
		return s.syncPlanExecution(task.PlanID)
	}
	fixBranch := "kitchen/" + lineage + "/fix-merge/" + task.ID
	worktreePath := filepath.Join(s.git.worktreeBase, lineage, "fix-merge-"+task.ID)
	// The worker was expected to commit its resolution. If it left the
	// worktree dirty (unfinished resolution), auto-commit so the fast-
	// forward step below still captures their work rather than losing
	// it to the cleanup.
	commitMsg := fmt.Sprintf("Resolve %s→%s merge conflicts (auto-commit)", baseBranch, lineage)
	if _, err := s.git.commitWorktreeIfDirty(worktreePath, commitMsg); err != nil {
		s.logf("fix-lineage-merge %s auto-commit: %v", task.ID, err)
	}
	cleanupErr, err := s.git.FinalizeFixLineageMerge(lineage, fixBranch, worktreePath)
	if err != nil {
		return err
	}
	if cleanupErr != nil {
		s.logf("fix-lineage-merge %s cleanup (non-fatal): %v", task.ID, cleanupErr)
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageFixMergeCompleted,
		TaskID:  task.ID,
		Summary: fmt.Sprintf("Resolved %s→%s conflicts on lineage; base untouched, ready for fast-forward merge", baseBranch, lineage),
	})
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
	return s.syncPlanExecution(task.PlanID)
}

func (s *Scheduler) onLineageMergeCompleted(task pool.Task) error {
	if s == nil || s.plans == nil {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	if lineage == "" {
		return s.restoreCompletedPlanAfterLineageMergeAttempt(task.PlanID, task.ID, "Merge generation finished without a lineage to merge.")
	}
	output, err := s.pm.ReadTaskOutput(task.ID)
	if err != nil {
		return s.promptLineageMergeFallback(task, bundle, fmt.Sprintf("reading generated commit message failed: %v", err))
	}
	commitMsg, err := extractCommitMessageFromTaskOutput(output)
	if err != nil {
		return s.promptLineageMergeFallback(task, bundle, fmt.Sprintf("generated merge commit message was invalid: %v", err))
	}
	if err := s.completeLineageMergeWithMessage(task.PlanID, task.ID, commitMsg, "generated"); err != nil {
		return s.restoreCompletedPlanAfterLineageMergeAttempt(task.PlanID, task.ID, fmt.Sprintf("Lineage merge failed: %v", err))
	}
	return nil
}

func (s *Scheduler) onLineageMergeFailed(task pool.Task, class FailureClass) error {
	if s == nil || s.plans == nil {
		return nil
	}
	if class == FailureAuth {
		bundle, err := s.plans.Get(task.PlanID)
		if err != nil {
			return err
		}
		if handled, err := s.retryAuthFailedTask(&task, bundle); handled || err != nil {
			return err
		}
	}
	message := "lineage merge task failed"
	if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
		message = strings.TrimSpace(task.Result.Error)
	}
	return s.restoreCompletedPlanAfterLineageMergeAttempt(task.PlanID, task.ID, "Lineage merge generation failed: "+message)
}

func (s *Scheduler) promptLineageMergeFallback(task pool.Task, bundle StoredPlan, reason string) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	if strings.TrimSpace(task.WorkerID) != "" {
		if _, err := s.pm.AskQuestion(task.WorkerID, pool.Question{
			TaskID:   task.ID,
			Question: "Generated merge commit message failed validation. Continue with fallback commit message or do nothing?",
			Category: "lineage_merge",
			Options:  []string{"Do nothing", "Continue with fallback commit message"},
			Blocking: true,
			Context:  strings.TrimSpace(reason),
		}); err != nil {
			return err
		}
	}
	bundle.Plan.State = planStateCompleted
	bundle.Execution.State = planStateCompleted
	bundle.Execution.ActiveTaskIDs = nil
	now := time.Now().UTC()
	bundle.Execution.CompletedAt = &now
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeFallbackPrompt,
		TaskID:  task.ID,
		Summary: strings.TrimSpace(reason),
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	return s.plans.UpdateExecution(task.PlanID, bundle.Execution)
}

func (s *Scheduler) completeLineageMergeWithMessage(planID, taskID, commitMsg, source string) error {
	if s == nil || s.git == nil || s.plans == nil {
		return fmt.Errorf("scheduler merge dependencies not configured")
	}
	bundle, err := s.plans.Get(planID)
	if err != nil {
		return err
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	if lineage == "" {
		return fmt.Errorf("plan %s has no lineage", planID)
	}
	baseBranch := firstNonEmpty(strings.TrimSpace(bundle.Plan.Anchor.Branch), "main")
	if err := s.git.MergeLineage(lineage, baseBranch, "squash", commitMsg); err != nil {
		return err
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeCompleted,
		TaskID:  taskID,
		Summary: fmt.Sprintf("Merged %s into %s using %s commit message.", lineage, baseBranch, firstNonEmpty(strings.TrimSpace(source), "generated")),
	})
	if err := s.plans.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	if err := s.markPlanMerged(planID); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_merged", ID: planID, Message: lineage})
	}
	if s.activateWaitingPlans != nil {
		s.activateWaitingPlans()
	}
	return nil
}

func (s *Scheduler) restoreCompletedPlanAfterLineageMergeAttempt(planID, taskID, summary string) error {
	if s == nil || s.plans == nil {
		return fmt.Errorf("scheduler plans not configured")
	}
	bundle, err := s.plans.Get(planID)
	if err != nil {
		return err
	}
	active := append([]string(nil), bundle.Execution.ActiveTaskIDs...)
	completed := append([]string(nil), bundle.Execution.CompletedTaskIDs...)
	failed := append([]string(nil), bundle.Execution.FailedTaskIDs...)
	if s.pm != nil {
		active, completed, failed = summarizeRelevantPlanTasks(s.pm.Tasks(), bundle)
	}
	active = removeTrimmedID(active, taskID)
	completed = removeTrimmedID(completed, taskID)
	failed = appendUniqueIDs(failed, taskID)
	bundle.Execution.ActiveTaskIDs = active
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	if len(active) > 0 {
		bundle.Plan.State = planStateActive
		bundle.Execution.State = planStateActive
		bundle.Execution.CompletedAt = nil
	} else {
		now := time.Now().UTC()
		bundle.Plan.State = planStateCompleted
		bundle.Execution.State = planStateCompleted
		bundle.Execution.CompletedAt = &now
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeFailed,
		TaskID:  taskID,
		Summary: strings.TrimSpace(summary),
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	return s.plans.UpdateExecution(planID, bundle.Execution)
}

func (s *Scheduler) markPlanMerged(planID string) error {
	if s == nil || s.plans == nil {
		return fmt.Errorf("scheduler plans not configured")
	}
	bundle, err := s.plans.Get(planID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var completed []string
	var failed []string
	if s.pm != nil {
		for _, task := range s.pm.Tasks() {
			if task.PlanID != planID {
				continue
			}
			switch task.Status {
			case pool.TaskCompleted:
				completed = append(completed, task.ID)
			default:
				failed = append(failed, task.ID)
			}
		}
		sort.Strings(completed)
		sort.Strings(failed)
	}
	bundle.Plan.State = planStateMerged
	bundle.Execution.State = planStateMerged
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Execution.CompletedAt = &now
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	if s.lineages != nil {
		return s.lineages.ClearActivePlan(bundle.Plan.Lineage, planID)
	}
	return nil
}

func (s *Scheduler) onTaskMergeFailed(task pool.Task, lineage string, mergeErr error) error {
	class := FailureInfrastructure
	if strings.Contains(strings.ToLower(mergeErr.Error()), "merge conflict") {
		class = FailureConflict
	}
	if err := s.pm.FailCompletedTask(task.ID, mergeErr.Error()); err != nil {
		return err
	}
	if class == FailureConflict {
		s.recordConflictInfo(task, lineage, mergeErr)
	}
	if err := s.git.DiscardChild(lineage, task.ID); err != nil {
		s.logf("task %s merge failure cleanup: %v", task.ID, err)
	}
	s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
	if err := s.syncPlanExecution(task.PlanID); err != nil {
		return err
	}
	if err := s.onTaskFailed(task.ID, class); err != nil {
		return err
	}
	return nil
}

// recordConflictInfo extracts conflict file names from the merge error, computes
// the lineage diff for those files, and persists the data on the task result so
// that fix-conflicts tooling can use it later. Errors are logged and swallowed;
// the conflict info is best-effort enrichment only.
func (s *Scheduler) recordConflictInfo(task pool.Task, lineage string, mergeErr error) {
	// Extract the comma-separated file list from the error string produced by
	// mergeIntoTemp: "merge conflicts: file1, file2, ...".
	const prefix = "merge conflicts: "
	errMsg := mergeErr.Error()
	idx := strings.Index(errMsg, prefix)
	if idx < 0 {
		return
	}
	fileStr := errMsg[idx+len(prefix):]
	var conflictFiles []string
	for _, f := range strings.Split(fileStr, ", ") {
		if f = strings.TrimSpace(f); f != "" {
			conflictFiles = append(conflictFiles, f)
		}
	}
	if len(conflictFiles) == 0 {
		return
	}

	if task.PlanID == "" || s.plans == nil {
		return
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		s.logf("task %s conflict info: get plan: %v", task.ID, err)
		return
	}
	anchorSHA := strings.TrimSpace(bundle.Plan.Anchor.Commit)
	if anchorSHA == "" {
		return
	}

	diff, err := s.git.ConflictDiff(anchorSHA, lineageBranchName(lineage), conflictFiles)
	if err != nil {
		s.logf("task %s conflict diff: %v", task.ID, err)
		// Continue with empty diff — the file list alone is still useful.
	}

	if err := s.pm.SetTaskConflictInfo(task.ID, &pool.ConflictInfo{
		ConflictingFiles: conflictFiles,
		LineageDiff:      diff,
	}); err != nil {
		s.logf("task %s set conflict info: %v", task.ID, err)
	}
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
	if task.Role == researcherTaskRole {
		return s.onResearchTaskFailed(*task)
	}
	if task.Role == plannerTaskRole {
		return s.onCouncilTurnFailed(*task, class)
	}
	if isLineageMergeTask(*task) {
		return s.onLineageMergeFailed(*task, class)
	}
	if isReviewCouncilTask(*task) {
		return s.onReviewCouncilTurnFailed(*task, class)
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	if bundle.Plan.Lineage == "" {
		if class == FailureAuth {
			if handled, err := s.retryAuthFailedTask(task, bundle); handled || err != nil {
				return err
			}
		}
		if bundle.Plan.Source == planSourceQuick {
			maxRetries := quickPlanEffectiveMaxRetries(bundle)
			if task.RetryCount < maxRetries {
				if err := s.pm.ReviveFailedTask(task.ID, true); err != nil {
					return err
				}
				bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
					Type:    planHistoryManualRetried,
					TaskID:  task.ID,
					Summary: fmt.Sprintf("Quick plan auto-retry (attempt %d of %d).", task.RetryCount+1, maxRetries),
				})
				if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
					return err
				}
				if revivedTask, ok := s.pm.Task(task.ID); ok {
					if _, err := s.spawnWorkerForTask(*revivedTask); err != nil {
						return err
					}
				}
				return s.syncPlanExecution(task.PlanID)
			}
		}
		return s.syncPlanExecution(task.PlanID)
	}
	if class == FailureAuth {
		if handled, err := s.retryAuthFailedTask(task, bundle); handled || err != nil {
			return err
		}
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
			if _, err := s.spawnWorkerForTask(*revivedTask); err != nil {
				return err
			}
		}
		return s.syncPlanExecution(task.PlanID)
	}
	// Quick-plan auto-retry: revive instead of entering implementation_failed.
	if bundle.Plan.Source == planSourceQuick && !isLineageMergeTask(*task) {
		maxRetries := quickPlanEffectiveMaxRetries(bundle)
		if task.RetryCount < maxRetries {
			if err := s.git.DiscardChild(bundle.Plan.Lineage, task.ID); err != nil {
				return err
			}
			s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
			if err := s.pm.ReviveFailedTask(task.ID, true); err != nil {
				return err
			}
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:    planHistoryManualRetried,
				TaskID:  task.ID,
				Summary: fmt.Sprintf("Quick plan auto-retry (attempt %d of %d).", task.RetryCount+1, maxRetries),
			})
			if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
				return err
			}
			if revivedTask, ok := s.pm.Task(task.ID); ok {
				if _, err := s.spawnWorkerForTask(*revivedTask); err != nil {
					return err
				}
			}
			return s.syncPlanExecution(task.PlanID)
		}
	}
	if class == FailureConflict || class == FailureInfrastructure || class == FailureEnvironment || class == FailureCapability || class == FailureAuth || class == FailureTimeout || class == FailureUnknown {
		if err := s.git.DiscardChild(bundle.Plan.Lineage, taskID); err != nil {
			return err
		}
		s.killWorkerForDiscardedWorktree(task.WorkerID, taskID)
	}
	return s.syncPlanExecution(task.PlanID)
}

// killWorkerForDiscardedWorktree kills the worker container whose bind
// mount pointed at a now-removed child worktree. Kitchen workers are
// spawned with a single static workspace bind mount, so once that path
// is discarded the container's cwd becomes a dangling inode and any
// subsequent task dispatched to the idle worker would fail with
// "current working directory was deleted". The container must be
// recycled instead.
//
// When keepDeadWorkers is set the container is left running (only the
// pool state transitions to Dead) so an operator can docker exec into
// it for post-mortem. The scheduler tracks retained IDs and evicts the
// oldest before spawning a new worker if the total would exceed
// MaxWorkersTotal.
func (s *Scheduler) killWorkerForDiscardedWorktree(workerID, taskID string) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return
	}
	if s.keepDeadWorkers {
		if err := s.pm.MarkDead(workerID); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "not found") {
				return
			}
			s.logf("mark worker %s dead after discarding worktree for task %s: %v", workerID, taskID, err)
			return
		}
		s.retainedDeadWorkers = append(s.retainedDeadWorkers, workerID)
		return
	}
	if err := s.pm.KillWorker(workerID); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") {
			return
		}
		s.logf("kill worker %s after discarding worktree for task %s: %v", workerID, taskID, err)
	}
}

func (s *Scheduler) killWorkerIDs(workerIDs ...string) {
	if s == nil || s.pm == nil {
		return
	}
	seen := make(map[string]struct{}, len(workerIDs))
	for _, workerID := range workerIDs {
		workerID = strings.TrimSpace(workerID)
		if workerID == "" {
			continue
		}
		if _, ok := seen[workerID]; ok {
			continue
		}
		seen[workerID] = struct{}{}
		if err := s.pm.KillWorker(workerID); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "not found") {
				continue
			}
			s.logf("kill worker %s: %v", workerID, err)
		}
	}
}

// evictRetainedDeadWorkersUntilUnderCap evicts retained dead worker
// containers (oldest first) until the total number of containers
// (alive + retained-dead) is strictly below MaxWorkersTotal, so a
// fresh spawn can fit under the cap. Called from spawnWorkerForTask
// when keepDeadWorkers is enabled.
func (s *Scheduler) evictRetainedDeadWorkersUntilUnderCap() {
	if !s.keepDeadWorkers || s.hostAPI == nil {
		return
	}
	cap := s.cfg.MaxWorkersTotal
	if cap <= 0 {
		return
	}
	for len(s.retainedDeadWorkers) > 0 && s.pm.AliveWorkers()+len(s.retainedDeadWorkers) >= cap {
		victim := s.retainedDeadWorkers[0]
		s.retainedDeadWorkers = s.retainedDeadWorkers[1:]
		if err := s.hostAPI.KillWorker(context.Background(), victim); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "not found") {
				continue
			}
			s.logf("evict retained dead worker %s: %v", victim, err)
		}
	}
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
		s.handleRuntimeDiscoveryFailure(err)
		return err
	}
	wasOutage := s.runtimeDiscoveryOutage
	wasAlerted := s.runtimeDiscoveryAlerted
	s.runtimeDiscoveryFailures = 0
	s.runtimeDiscoveryOutage = false
	s.runtimeDiscoveryAlerted = false
	if wasOutage && wasAlerted && s.notify != nil {
		s.notify(pool.Notification{Type: "scheduler_runtime_discovery_recovered", ID: s.sessionID})
	}
	pool.Reconcile(s.pm, containers, s.reapTimeout)
	pool.RequeueOrphanedTasks(s.pm)
	if wasOutage {
		if err := s.runRecoverySuite(); err != nil {
			return err
		}
	} else {
		if err := s.recoverCouncilPlansOnStartup(); err != nil {
			return err
		}
		if err := s.recoverReviewCouncilPlansOnStartup(); err != nil {
			return err
		}
	}
	s.refreshPendingSpawns()
	return nil
}

func (s *Scheduler) handleRuntimeDiscoveryFailure(err error) {
	if s == nil || s.pm == nil {
		return
	}
	s.runtimeDiscoveryFailures++
	s.runtimeDiscoveryOutage = true
	protected := s.reservedWorkerIDs()
	for _, worker := range s.pm.Workers() {
		if worker.Status == pool.WorkerDead {
			continue
		}
		if _, ok := protected[worker.ID]; ok {
			continue
		}
		s.pm.MarkDeadIfStale(worker.ID, s.reapTimeout)
	}
	pool.RequeueOrphanedTasks(s.pm)
	if councilErr := s.recoverCouncilPlansOnStartup(); councilErr != nil {
		s.logf("scheduler council recovery during runtime discovery failure: %v", councilErr)
	}
	if reviewErr := s.recoverReviewCouncilPlansOnStartup(); reviewErr != nil {
		s.logf("scheduler review council recovery during runtime discovery failure: %v", reviewErr)
	}
	s.refreshPendingSpawns()
	if s.runtimeDiscoveryAlerted || s.runtimeDiscoveryFailures < s.runtimeDiscoveryFailureThreshold {
		return
	}
	s.runtimeDiscoveryAlerted = true
	if s.notify != nil {
		s.notify(pool.Notification{
			Type:    "scheduler_runtime_discovery_unavailable",
			ID:      s.sessionID,
			Message: strings.TrimSpace(err.Error()),
		})
	}
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

func (s *Scheduler) spawnWorkerForTask(task pool.Task) (bool, error) {
	resolution := s.routeResolutionForTask(task)
	if routeResolutionUnavailable(resolution) {
		return s.handleRouteUnavailableTask(task, resolution)
	}
	s.clearRouteNotice(task.ID)
	spec, err := s.workerSpecForTask(task)
	if err != nil {
		return false, err
	}
	s.evictRetainedDeadWorkersUntilUnderCap()
	worker, err := s.pm.SpawnWorker(spec)
	if err != nil {
		return false, err
	}
	s.pendingSpawn[task.ID] = worker.ID
	return true, nil
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

	resolution := s.routeResolutionForTask(task)
	if len(resolution.Keys) > 0 {
		spec.Provider = resolution.Keys[0].Provider
		spec.Model = resolution.Keys[0].Model
		spec.Adapter = resolution.Keys[0].Adapter
	}
	if spec.Adapter == "" {
		spec.Adapter = adapter.DefaultAdapterForProvider(spec.Provider)
	}
	if isPlanControlTask(task) {
		if s.git != nil {
			spec.WorkspacePath = s.git.repoPath
		}
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
	if task.Role == lineageFixMergeRole {
		baseBranch := bundle.Plan.Anchor.Branch
		if strings.TrimSpace(baseBranch) == "" {
			return spec, fmt.Errorf("lineage fix-merge task needs a base branch on the plan anchor")
		}
		worktreePath, _, err := s.git.CreateFixLineageMergeWorktree(bundle.Plan.Lineage, baseBranch, task.ID)
		if err != nil {
			return spec, err
		}
		if worktreePath == "" {
			// Merge turned out to be clean — nothing for the worker to do.
			return spec, fmt.Errorf("lineage fix-merge: %s→%s is already clean", bundle.Plan.Lineage, baseBranch)
		}
		spec.WorkspacePath = worktreePath
		return spec, nil
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
	if !s.pm.WorkerHealthy(workerID, s.reapTimeout) {
		s.pm.MarkDeadIfStale(workerID, s.reapTimeout)
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
		resolution := s.routeResolutionForTask(task)
		if routeResolutionUnavailable(resolution) {
			if _, err := s.handleRouteUnavailableTask(task, resolution); err != nil {
				return err
			}
			continue
		}
		s.clearRouteNotice(task.ID)
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

// reconcilePlanExecutionOnStartup re-syncs plan execution records that
// show evidence of partially-applied task progress. This covers the
// recovery case where completion handling advanced git state but crashed
// before syncPlanExecution ran, leaving the plan marked active without
// the follow-up review or completion transition persisted.
func (s *Scheduler) reconcilePlanExecutionOnStartup() error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, t := range s.pm.Tasks() {
		planID := strings.TrimSpace(t.PlanID)
		if planID == "" || seen[planID] {
			continue
		}
		seen[planID] = true
		bundle, err := s.plans.Get(planID)
		if err != nil {
			continue
		}
		switch bundle.Execution.State {
		case planStateReviewing, planStateImplementationReview, planStateWaitingOnDependency:
			continue
		}
		if err := s.syncPlanExecution(planID); err != nil {
			s.logf("startup sync %s: %v", planID, err)
		}
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		planID := strings.TrimSpace(plan.PlanID)
		if planID == "" || seen[planID] {
			continue
		}
		bundle, err := s.plans.Get(planID)
		if err != nil {
			continue
		}
		switch bundle.Execution.State {
		case planStateReviewing, planStateImplementationReview, planStateWaitingOnDependency:
			continue
		}
		if bundle.Execution.State != planStateActive {
			continue
		}
		if len(bundle.Execution.ActiveTaskIDs) == 0 &&
			len(bundle.Execution.CompletedTaskIDs) == 0 &&
			len(bundle.Execution.FailedTaskIDs) == 0 &&
			!bundle.Execution.ImplReviewRequested &&
			!bundle.Execution.AutoRemediationActive {
			continue
		}
		if err := s.syncPlanExecution(planID); err != nil {
			s.logf("startup sync %s: %v", planID, err)
		}
	}
	return nil
}

func (s *Scheduler) runRecoverySuite() error {
	if s == nil {
		return nil
	}
	if err := s.recoverFailedTasksOnStartup(); err != nil {
		return err
	}
	if err := s.replayDeferredTaskFailures(); err != nil {
		return err
	}
	if err := s.recoverMissingActivePlanTasksOnStartup(); err != nil {
		return err
	}
	// One-shot reconciliation of plan execution state against the
	// pool's task map. Catches plans left with stale activeTaskIDs
	// because a completion handler errored out before syncPlanExecution
	// ran (e.g. worktree cleanup blew up with EACCES after the ref
	// advance had already succeeded).
	if err := s.reconcilePlanExecutionOnStartup(); err != nil {
		return err
	}
	if err := s.recoverOrphanedPlansOnStartup(); err != nil {
		return err
	}
	if err := s.recoverCouncilPlansOnStartup(); err != nil {
		return err
	}
	if err := s.recoverReviewCouncilPlansOnStartup(); err != nil {
		return err
	}
	if err := s.recoverLineageMergePlansOnStartup(); err != nil {
		return err
	}
	s.recoverWaitingPlansOnStartup()
	return nil
}

func (s *Scheduler) recoverLineageMergePlansOnStartup() error {
	if s == nil || s.plans == nil || s.pm == nil {
		return nil
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil || bundle.Execution.State != planStateMerging {
			continue
		}
		for _, task := range s.pm.Tasks() {
			if task.PlanID != plan.PlanID || !isLineageMergeTask(task) {
				continue
			}
			switch task.Status {
			case pool.TaskCompleted:
				if err := s.onTaskCompleted(task.ID); err != nil {
					return err
				}
			case pool.TaskFailed:
				if err := s.onTaskFailed(task.ID, s.taskFailureClass(&task)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Scheduler) replayDeferredTaskFailures() error {
	if s == nil || len(s.deferredTaskFailures) == 0 {
		return nil
	}
	taskIDs := make([]string, 0, len(s.deferredTaskFailures))
	for taskID := range s.deferredTaskFailures {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		class := s.deferredTaskFailures[taskID]
		delete(s.deferredTaskFailures, taskID)
		if err := s.onTaskFailed(taskID, class); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) recoverMissingActivePlanTasksOnStartup() error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil {
			continue
		}
		if bundle.Execution.State != planStateActive || bundle.Execution.AutoRemediationActive {
			continue
		}
		if err := s.ensurePlanTrackedActiveTasks(&bundle, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) ensurePlanTrackedActiveTasks(bundle *StoredPlan, recovered bool) error {
	if s == nil || s.pm == nil || s.plans == nil || bundle == nil {
		return nil
	}
	if bundle.Execution.State != planStateActive || len(bundle.Execution.ActiveTaskIDs) == 0 {
		return nil
	}

	changed := false
	recoveredTaskIDs := make([]string, 0, len(bundle.Execution.ActiveTaskIDs))
	for _, runtimeTaskID := range bundle.Execution.ActiveTaskIDs {
		runtimeTaskID = strings.TrimSpace(runtimeTaskID)
		if runtimeTaskID == "" {
			continue
		}
		task, exists := s.pm.Task(runtimeTaskID)
		if exists {
			if task.Status == pool.TaskCanceled {
				requireFresh, ok := recoveredPlanTaskFreshWorker(*bundle, runtimeTaskID)
				if !ok {
					continue
				}
				if err := s.pm.ReviveCanceledTask(runtimeTaskID, requireFresh); err != nil {
					return err
				}
				changed = true
				recoveredTaskIDs = append(recoveredTaskIDs, runtimeTaskID)
			}
			continue
		}

		spec, ok := recoveredPlanTaskSpec(*bundle, runtimeTaskID)
		if !ok {
			continue
		}
		if _, err := s.pm.EnqueueTask(spec); err != nil {
			return err
		}
		changed = true
		recoveredTaskIDs = append(recoveredTaskIDs, runtimeTaskID)
	}

	if !changed {
		return nil
	}

	latest, err := s.plans.Get(bundle.Plan.PlanID)
	if err != nil {
		return err
	}
	active, completed, failed := summarizeRelevantPlanTasks(s.pm.Tasks(), latest)
	latest.Plan.State = planStateActive
	latest.Execution.State = planStateActive
	latest.Execution.ActiveTaskIDs = active
	latest.Execution.CompletedTaskIDs = completed
	latest.Execution.FailedTaskIDs = failed
	latest.Execution.CompletedAt = nil
	if recovered {
		for _, taskID := range recoveredTaskIDs {
			latest.Execution = appendPlanHistory(latest.Execution, PlanHistoryEntry{
				Type:    planHistoryManualRetried,
				TaskID:  taskID,
				Summary: "Recovered missing active task after scheduler restart.",
			})
		}
	}
	if err := s.plans.UpdatePlan(latest.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(latest.Plan.PlanID, latest.Execution); err != nil {
		return err
	}
	*bundle = latest
	return nil
}

func recoveredPlanTaskSpec(bundle StoredPlan, runtimeTaskID string) (pool.TaskSpec, bool) {
	logicalTaskID, planTask, idx, ok := recoveredPlanTask(bundle.Plan, runtimeTaskID)
	if !ok {
		return pool.TaskSpec{}, false
	}
	deps := make([]string, 0, len(planTask.Dependencies))
	for _, dep := range planTask.Dependencies {
		depID := strings.TrimSpace(dep.Task)
		if depID == "" {
			continue
		}
		deps = append(deps, planTaskRuntimeID(bundle.Plan.PlanID, depID))
	}
	requireFresh, _ := recoveredPlanTaskFreshWorker(bundle, runtimeTaskID)
	return pool.TaskSpec{
		ID:                 runtimeTaskID,
		PlanID:             bundle.Plan.PlanID,
		Prompt:             planTask.Prompt,
		Complexity:         string(planTask.Complexity),
		Role:               recoveredPlanTaskRole(logicalTaskID),
		Priority:           idx + 1,
		DependsOn:          deps,
		TimeoutMinutes:     planTask.TimeoutMinutes,
		RequireFreshWorker: requireFresh,
	}, true
}

func recoveredPlanTask(bundle PlanRecord, runtimeTaskID string) (string, PlanTask, int, bool) {
	planID := strings.TrimSpace(bundle.PlanID)
	prefix := planID + "-"
	runtimeTaskID = strings.TrimSpace(runtimeTaskID)
	if planID == "" || !strings.HasPrefix(runtimeTaskID, prefix) {
		return "", PlanTask{}, 0, false
	}
	logicalTaskID := strings.TrimPrefix(runtimeTaskID, prefix)
	for idx, task := range bundle.Tasks {
		if strings.TrimSpace(task.ID) == logicalTaskID {
			return logicalTaskID, task, idx, true
		}
	}
	return "", PlanTask{}, 0, false
}

func recoveredPlanTaskRole(logicalTaskID string) string {
	if strings.HasPrefix(strings.TrimSpace(logicalTaskID), "fix-merge-") {
		return lineageFixMergeRole
	}
	return "implementer"
}

func recoveredPlanTaskFreshWorker(bundle StoredPlan, runtimeTaskID string) (bool, bool) {
	logicalTaskID, _, _, ok := recoveredPlanTask(bundle.Plan, runtimeTaskID)
	if !ok {
		return false, false
	}
	logicalTaskID = strings.TrimSpace(logicalTaskID)
	if strings.HasPrefix(logicalTaskID, "fix-merge-") ||
		strings.HasPrefix(logicalTaskID, "conflict-fix-") ||
		strings.HasPrefix(logicalTaskID, "review-fix-") {
		return true, true
	}
	return false, true
}

func (s *Scheduler) recoverFailedTasksOnStartup() error {
	if s == nil || s.pm == nil || s.plans == nil || s.git == nil {
		return nil
	}
	for _, task := range s.pm.Tasks() {
		if task.Status != pool.TaskFailed {
			continue
		}
		if strings.TrimSpace(task.PlanID) == "" {
			continue
		}
		if task.Role == plannerTaskRole || isReviewCouncilTask(task) {
			continue
		}

		class := s.taskFailureClass(&task)
		if class != FailureConflict && class != FailureAuth {
			continue
		}
		if class == FailureConflict && !s.shouldRetryConflict(task) {
			continue
		}
		if class == FailureAuth && strings.TrimSpace(s.authFailureRule().Action) == authActionFail {
			continue
		}
		if class == FailureConflict {
			bundle, err := s.plans.Get(task.PlanID)
			if err != nil {
				s.logf("startup: load failed task plan %s/%s: %v", task.PlanID, task.ID, err)
				continue
			}
			if err := s.pm.ReviveFailedTask(task.ID, true); err != nil {
				s.logf("startup: revive failed task %s: %v", task.ID, err)
				continue
			}
			if lineage := strings.TrimSpace(bundle.Plan.Lineage); lineage != "" {
				if err := s.git.DiscardChild(lineage, task.ID); err != nil {
					s.logf("startup: discard child worktree for failed task %s: %v", task.ID, err)
					continue
				}
				s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
			}

			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:    planHistoryConflictRetried,
				TaskID:  task.ID,
				Summary: "Retrying task from current lineage head after startup recovery.",
			})
			if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
				s.logf("startup: record conflict retry history for %s: %v", task.ID, err)
			}
			s.logf("startup: retrying %s-failed task %s (attempt %d)", class, task.ID, task.RetryCount+1)
			continue
		}
		if err := s.onTaskFailed(task.ID, class); err != nil {
			s.logf("startup: recover failed task %s: %v", task.ID, err)
			continue
		}
		s.logf("startup: retrying %s-failed task %s (attempt %d)", class, task.ID, task.RetryCount+1)
	}
	return nil
}

func (s *Scheduler) recoverOrphanedPlansOnStartup() error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	tasks := s.pm.Tasks()
	for _, plan := range plans {
		if plan.State != planStateActive {
			continue
		}
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil {
			s.logf("startup: load active plan %s: %v", plan.PlanID, err)
			continue
		}
		active, completed, failed := summarizeRelevantPlanTasks(tasks, bundle)
		if len(active) != 0 || len(completed) != 0 || len(failed) != 0 {
			continue
		}

		now := s.currentTime().UTC()
		bundle.Plan.State = planStatePlanningFailed
		bundle.Execution.State = planStatePlanningFailed
		bundle.Execution.ActiveTaskIDs = nil
		bundle.Execution.CompletedTaskIDs = nil
		bundle.Execution.FailedTaskIDs = nil
		bundle.Execution.CompletedAt = &now
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryPlanningFailed,
			Summary: "Plan left orphaned after crash - re-submit to retry.",
		})
		if err := s.plans.UpdateExecution(plan.PlanID, bundle.Execution); err != nil {
			s.logf("startup: persist orphaned plan execution %s: %v", plan.PlanID, err)
			continue
		}
		if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
			s.logf("startup: mark orphaned plan %s failed: %v", plan.PlanID, err)
			continue
		}
		s.logf("startup: marking orphaned plan %s as planning_failed (no tasks in pool)", plan.PlanID)
		if s.notify != nil {
			title := bundle.Plan.Title
			if title == "" {
				title = plan.PlanID
			}
			s.notify(pool.Notification{Type: "plan_failed", ID: plan.PlanID, Message: title})
		}
	}
	return nil
}

// recoverWaitingPlansOnStartup attempts activation of any
// waiting_on_dependency plans whose dependencies may have been
// merged while the scheduler was down.
func (s *Scheduler) recoverWaitingPlansOnStartup() {
	if s == nil || s.activatePlan == nil || s.plans == nil {
		return
	}
	plans, err := s.plans.List()
	if err != nil {
		return
	}
	for _, plan := range plans {
		if plan.State != planStateWaitingOnDependency {
			continue
		}
		if err := s.activatePlan(plan.PlanID); err != nil {
			s.logf("recover waiting plan %s: %v", plan.PlanID, err)
		}
	}
}

// reapOrphanPlanTasks cancels any non-terminal task whose referenced plan
// has been removed from disk. Without this, the scheduler would repeatedly
// try to load the missing plan on every reconcile tick and spam errors.
func (s *Scheduler) reapOrphanPlanTasks() {
	if s == nil || s.pm == nil || s.plans == nil {
		return
	}
	missing := make(map[string]bool)
	for _, t := range s.pm.Tasks() {
		if strings.TrimSpace(t.PlanID) == "" {
			continue
		}
		switch t.Status {
		case pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled:
			continue
		}
		gone, known := missing[t.PlanID]
		if !known {
			_, err := s.plans.Get(t.PlanID)
			if err == nil {
				missing[t.PlanID] = false
				continue
			}
			if !errors.Is(err, ErrPlanNotFound) {
				// Transient error — leave task alone; downstream callers
				// will surface the real error.
				missing[t.PlanID] = false
				continue
			}
			missing[t.PlanID] = true
			gone = true
			s.logf("scheduler: plan %s missing on disk, canceling orphan tasks", t.PlanID)
		}
		if gone {
			if err := s.pm.CancelTask(t.ID); err != nil {
				s.logf("scheduler: cancel orphan task %s: %v", t.ID, err)
			}
		}
	}
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
	if allowed, handled := s.workerCanRunReviewCouncilTask(worker, task); handled {
		return allowed
	}
	if allowed, handled := s.workerCanRunCouncilTask(worker, task); handled {
		return allowed
	}
	if s.pm != nil && !s.pm.WorkerHealthy(worker.ID, s.reapTimeout) {
		return false
	}
	if !workerRoleCompatible(worker, task) {
		return false
	}
	if !s.workerWorkspaceCompatible(worker, task) {
		return false
	}
	resolution := s.routeResolutionForTask(task)
	if routeResolutionUnavailable(resolution) {
		return false
	}
	keys := resolution.Keys
	if len(keys) == 0 || strings.TrimSpace(worker.Provider) == "" {
		return true
	}
	for _, key := range keys {
		if poolKeyMatchesWorker(key, worker) {
			return true
		}
	}
	return false
}

func workerRoleCompatible(worker pool.Worker, task pool.Task) bool {
	workerRole := strings.TrimSpace(worker.Role)
	taskRole := strings.TrimSpace(task.Role)

	if workerRole == "" || workerRole == "general" || taskRole == "" {
		return true
	}
	return workerRole == taskRole
}

func (s *Scheduler) workerWorkspaceCompatible(worker pool.Worker, task pool.Task) bool {
	actual := strings.TrimSpace(worker.WorkspacePath)
	expected, exact, ok := s.expectedWorkspaceForTask(task)
	if !ok {
		return actual == ""
	}
	if actual == "" {
		return false
	}
	if _, err := os.Stat(actual); err != nil {
		return false
	}
	actual = filepath.Clean(actual)
	expected = filepath.Clean(expected)
	if exact {
		return actual == expected
	}
	return pathWithinBase(actual, expected)
}

func (s *Scheduler) expectedWorkspaceForTask(task pool.Task) (path string, exact bool, ok bool) {
	if s == nil || s.git == nil || s.plans == nil || strings.TrimSpace(task.PlanID) == "" {
		return "", false, false
	}
	if task.Role == plannerTaskRole || isLineageMergeTask(task) {
		return s.git.repoPath, true, true
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return "", false, false
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	if lineage == "" {
		return "", false, false
	}
	lineageDir := filepath.Join(s.git.worktreeBase, lineage)
	switch task.Role {
	case lineageFixMergeRole:
		return filepath.Join(lineageDir, "fix-merge-"+task.ID), true, true
	default:
		return lineageDir, false, true
	}
}

func pathWithinBase(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func routeResolutionUnavailable(resolution RouteResolution) bool {
	return resolution.Availability != "" && resolution.Availability != RouteAvailable
}

func routeResolutionTemporarilyExhausted(resolution RouteResolution) bool {
	return resolution.Availability == RouteTemporarilyExhausted
}

func routeResolutionUnroutable(resolution RouteResolution) bool {
	return resolution.Availability == RouteUnroutable
}

func routeResolutionCandidateSummary(candidates []PoolKey) string {
	if len(candidates) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		part := strings.TrimSpace(candidate.Provider)
		if model := strings.TrimSpace(candidate.Model); model != "" {
			part += "/" + model
		}
		if part == "" {
			part = "unknown"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func routeResolutionFingerprint(resolution RouteResolution) string {
	return string(resolution.Availability) + ":" + routeResolutionCandidateSummary(resolution.Candidates)
}

func (s *Scheduler) noteRouteUnavailable(task pool.Task, resolution RouteResolution) {
	if s == nil || !routeResolutionUnavailable(resolution) {
		return
	}
	taskID := strings.TrimSpace(task.ID)
	if taskID == "" {
		return
	}
	fingerprint := routeResolutionFingerprint(resolution)
	if s.routeNotices[taskID] == fingerprint {
		return
	}
	s.routeNotices[taskID] = fingerprint
	candidates := routeResolutionCandidateSummary(resolution.Candidates)
	switch resolution.Availability {
	case RouteTemporarilyExhausted:
		s.logf("task %s blocked: no eligible provider currently available for role %q on routes [%s]; waiting for provider health to recover", taskID, task.Role, candidates)
	case RouteUnroutable:
		s.logf("task %s unroutable: no configured provider route available for role %q on routes [%s]", taskID, task.Role, candidates)
	}
}

func (s *Scheduler) handleRouteUnavailableTask(task pool.Task, resolution RouteResolution) (bool, error) {
	if !routeResolutionUnavailable(resolution) {
		return false, nil
	}
	if routeResolutionTemporarilyExhausted(resolution) {
		s.noteRouteUnavailable(task, resolution)
		return false, nil
	}
	if routeResolutionUnroutable(resolution) {
		return false, s.failQueuedTaskForRoute(task, resolution)
	}
	s.noteRouteUnavailable(task, resolution)
	return false, nil
}

func (s *Scheduler) failQueuedTaskForRoute(task pool.Task, resolution RouteResolution) error {
	if s == nil || s.pm == nil {
		return nil
	}
	current, ok := s.pm.Task(task.ID)
	if !ok || current.Status != pool.TaskQueued {
		return nil
	}
	message := fmt.Sprintf("task %s unroutable: no configured provider route available for role %q on routes [%s]", task.ID, task.Role, routeResolutionCandidateSummary(resolution.Candidates))
	detail, err := json.Marshal(pool.FailureDetail{Summary: message})
	if err != nil {
		return err
	}
	if err := s.pm.FailQueuedTaskDetailed(task.ID, message, string(FailureCapability), detail); err != nil {
		return err
	}
	s.clearRouteNotice(task.ID)
	return s.onTaskFailed(task.ID, FailureCapability)
}

func (s *Scheduler) clearRouteNotice(taskID string) {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	delete(s.routeNotices, taskID)
}

func (s *Scheduler) pruneRouteNotices() {
	if s == nil || s.pm == nil || len(s.routeNotices) == 0 {
		return
	}
	for taskID := range s.routeNotices {
		task, ok := s.pm.Task(taskID)
		if !ok || task.Status != pool.TaskQueued {
			delete(s.routeNotices, taskID)
			continue
		}
		if !routeResolutionUnavailable(s.routeResolutionForTask(*task)) {
			delete(s.routeNotices, taskID)
		}
	}
}

func (s *Scheduler) routeResolutionForTask(task pool.Task) RouteResolution {
	if s == nil || s.router == nil {
		return RouteResolution{Availability: RouteAvailable}
	}
	if !isValidComplexity(Complexity(task.Complexity)) {
		return RouteResolution{Availability: RouteAvailable}
	}
	if retryKey, ok := retryRoutePoolKey(task); ok {
		return RouteResolution{
			Keys:         []PoolKey{retryKey},
			Candidates:   []PoolKey{retryKey},
			Availability: RouteAvailable,
		}
	}
	// Check plan-level provider override before council seat / role routing.
	if task.PlanID != "" && s.plans != nil {
		if bundle, err := s.plans.Get(task.PlanID); err == nil && bundle.Plan.ProviderOverrides != nil {
			overrides := bundle.Plan.ProviderOverrides
			var overrideProvider string
			if task.Role == plannerTaskRole {
				if seat := councilSeatForTask(task); seat != "" {
					overrideProvider = overrides.BySeat[seat]
				}
			} else if isReviewCouncilTask(task) {
				turn := reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID)
				if seat := reviewCouncilSeatForTurn(turn); seat != "" {
					overrideProvider = overrides.BySeat[seat]
				}
			}
			if overrideProvider == "" {
				overrideProvider = overrides.ByRole[task.Role]
			}
			if overrideProvider != "" {
				if keys := s.router.ResolveProvider(overrideProvider, Complexity(task.Complexity)); len(keys) > 0 {
					return RouteResolution{Keys: keys, Candidates: keys, Availability: RouteAvailable}
				}
			}
		}
	}
	if task.Role == plannerTaskRole && task.PlanID != "" {
		if seat := councilSeatForTask(task); seat != "" {
			return s.router.ResolveCouncilSeatOutcome(seat, Complexity(task.Complexity))
		}
	}
	return s.router.ResolveForRoleOutcome(task.Role, Complexity(task.Complexity))
}

func (s *Scheduler) routeKeysForTask(task pool.Task) []PoolKey {
	return s.routeResolutionForTask(task).Keys
}

func councilSeatForTask(task pool.Task) string {
	if strings.TrimSpace(task.PlanID) == "" {
		return ""
	}
	return councilSeatForTurn(councilTurnNumberFromTaskID(task.PlanID, task.ID))
}

func poolKeyMatchesWorker(key PoolKey, worker pool.Worker) bool {
	if !sameProvider(key.Provider, worker.Provider) {
		return false
	}
	keyModel := strings.TrimSpace(key.Model)
	workerModel := strings.TrimSpace(worker.Model)
	if keyModel != "" && workerModel != "" && keyModel != workerModel {
		return false
	}
	keyAdapter := strings.TrimSpace(key.Adapter)
	workerAdapter := strings.TrimSpace(worker.Adapter)
	return keyAdapter == "" || workerAdapter == "" || keyAdapter == workerAdapter
}

func workerMatchesAnyRouteKey(worker pool.Worker, keys []PoolKey) bool {
	if len(keys) == 0 {
		return true
	}
	if strings.TrimSpace(worker.Provider) == "" {
		return false
	}
	for _, key := range keys {
		if poolKeyMatchesWorker(key, worker) {
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
		if dep.Status != pool.TaskCompleted {
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

// allTasksAreConflictFailed reports whether every task in failedIDs failed due
// to a merge conflict. Conflict failures keep the plan active so the operator
// can resolve them manually or via a fix-merge task.
func allTasksAreConflictFailed(tasks []pool.Task, failedIDs []string) bool {
	if len(failedIDs) == 0 {
		return false
	}
	taskMap := make(map[string]pool.Task, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
	}
	for _, id := range failedIDs {
		t, ok := taskMap[id]
		if !ok {
			return false
		}
		if t.Result == nil || !strings.Contains(strings.ToLower(t.Result.Error), "merge conflict") {
			return false
		}
	}
	return true
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
	wasCompleted := executionStateHint(bundle) == planStateCompleted

	tasks := s.pm.Tasks()
	if bundle.Execution.AutoRemediationActive {
		active, completed, failed := summarizeRelevantPlanTasks(tasks, bundle)
		if len(active) == 0 && len(failed) == 0 && strings.TrimSpace(bundle.Execution.AutoRemediationTaskID) != "" && containsTrimmedString(completed, bundle.Execution.AutoRemediationTaskID) {
			completeAutoRemediationState(&bundle.Execution)
		}
	}

	projection := projectPlan(bundle, tasks, len(pendingQuestionsForPlan(s.pm, planID)))
	bundle.Execution.ActiveTaskIDs = projection.ActiveTaskIDs
	bundle.Execution.CompletedTaskIDs = projection.CompletedTaskIDs
	bundle.Execution.FailedTaskIDs = projection.FailedTaskIDs
	if projection.State == planStateImplementationReview && executionStateHint(bundle) != planStateImplementationReview && shouldProjectImplementationReview(bundle.Execution) {
		return s.enqueueImplementationReview(bundle)
	}

	bundle.Plan.State = projectedPersistentPlanState(projection.State)
	bundle.Execution.State = projectedPersistentExecutionState(projection.State)
	if projection.State == planStateCompleted {
		now := time.Now().UTC()
		bundle.Execution.CompletedAt = &now
	} else {
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

func hasCanceledPlanTrackedTask(tasks []pool.Task, bundle StoredPlan) bool {
	if strings.TrimSpace(bundle.Execution.State) != planStateActive {
		return false
	}
	for _, task := range tasks {
		if task.PlanID != bundle.Plan.PlanID || task.Status != pool.TaskCanceled {
			continue
		}
		if task.Role == plannerTaskRole || isReviewCouncilTask(task) || isLineageMergeTask(task) {
			continue
		}
		if _, _, _, ok := recoveredPlanTask(bundle.Plan, task.ID); ok {
			return true
		}
	}
	return false
}

func shouldEnqueueImplementationReview(exec ExecutionRecord) bool {
	if !exec.ImplReviewRequested || exec.State != planStateActive {
		return false
	}
	if exec.AutoRemediationActive {
		return false
	}
	if strings.TrimSpace(exec.ImplReviewStatus) != "" || exec.ImplReviewedAt != nil {
		return false
	}
	if exec.ReviewCouncilTurnsCompleted > 0 || strings.TrimSpace(exec.ReviewCouncilFinalDecision) != "" || len(exec.ReviewCouncilTurns) > 0 {
		return false
	}
	return true
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
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
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

func (s *Scheduler) enqueueImplementationReview(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	if bundle.Execution.ReviewCouncilTurnsCompleted > 0 || bundle.Execution.ReviewCouncilFinalDecision != "" {
		return nil
	}
	bundle.Execution.ReviewCouncilMaxTurns = 4
	bundle.Execution.ReviewCouncilCycle = nextReviewCouncilCycle(bundle.Execution)
	bundle.Execution.ReviewCouncilTurnsCompleted = 0
	bundle.Execution.ReviewCouncilSeats = newReviewCouncilSeats()
	bundle.Execution.ReviewCouncilAwaitingAnswers = false
	bundle.Execution.ReviewCouncilFinalDecision = ""
	bundle.Execution.ReviewCouncilTurns = nil
	bundle.Execution.ReviewCouncilWarnings = nil
	bundle.Execution.ReviewCouncilUnresolvedDisagreements = nil
	bundle.Plan.State = planStateImplementationReview
	bundle.Execution.State = planStateImplementationReview
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryImplReviewRequested,
		Summary: "Implementation queued for post-implementation review.",
	})
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewCouncilStarted,
		Summary: "Review council started.",
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_impl_review_requested", ID: bundle.Plan.PlanID, Message: bundle.Plan.Title})
		s.notify(pool.Notification{Type: "plan_review_council_started", ID: bundle.Plan.PlanID, Message: bundle.Plan.Title})
	}
	return s.enqueueReviewCouncilTurn(bundle)
}

func (s *Scheduler) recoverAutoRemediationIntents() error {
	if s == nil || s.plans == nil || s.pm == nil {
		return nil
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil {
			continue
		}
		if !bundle.Execution.AutoRemediationActive {
			continue
		}
		if err := s.ensureAutoRemediationTask(&bundle, true); err != nil {
			return err
		}
	}
	return nil
}

func isPlanControlTask(task pool.Task) bool {
	return task.Role == plannerTaskRole || isLineageMergeTask(task)
}

func implementationReviewComplexityForPlan(plan PlanRecord) Complexity {
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

func summarizeRelevantPlanTasks(tasks []pool.Task, bundle StoredPlan) (active []string, completed []string, failed []string) {
	planID := bundle.Plan.PlanID
	for _, task := range tasks {
		if task.PlanID != planID {
			continue
		}
		if !taskCountsTowardExecution(task, bundle) {
			continue
		}
		switch task.Status {
		case pool.TaskCompleted:
			completed = append(completed, task.ID)
		case pool.TaskFailed:
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

func taskCountsTowardExecution(task pool.Task, bundle StoredPlan) bool {
	switch task.Status {
	case pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled:
	default:
		return true
	}
	state := executionStateHint(bundle)
	if isLineageMergeTask(task) {
		return state == planStateMerging
	}
	if task.Role == plannerTaskRole {
		switch state {
		case planStatePlanning, planStateReviewing, planStatePendingApproval:
			return true
		default:
			return false
		}
	}
	if isReviewCouncilTask(task) {
		return state == planStateImplementationReview
	}
	return true
}
