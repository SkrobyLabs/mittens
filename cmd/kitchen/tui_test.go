package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type fakeKitchenTUIBackend struct {
	deletedPlanID             string
	deleteCalls               int
	plans                     []PlanRecord
	submitPlanID              string
	submittedIdea             string
	retriedTaskID             string
	retriedRequireFreshWorker bool
	retryCalls                int
}

func (b *fakeKitchenTUIBackend) Label() string                      { return "test" }
func (b *fakeKitchenTUIBackend) Status() (tuiStatusSnapshot, error) { return tuiStatusSnapshot{}, nil }
func (b *fakeKitchenTUIBackend) ListPlans() ([]PlanRecord, error) {
	return append([]PlanRecord(nil), b.plans...), nil
}
func (b *fakeKitchenTUIBackend) PlanDetail(planID string) (PlanDetail, error) {
	for _, plan := range b.plans {
		if plan.PlanID == planID {
			return PlanDetail{Plan: plan}, nil
		}
	}
	return PlanDetail{}, fmt.Errorf("plan %s not found", planID)
}
func (b *fakeKitchenTUIBackend) TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error) {
	return nil, nil
}
func (b *fakeKitchenTUIBackend) ListQuestions() ([]pool.Question, error) { return nil, nil }
func (b *fakeKitchenTUIBackend) SubmitIdea(idea string) (string, error) {
	b.submittedIdea = idea
	if strings.TrimSpace(b.submitPlanID) == "" {
		return "plan_submitted", nil
	}
	return b.submitPlanID, nil
}
func (b *fakeKitchenTUIBackend) ApprovePlan(planID string) error { return nil }
func (b *fakeKitchenTUIBackend) CancelPlan(planID string) error  { return nil }
func (b *fakeKitchenTUIBackend) DeletePlan(planID string) error {
	b.deleteCalls++
	b.deletedPlanID = planID
	return nil
}
func (b *fakeKitchenTUIBackend) CancelTask(taskID string) error { return nil }
func (b *fakeKitchenTUIBackend) RetryTask(taskID string, requireFreshWorker bool) error {
	b.retryCalls++
	b.retriedTaskID = taskID
	b.retriedRequireFreshWorker = requireFreshWorker
	return nil
}
func (b *fakeKitchenTUIBackend) ReplanPlan(planID, reason string) (string, error) { return "", nil }
func (b *fakeKitchenTUIBackend) MergeCheck(lineage string) (string, error)        { return "", nil }
func (b *fakeKitchenTUIBackend) MergeLineage(lineage string) (string, error)      { return "", nil }

func textInputWithValue(value string) textinput.Model {
	input := textinput.New()
	input.SetValue(value)
	return input
}

func TestKitchenTUIRetryKeyRetriesFailedTask(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftTasks,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_retry", Title: "Retry"}},
		},
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskFailed},
		},
	}

	updatedModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil {
		t.Fatal("expected retry command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if action.status != "retried plan_retry-t1" {
		t.Fatalf("action status = %q", action.status)
	}
	if action.selectedPlanID != "plan_retry" {
		t.Fatalf("selectedPlanID = %q, want plan_retry", action.selectedPlanID)
	}
	if backend.retryCalls != 1 || backend.retriedTaskID != "plan_retry-t1" || !backend.retriedRequireFreshWorker {
		t.Fatalf("backend retry = %+v", backend)
	}
	if _, ok := updatedModel.(kitchenTUIModel); !ok {
		t.Fatalf("updated model = %T, want kitchenTUIModel", updatedModel)
	}
}

func TestKitchenTUIRetryKeyRejectsNonFailedTask(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskQueued},
		},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd != nil {
		t.Fatal("expected no command for non-failed task")
	}
	got := updated.(kitchenTUIModel)
	if got.flash != "selected task cannot be retried" {
		t.Fatalf("flash = %q", got.flash)
	}
	if backend.retryCalls != 0 {
		t.Fatalf("retryCalls = %d, want 0", backend.retryCalls)
	}
}

func TestKitchenTUIRetryKeyQueuesCompletedTaskAgain(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftTasks,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_retry", Title: "Retry"}},
		},
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskCompleted},
		},
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil {
		t.Fatal("expected retry command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.status != "queued again plan_retry-t1" {
		t.Fatalf("action status = %q", action.status)
	}
	if backend.retryCalls != 1 || backend.retriedTaskID != "plan_retry-t1" || !backend.retriedRequireFreshWorker {
		t.Fatalf("backend retry = %+v", backend)
	}
}

func TestKitchenTUIReuseKeyAllowsWorkerReuse(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftTasks,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_retry", Title: "Retry"}},
		},
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskFailed},
		},
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	if cmd == nil {
		t.Fatal("expected reuse command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.status != "retried plan_retry-t1 (reuse allowed)" {
		t.Fatalf("action status = %q", action.status)
	}
	if backend.retryCalls != 1 || backend.retriedTaskID != "plan_retry-t1" || backend.retriedRequireFreshWorker {
		t.Fatalf("backend retry = %+v", backend)
	}
}

func TestKitchenTUIDeleteKeyDeletesSelectedPlan(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_delete", Title: "Delete me"}},
		},
	}

	updatedModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if action.status != "deleted plan_delete" {
		t.Fatalf("action status = %q", action.status)
	}
	if action.selectedPlanID != "" {
		t.Fatalf("selectedPlanID = %q, want empty after delete", action.selectedPlanID)
	}
	if backend.deleteCalls != 1 || backend.deletedPlanID != "plan_delete" {
		t.Fatalf("backend delete = %+v", backend)
	}
	if _, ok := updatedModel.(kitchenTUIModel); !ok {
		t.Fatalf("updated model = %T, want kitchenTUIModel", updatedModel)
	}
}

