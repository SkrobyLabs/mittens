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
	questions                 []pool.Question
	submitPlanID              string
	submittedIdea             string
	submittedImplReview       bool
	retriedTaskID             string
	retriedRequireFreshWorker bool
	retryCalls                int
	answeredQuestionID        string
	answeredQuestionAnswer    string
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
func (b *fakeKitchenTUIBackend) ListQuestions() ([]pool.Question, error) {
	return append([]pool.Question(nil), b.questions...), nil
}
func (b *fakeKitchenTUIBackend) SubmitIdea(idea string, implReview bool) (string, error) {
	b.submittedIdea = idea
	b.submittedImplReview = implReview
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
func (b *fakeKitchenTUIBackend) FixConflicts(taskID string) (string, error)       { return "", nil }
func (b *fakeKitchenTUIBackend) ReplanPlan(planID, reason string) (string, error) { return "", nil }
func (b *fakeKitchenTUIBackend) AnswerQuestion(id, answer string) error {
	b.answeredQuestionID = id
	b.answeredQuestionAnswer = answer
	return nil
}
func (b *fakeKitchenTUIBackend) MergeCheck(lineage string) (string, error)   { return "", nil }
func (b *fakeKitchenTUIBackend) MergeLineage(lineage string) (string, error) { return "", nil }
func (b *fakeKitchenTUIBackend) FixLineageConflicts(lineage string) (string, error) {
	return "", nil
}

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
		backend:          backend,
		inputMode:        kitchenTUIInputSubmit,
		submitImplReview: true,
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
	if action.status != "submitted plan_new with impl review" || action.selectedPlanID != "plan_new" {
		t.Fatalf("action = %+v", action)
	}
	if backend.submittedIdea != "Add typed parser errors" {
		t.Fatalf("submittedIdea = %q, want entered value", backend.submittedIdea)
	}
	if !backend.submittedImplReview {
		t.Fatal("submittedImplReview = false, want true")
	}
}

func TestKitchenTUISubmitTabTogglesImplReview(t *testing.T) {
	model := kitchenTUIModel{inputMode: kitchenTUIInputSubmit}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := updated.(kitchenTUIModel)
	if !got.submitImplReview {
		t.Fatal("submitImplReview = false, want true after tab")
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyTab})
	got = updated.(kitchenTUIModel)
	if got.submitImplReview {
		t.Fatal("submitImplReview = true, want false after second tab")
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

func TestKitchenTUIViewDoesNotShowSubmitBarAfterInputClosed(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:   backend,
		width:     120,
		height:    40,
		inputMode: kitchenTUIInputSubmit,
	}
	model.input = textInputWithValue("some idea")

	updated, _ := model.Update(kitchenTUIActionMsg{status: "submitted plan_new", selectedPlanID: "plan_new"})
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q after action success, want none", got.inputMode)
	}

	view := got.View()
	if strings.Contains(view, "Submit") {
		t.Fatalf("View() contains 'Submit' bar after input closed:\n%s", view)
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

func TestKitchenTUIFooterHidesMergeForFailedImplementationReview(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record: PlanRecord{PlanID: "plan_impl_fail", Title: "Impl Fail", Lineage: "feat/merge", State: planStateCompleted},
				Progress: &PlanProgress{
					State:               planStateCompleted,
					ImplReviewRequested: true,
					ImplReviewStatus:    planReviewStatusFailed,
				},
			},
		},
	}

	footer := model.renderFooter()
	if strings.Contains(footer, "M merge") {
		t.Fatalf("footer = %q, did not expect merge action after failed impl review", footer)
	}
}

// helpers for question detail pane tests

func questionModel(q pool.Question) kitchenTUIModel {
	return kitchenTUIModel{
		leftMode: kitchenTUILeftQuestions,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_q"}},
		},
		questions: []pool.Question{q},
	}
}

func TestRenderQuestionDetailPaneFreeText(t *testing.T) {
	q := pool.Question{
		ID:       "q1",
		WorkerID: "w1",
		TaskID:   "plan_q-t1",
		Question: "What is the intended behavior?",
		Context:  "This context block explains the background.",
	}
	model := questionModel(q)
	out := model.renderQuestionDetailPane(80, 30)

	if !strings.Contains(out, "What is the intended behavior?") {
		t.Fatalf("output missing question text: %q", out)
	}
	if !strings.Contains(out, "This context block explains the background.") {
		t.Fatalf("output missing context: %q", out)
	}
	if !strings.Contains(out, "w1") {
		t.Fatalf("output missing worker ID: %q", out)
	}
	if !strings.Contains(out, "Press 'a' to answer") {
		t.Fatalf("output missing answer hint: %q", out)
	}
}