func TestKitchenTUISubmitEnterClosesInputAndQueuesLoad(t *testing.T) {
	backend := &fakeKitchenTUIBackend{submitPlanID: "plan_new"}
	model := kitchenTUIModel{
		backend:   backend,
		inputMode: kitchenTUIInputSubmit,
	}
	model.input = textInputWithValue("Add typed parser errors")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want none", got.inputMode)
	}
	if backend.submittedIdea != "" {
		t.Fatalf("submit should not run until command executes, got %q", backend.submittedIdea)
	}

	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if action.status != "submitted plan_new" || action.selectedPlanID != "plan_new" {
		t.Fatalf("action = %+v", action)
	}
	if backend.submittedIdea != "Add typed parser errors" {
		t.Fatalf("submittedIdea = %q, want entered value", backend.submittedIdea)
	}
}

func TestKitchenTUIActionSuccessKeepsInputClosed(t *testing.T) {
	model := kitchenTUIModel{
		inputMode: kitchenTUIInputSubmit,
	}

	updated, cmd := model.Update(kitchenTUIActionMsg{status: "submitted plan_new", selectedPlanID: "plan_new"})
	if cmd == nil {
		t.Fatal("expected reload command")
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want none", got.inputMode)
	}
	if got.pendingSelectedID != "plan_new" {
		t.Fatalf("pendingSelectedID = %q, want plan_new", got.pendingSelectedID)
	}
}
func TestKitchenTUIDeleteKeyRetargetsSelectionBeforeRefresh(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:      backend,
		leftMode:     kitchenTUILeftPlans,
		selectedPlan: 1,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_a", Title: "A"}},
			{Record: PlanRecord{PlanID: "plan_b", Title: "B"}},
		},
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	if cmd == nil {
		t.Fatal("expected delete command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.selectedPlanID != "plan_a" {
		t.Fatalf("selectedPlanID = %q, want previous plan", action.selectedPlanID)
	}
}

func TestKitchenTUILoadCmdFallsBackWhenSelectedPlanWasDeleted(t *testing.T) {
	backend := &fakeKitchenTUIBackend{
		plans: []PlanRecord{
			{PlanID: "plan_a", Title: "A"},
		},
	}
	model := kitchenTUIModel{
		backend:           backend,
		pendingSelectedID: "plan_deleted",
	}

	msg := model.loadCmd()()
	loaded, ok := msg.(kitchenTUILoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUILoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loaded err = %v, want fallback to remaining plan", loaded.err)
	}
	if loaded.detail == nil || loaded.detail.Plan.PlanID != "plan_a" {
		t.Fatalf("detail = %+v, want fallback plan_a", loaded.detail)
	}
}

func TestKitchenTUIFooterShowsTaskActionsOnlyForSelectedTask(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskFailed},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "R retry") {
		t.Fatalf("footer = %q, want retry action", footer)
	}
	if !strings.Contains(footer, "U reuse") {
		t.Fatalf("footer = %q, want reuse action", footer)
	}
	if strings.Contains(footer, "a approve") || strings.Contains(footer, "p replan") || strings.Contains(footer, "D delete") || strings.Contains(footer, "M merge") {
		t.Fatalf("footer = %q, want only task-scoped actions", footer)
	}
}

func TestKitchenTUIRenderPlansPaneShowsLineageAndPlanID(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_61355bea02", Title: "Add Gemini CLI adapter to Kitchen", Lineage: "gemini-adapter", State: planStatePendingApproval}},
			{Record: PlanRecord{PlanID: "plan_e7513ed6de", Title: "TUI question indicators and interactive answering", Lineage: "in-the-tui-mark-the-pending-questions-against-th", State: planStateMerged}},
		},
	}

	pane := model.renderPlansPane(120, 12)
	if !strings.Contains(pane, "lineage: gemini-adapter  plan: plan_61355bea02") {
		t.Fatalf("plans pane = %q, want labeled gemini-adapter lineage", pane)
	}
	if !strings.Contains(pane, "lineage: in-the-tui-mark-the-pending-questions-against-th  plan: plan_e7513ed6de") {
		t.Fatalf("plans pane = %q, want labeled questions lineage", pane)
	}
}

func TestKitchenTUIFooterShowsAgainForCompletedTask(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_retry-t1", Title: "Task 1", State: pool.TaskCompleted},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "R again") {
		t.Fatalf("footer = %q, want again action", footer)
	}
	if !strings.Contains(footer, "U reuse") {
		t.Fatalf("footer = %q, want reuse action", footer)
	}
	if strings.Contains(footer, "c cancel") {
		t.Fatalf("footer = %q, did not expect cancel action for completed task", footer)
	}
}
func TestKitchenTUIFooterShowsPlanActionsOnlyWhenActionable(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_retry", Title: "Retry", Lineage: "feat/retry", State: planStatePendingApproval},
				Progress: &PlanProgress{State: planStatePendingApproval},
			},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "a approve") {
		t.Fatalf("footer = %q, want approve action", footer)
	}
	if !strings.Contains(footer, "p replan") || !strings.Contains(footer, "D delete") || !strings.Contains(footer, "m check") {
		t.Fatalf("footer = %q, want actionable plan actions", footer)
	}
	if strings.Contains(footer, "R retry") || strings.Contains(footer, "M merge") {
		t.Fatalf("footer = %q, did not expect unavailable actions", footer)
	}
}

func TestKitchenTUIFooterShowsMergeOnlyForCompletedPlan(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "feat/merge", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "M merge") {
		t.Fatalf("footer = %q, want merge action for completed plan", footer)
	}
}