func TestRenderQuestionDetailPaneMultipleChoice(t *testing.T) {
	q := pool.Question{
		ID:       "q2",
		WorkerID: "w2",
		TaskID:   "plan_q-t1",
		Question: "Which approach?",
		Options:  []string{"Option A", "Option B", "Option C"},
	}
	model := questionModel(q)
	out := model.renderQuestionDetailPane(80, 30)

	for _, opt := range []string{"Option A", "Option B", "Option C"} {
		if !strings.Contains(out, opt) {
			t.Fatalf("output missing %q: %q", opt, out)
		}
	}
	if !strings.Contains(out, "Press 'a' to answer") {
		t.Fatalf("output missing answer hint: %q", out)
	}
}

func TestRenderQuestionDetailPaneMultipleChoiceHighlight(t *testing.T) {
	q := pool.Question{
		ID:      "q3",
		TaskID:  "plan_q-t1",
		Options: []string{"Option A", "Option B", "Option C"},
	}
	model := questionModel(q)
	model.inputMode = kitchenTUIInputAnswer
	model.selectedOption = 1
	out := model.renderQuestionDetailPane(80, 30)

	if !strings.Contains(out, "Option A") {
		t.Fatalf("output missing Option A: %q", out)
	}
	if !strings.Contains(out, "Option B") {
		t.Fatalf("output missing Option B: %q", out)
	}
	if !strings.Contains(out, "↑/↓") {
		t.Fatalf("output missing navigation hint: %q", out)
	}
}

func TestMultipleChoiceSubmitViaEnter(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	q := pool.Question{
		ID:      "q4",
		TaskID:  "plan_q-t1",
		Options: []string{"Option A", "Option B"},
	}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftQuestions,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_q"}},
		},
		questions:      []pool.Question{q},
		inputMode:      kitchenTUIInputAnswer,
		selectedOption: 0,
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected command after enter")
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want none", got.inputMode)
	}

	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if backend.answeredQuestionID != "q4" {
		t.Fatalf("answeredQuestionID = %q, want q4", backend.answeredQuestionID)
	}
	if backend.answeredQuestionAnswer != "Option A" {
		t.Fatalf("answeredQuestionAnswer = %q, want Option A", backend.answeredQuestionAnswer)
	}
}

func TestFreeTextSubmitViaEnter(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	q := pool.Question{
		ID:     "q5",
		TaskID: "plan_q-t1",
	}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftQuestions,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_q"}},
		},
		questions: []pool.Question{q},
		inputMode: kitchenTUIInputAnswer,
	}
	model.input = textInputWithValue("my answer")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected command after enter")
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want none", got.inputMode)
	}

	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if backend.answeredQuestionID != "q5" {
		t.Fatalf("answeredQuestionID = %q, want q5", backend.answeredQuestionID)
	}
	if backend.answeredQuestionAnswer != "my answer" {
		t.Fatalf("answeredQuestionAnswer = %q, want my answer", backend.answeredQuestionAnswer)
	}
}

func TestRenderQuestionDetailPaneAlreadyAnswered(t *testing.T) {
	q := pool.Question{
		ID:       "q6",
		TaskID:   "plan_q-t1",
		Question: "Should we proceed?",
		Answered: true,
		Answer:   "done",
	}
	model := questionModel(q)
	out := model.renderQuestionDetailPane(80, 30)

	if !strings.Contains(out, "done") {
		t.Fatalf("output missing answer 'done': %q", out)
	}
	if strings.Contains(out, "Press 'a' to answer") {
		t.Fatalf("output should not contain answer hint for already-answered question: %q", out)
	}
}

func TestBuildTaskItemsIncludesImplementationReviewCycle(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{PlanID: "plan_impl_review"},
		Progress: PlanProgress{
			Cycles: []PlanCycleProgress{{
				Index:               1,
				ImplReviewTaskID:    "plan_impl_review-impl-review-1",
				ImplReviewTaskState: pool.TaskQueued,
			}},
		},
	}
	snapshot := tuiStatusSnapshot{
		Queue: struct {
			AliveWorkers     int                `json:"aliveWorkers"`
			MaxWorkers       int                `json:"maxWorkers"`
			PendingQuestions int                `json:"pendingQuestions"`
			Tasks            []pool.TaskSummary `json:"tasks"`
		}{
			Tasks: []pool.TaskSummary{{
				ID:     "plan_impl_review-impl-review-1",
				Status: pool.TaskQueued,
			}},
		},
	}

	items := buildTaskItems(detail, snapshot)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one impl-review item", items)
	}
	if items[0].Kind != "implementation-review" {
		t.Fatalf("kind = %q, want implementation-review", items[0].Kind)
	}
	if items[0].RuntimeID != "plan_impl_review-impl-review-1" {
		t.Fatalf("runtimeID = %q, want impl review runtime id", items[0].RuntimeID)
	}
}

// TestKitchenTUIRenderPlansPaneBadge verifies that plans with pending questions show a
// [N?] badge and plans without questions do not.
func TestKitchenTUIRenderPlansPaneBadge(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_has_q", Title: "Has Questions"}},
			{Record: PlanRecord{PlanID: "plan_no_q", Title: "No Questions"}},
		},
		questions: []pool.Question{
			{ID: "q1", TaskID: "plan_has_q-t1", Question: "What is the intended behavior?"},
		},
	}

	pane := model.renderPlansPane(120, 20)

	if !strings.Contains(pane, "[1?]") {
		t.Fatalf("plans pane missing [1?] badge for plan with questions: %q", pane)
	}

	// Split into lines and check that no line mentioning plan_no_q contains a badge.
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(line, "plan_no_q") && strings.Contains(line, "?]") {
			t.Fatalf("plan_no_q row should not contain a badge, got line: %q", line)
		}
	}
}

// TestKitchenTUIQuestionKeyEntersQuestionsMode verifies that pressing '?' in plans mode
// switches leftMode to kitchenTUILeftQuestions with selectedQuestion reset to 0.
func TestKitchenTUIQuestionKeyEntersQuestionsMode(t *testing.T) {
	model := kitchenTUIModel{
		backend:  &fakeKitchenTUIBackend{},
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_q", Title: "Plan Q"}},
		},
		questions: []pool.Question{
			{ID: "q1", TaskID: "plan_q-t1", Question: "Shall we proceed?"},
		},
		selectedQuestion: 5, // ensure it gets reset
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if cmd != nil {
		t.Fatal("expected no command for ? key transition")
	}
	got, ok := updated.(kitchenTUIModel)
	if !ok {
		t.Fatalf("updated model = %T, want kitchenTUIModel", updated)
	}
	if got.leftMode != kitchenTUILeftQuestions {
		t.Fatalf("leftMode = %q, want kitchenTUILeftQuestions", got.leftMode)
	}
	if got.selectedQuestion != 0 {
		t.Fatalf("selectedQuestion = %d, want 0", got.selectedQuestion)
	}
}

// TestKitchenTUIRenderQuestionsPaneScoping verifies that renderQuestionsPane only shows
// questions belonging to the currently selected plan.
func TestKitchenTUIRenderQuestionsPaneScoping(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftQuestions,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_alpha", Title: "Alpha"}},
			{Record: PlanRecord{PlanID: "plan_beta", Title: "Beta"}},
		},
		selectedPlan: 0, // plan_alpha is selected
		questions: []pool.Question{
			{ID: "qa1", TaskID: "plan_alpha-t1", Question: "Alpha question one"},
			{ID: "qb1", TaskID: "plan_beta-t1", Question: "Beta question one"},
		},
	}

	pane := model.renderQuestionsPane(120, 20)

	if !strings.Contains(pane, "Alpha question one") {
		t.Fatalf("questions pane missing selected plan's question: %q", pane)
	}
	if strings.Contains(pane, "Beta question one") {
		t.Fatalf("questions pane should not show other plan's question, got: %q", pane)
	}
	if strings.Contains(pane, "qb1") {
		t.Fatalf("questions pane should not show other plan's question ID, got: %q", pane)
	}
}

// TestKitchenTUIEscExitsQuestionsMode verifies that pressing Esc in questions mode
// returns to plans mode.
func TestKitchenTUIEscExitsQuestionsMode(t *testing.T) {
	model := kitchenTUIModel{
		backend:  &fakeKitchenTUIBackend{},
		leftMode: kitchenTUILeftQuestions,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_q", Title: "Plan Q"}},
		},
		questions: []pool.Question{
			{ID: "q1", TaskID: "plan_q-t1", Question: "Shall we proceed?"},
		},
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got, ok := updated.(kitchenTUIModel)
	if !ok {
		t.Fatalf("updated model = %T, want kitchenTUIModel", updated)
	}
	if got.leftMode != kitchenTUILeftPlans {
		t.Fatalf("leftMode = %q, want kitchenTUILeftPlans after esc", got.leftMode)
	}
}

// TestKitchenTUIFooterHints verifies that the footer contains the '?' questions hint in
// plans mode and the answer/back hints in questions mode.
func TestKitchenTUIFooterHints(t *testing.T) {
	basePlans := []kitchenTUIPlanItem{
		{
			Record:   PlanRecord{PlanID: "plan_q", Title: "Plan Q", State: planStatePendingApproval},
			Progress: &PlanProgress{State: planStatePendingApproval},
		},
	}

	plansModel := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans:    basePlans,
	}
	plansFooter := plansModel.renderFooter()
	if !strings.Contains(plansFooter, "? questions") {
		t.Fatalf("plans footer missing '? questions' hint: %q", plansFooter)
	}

	questionsModel := kitchenTUIModel{
		leftMode: kitchenTUILeftQuestions,
		plans:    basePlans,
		questions: []pool.Question{
			{ID: "q1", TaskID: "plan_q-t1", Question: "Which way?"},
		},
	}
	questionsFooter := questionsModel.renderFooter()
	if !strings.Contains(questionsFooter, "a answer") {
		t.Fatalf("questions footer missing 'a answer' hint: %q", questionsFooter)
	}
	if !strings.Contains(questionsFooter, "esc back") {
		t.Fatalf("questions footer missing 'esc back' hint: %q", questionsFooter)
	}
}
