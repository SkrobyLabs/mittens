package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type fakeKitchenTUIBackend struct {
	deletedPlanID             string
	deleteCalls               int
	plans                     []PlanRecord
	questions                 []pool.Question
	submitPlanID              string
	submittedIdea             string
	submittedResearchTopic    string
	submittedImplReview       bool
	submittedAnchorRef        string
	submittedDependsOn        []string
	promotedResearchPlanID    string
	promotedLineage           string
	promotedAuto              bool
	promotedImplReview        bool
	promotePlanID             string
	requestedReviewPlanID     string
	requestReviewCalls        int
	remediatedReviewPlanID    string
	remediateIncludeNits      bool
	remediateReviewCalls      int
	retriedTaskID             string
	retriedRequireFreshWorker bool
	retryCalls                int
	answeredQuestionID        string
	answeredQuestionAnswer    string
	taskOutputs               map[string]string
	taskOutputErr             error
	taskOutputCalls           int
	mergeCheckedLineage       string
	mergeCheckCalls           int
	mergedLineage             string
	mergeCalls                int
	fixMergeLineage           string
	fixMergeCalls             int
	reappliedLineage          string
	reapplyCalls              int
	cancelledPlanID           string
	cancelPlanCalls           int
	cancelledTaskID           string
	cancelTaskCalls           int
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
func (b *fakeKitchenTUIBackend) TaskOutput(taskID string) (string, error) {
	b.taskOutputCalls++
	if b.taskOutputErr != nil {
		return "", b.taskOutputErr
	}
	return b.taskOutputs[taskID], nil
}
func (b *fakeKitchenTUIBackend) ListQuestions() ([]pool.Question, error) {
	return append([]pool.Question(nil), b.questions...), nil
}
func (b *fakeKitchenTUIBackend) SubmitIdea(idea string, implReview bool, anchorRef string, dependsOn []string) (string, error) {
	b.submittedIdea = idea
	b.submittedImplReview = implReview
	b.submittedAnchorRef = anchorRef
	b.submittedDependsOn = append([]string(nil), dependsOn...)
	if strings.TrimSpace(b.submitPlanID) == "" {
		return "plan_submitted", nil
	}
	return b.submitPlanID, nil
}
func (b *fakeKitchenTUIBackend) SubmitResearch(topic string) (string, error) {
	b.submittedResearchTopic = topic
	if strings.TrimSpace(b.submitPlanID) == "" {
		return "plan_research", nil
	}
	return b.submitPlanID, nil
}
func (b *fakeKitchenTUIBackend) PromoteResearch(planID, lineage string, auto, implReview bool) (string, error) {
	b.promotedResearchPlanID = planID
	b.promotedLineage = lineage
	b.promotedAuto = auto
	b.promotedImplReview = implReview
	if strings.TrimSpace(b.promotePlanID) == "" {
		return "plan_promoted", nil
	}
	return b.promotePlanID, nil
}
func (b *fakeKitchenTUIBackend) ExtendCouncil(planID string, turns int) error { return nil }
func (b *fakeKitchenTUIBackend) RequestReview(planID string) error {
	b.requestReviewCalls++
	b.requestedReviewPlanID = planID
	return nil
}
func (b *fakeKitchenTUIBackend) RemediateReview(planID string, includeNits bool) error {
	b.remediateReviewCalls++
	b.remediatedReviewPlanID = planID
	b.remediateIncludeNits = includeNits
	return nil
}
func (b *fakeKitchenTUIBackend) ApprovePlan(planID string) error { return nil }
func (b *fakeKitchenTUIBackend) CancelPlan(planID string) error {
	b.cancelPlanCalls++
	b.cancelledPlanID = planID
	return nil
}
func (b *fakeKitchenTUIBackend) DeletePlan(planID string) error {
	b.deleteCalls++
	b.deletedPlanID = planID
	return nil
}
func (b *fakeKitchenTUIBackend) CancelTask(taskID string) error {
	b.cancelTaskCalls++
	b.cancelledTaskID = taskID
	return nil
}
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
func (b *fakeKitchenTUIBackend) MergeCheck(lineage string) (string, error) {
	b.mergeCheckCalls++
	b.mergeCheckedLineage = lineage
	return "merge-check: clean=true base=main", nil
}
func (b *fakeKitchenTUIBackend) MergeLineage(lineage string) (string, error) {
	b.mergeCalls++
	b.mergedLineage = lineage
	return "merged squash into main", nil
}
func (b *fakeKitchenTUIBackend) FixLineageConflicts(lineage string) (string, error) {
	b.fixMergeCalls++
	b.fixMergeLineage = lineage
	return "fix-123", nil
}
func (b *fakeKitchenTUIBackend) ReapplyLineage(lineage string) (string, error) {
	b.reapplyCalls++
	b.reappliedLineage = lineage
	return "reapply: reapplied from main @abc1234", nil
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

func TestKitchenTUISubmitParsesAnchorPrefix(t *testing.T) {
	backend := &fakeKitchenTUIBackend{submitPlanID: "plan_new"}
	model := kitchenTUIModel{
		backend:          backend,
		inputMode:        kitchenTUIInputSubmit,
		submitImplReview: false,
	}
	model.input = textInputWithValue("[ref=main] Add typed parser errors")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
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
	if backend.submittedIdea != "Add typed parser errors" {
		t.Fatalf("submittedIdea = %q, want parsed idea", backend.submittedIdea)
	}
	if backend.submittedAnchorRef != "main" {
		t.Fatalf("submittedAnchorRef = %q, want main", backend.submittedAnchorRef)
	}
}

func TestKitchenTUISubmitDependencySelectionTogglesPlans(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:     kitchenTUILeftTasks,
		inputMode:    kitchenTUIInputSubmit,
		input:        textInputWithValue("[ref=main] Add typed parser errors"),
		selectedPlan: 0,
		plans: []kitchenTUIPlanItem{
			{Record: PlanRecord{PlanID: "plan_alpha", Title: "Alpha", Lineage: "alpha"}},
			{Record: PlanRecord{PlanID: "plan_beta", Title: "Beta", Lineage: "beta"}},
		},
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	got := updated.(kitchenTUIModel)
	if !got.submitSelecting {
		t.Fatal("submitSelecting = false, want true")
	}
	if got.leftMode != kitchenTUILeftPlans {
		t.Fatalf("leftMode = %q, want plans", got.leftMode)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(kitchenTUIModel)
	if strings.Join(got.submitDependsOn, ",") != "plan_alpha" {
		t.Fatalf("submitDependsOn = %+v, want [plan_alpha]", got.submitDependsOn)
	}

	updated, cmd := got.Update(tea.KeyMsg{Type: tea.KeyDown})
	got = updated.(kitchenTUIModel)
	if cmd == nil {
		t.Fatal("expected reload command when moving dependency selection")
	}
	if got.selectedPlan != 1 {
		t.Fatalf("selectedPlan = %d, want 1", got.selectedPlan)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(kitchenTUIModel)
	if strings.Join(got.submitDependsOn, ",") != "plan_alpha,plan_beta" {
		t.Fatalf("submitDependsOn = %+v, want both plans selected", got.submitDependsOn)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	got = updated.(kitchenTUIModel)
	if got.submitSelecting {
		t.Fatal("submitSelecting = true, want false after leaving dependency selection")
	}
	if got.leftMode != kitchenTUILeftTasks {
		t.Fatalf("leftMode = %q, want tasks restored", got.leftMode)
	}
	if got.inputMode != kitchenTUIInputSubmit {
		t.Fatalf("inputMode = %q, want submit", got.inputMode)
	}
}

func TestKitchenTUISubmitPassesSelectedDependencies(t *testing.T) {
	backend := &fakeKitchenTUIBackend{submitPlanID: "plan_new"}
	model := kitchenTUIModel{
		backend:          backend,
		inputMode:        kitchenTUIInputSubmit,
		submitImplReview: false,
		submitDependsOn:  []string{"plan_alpha", "plan_beta"},
	}
	model.input = textInputWithValue("[ref=main] Add typed parser errors")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
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
	if strings.Join(backend.submittedDependsOn, ",") != "plan_alpha,plan_beta" {
		t.Fatalf("submittedDependsOn = %+v, want both dependencies", backend.submittedDependsOn)
	}
}

func TestKitchenTUIOpenSubmitInputPrefillsCurrentAnchorRef(t *testing.T) {
	repo := initGitRepo(t)
	model := kitchenTUIModel{
		repoPath: repo,
		input:    textinput.New(),
	}

	model.openSubmitInput()

	if !strings.HasPrefix(model.input.Value(), "[ref=main] ") {
		t.Fatalf("submit input value = %q, want main anchor prefix", model.input.Value())
	}
}

func TestKitchenTUISubmitModeToggleSwitchesToResearch(t *testing.T) {
	repo := initGitRepo(t)
	model := kitchenTUIModel{
		repoPath: repo,
		input:    textinput.New(),
	}

	model.openSubmitInput()
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	got := updated.(kitchenTUIModel)

	if !got.submitResearch {
		t.Fatal("submitResearch = false, want true after mode toggle")
	}
	if got.input.Prompt != "Submit research: " {
		t.Fatalf("prompt = %q, want research prompt", got.input.Prompt)
	}
	if strings.Contains(got.input.Value(), "[ref=") {
		t.Fatalf("submit input value = %q, want anchor prefix stripped in research mode", got.input.Value())
	}
}

func TestKitchenTUISubmitResearchCallsBackend(t *testing.T) {
	backend := &fakeKitchenTUIBackend{submitPlanID: "plan_research_new"}
	model := kitchenTUIModel{
		backend:        backend,
		inputMode:      kitchenTUIInputSubmit,
		submitResearch: true,
	}
	model.input = textInputWithValue("How does OAuth callback forwarding work?")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected submit command")
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
	if backend.submittedResearchTopic != "How does OAuth callback forwarding work?" {
		t.Fatalf("submittedResearchTopic = %q, want research topic", backend.submittedResearchTopic)
	}
	if action.selectedPlanID != "plan_research_new" {
		t.Fatalf("selectedPlanID = %q, want plan_research_new", action.selectedPlanID)
	}
}

func TestRenderCouncilTurnDiffLines(t *testing.T) {
	prev := &adapter.PlanArtifact{
		Title: "Parser cleanup",
		Tasks: []adapter.PlanArtifactTask{
			{ID: "t1", Title: "Normalize parser errors", Prompt: "Do work", Complexity: "medium", Dependencies: []string{"t0"}, Outputs: &adapter.PlanArtifactOutputs{Files: []string{"parser/errors.go"}}},
			{ID: "t2", Title: "Wire callers", Prompt: "Do work", Complexity: "medium"},
		},
	}
	curr := &adapter.PlanArtifact{
		Title: "Parser cleanup v2",
		Tasks: []adapter.PlanArtifactTask{
			{ID: "t1", Title: "Normalize typed parser errors", Prompt: "Do work", Complexity: "medium", Dependencies: []string{"t0", "t2"}, Outputs: &adapter.PlanArtifactOutputs{Files: []string{"parser/errors.go", "cmd/kitchen/capabilities.go"}}},
			{ID: "t2", Title: "Wire callers", Prompt: "Do work", Complexity: "medium"},
			{ID: "t3", Title: "Add scroll support", Prompt: "Do work", Complexity: "low"},
		},
	}

	t.Run("turn one has no diff lines", func(t *testing.T) {
		lines := renderCouncilTurnDiffLines(nil, curr, 80, 12)
		if len(lines) != 0 {
			t.Fatalf("lines = %+v, want no diff for initial turn", lines)
		}
	})

	t.Run("changed turn shows key diff lines", func(t *testing.T) {
		lines := renderCouncilTurnDiffLines(prev, curr, 80, 12)
		joined := strings.Join(lines, "\n")
		for _, want := range []string{
			"title: Parser cleanup -> Parser cleanup v2",
			"task count delta: +1",
			"added task: Add scroll support",
			"renamed task t1: Normalize parser errors -> Normalize typed parser errors",
			"Normalize typed parser errors deps: t0 -> t0, t2",
			"Normalize typed parser errors files: +cmd/kitchen/capabilities.go",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("diff output missing %q:\n%s", want, joined)
			}
		}
	})

	t.Run("non equal prompt only change still surfaces diff", func(t *testing.T) {
		promptOnly := &adapter.PlanArtifact{
			Title: "Parser cleanup",
			Tasks: []adapter.PlanArtifactTask{
				{ID: "t1", Title: "Normalize parser errors", Prompt: "Do different work", Complexity: "medium", Dependencies: []string{"t0"}, Outputs: &adapter.PlanArtifactOutputs{Files: []string{"parser/errors.go"}}},
				{ID: "t2", Title: "Wire callers", Prompt: "Do work", Complexity: "medium"},
			},
		}
		lines := renderCouncilTurnDiffLines(prev, promptOnly, 80, 12)
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "Normalize parser errors prompt updated") {
			t.Fatalf("diff output missing prompt update:\n%s", joined)
		}
	})

	t.Run("budget trimming keeps overflow marker", func(t *testing.T) {
		lines := renderCouncilTurnDiffLines(prev, curr, 80, 3)
		if len(lines) != 3 {
			t.Fatalf("line count = %d, want 3", len(lines))
		}
		if !strings.Contains(lines[2], "more changes") {
			t.Fatalf("last line = %q, want overflow marker", lines[2])
		}
	})

	t.Run("structurally equal turn shows no change", func(t *testing.T) {
		equal := &adapter.PlanArtifact{
			Title: "Parser cleanup",
			Tasks: []adapter.PlanArtifactTask{
				{ID: "t1", Title: "Normalize parser errors", Prompt: "Do work", Complexity: "medium", Dependencies: []string{"t0"}, Outputs: &adapter.PlanArtifactOutputs{Files: []string{"parser/errors.go"}}},
				{ID: "t2", Title: "Wire callers", Prompt: "Do work", Complexity: "medium"},
			},
		}
		lines := renderCouncilTurnDiffLines(prev, equal, 80, 12)
		if len(lines) != 1 || !strings.Contains(lines[0], "no change") {
			t.Fatalf("lines = %+v, want no-change marker", lines)
		}
	})
}

func TestRenderPlanDetailLinesIncludesCouncilTurnDiffs(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID:  "plan_council",
			Title:   "Parser cleanup",
			Summary: "Keep council changes visible.",
			Lineage: "parser-cleanup",
			State:   planStateReviewing,
			Anchor:  PlanAnchor{Branch: "main", Commit: "abcdef1234567890"},
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 2,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{
				{
					Seat: "A",
					Turn: 1,
					Artifact: &adapter.CouncilTurnArtifact{
						Seat:    "A",
						Turn:    1,
						Stance:  "propose",
						Summary: "Initial proposal.",
						CandidatePlan: &adapter.PlanArtifact{
							Title: "Parser cleanup",
							Tasks: []adapter.PlanArtifactTask{
								{ID: "t1", Title: "Normalize parser errors", Prompt: "Do work", Complexity: "medium"},
							},
						},
					},
				},
				{
					Seat: "B",
					Turn: 2,
					Artifact: &adapter.CouncilTurnArtifact{
						Seat:    "B",
						Turn:    2,
						Stance:  "revise",
						Summary: "Adds one missing dependency.",
						CandidatePlan: &adapter.PlanArtifact{
							Title: "Parser cleanup v2",
							Tasks: []adapter.PlanArtifactTask{
								{ID: "t1", Title: "Normalize typed parser errors", Prompt: "Do work", Complexity: "medium", Dependencies: []string{"t0"}},
								{ID: "t2", Title: "Wire callers", Prompt: "Do work", Complexity: "low"},
							},
						},
					},
				},
			},
		},
		Progress: PlanProgress{
			PlanID: "plan_council",
			Phase:  "planning",
		},
	}
	model := kitchenTUIModel{detail: detail}

	lines := model.renderPlanDetailLines(80)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Turn 2 [B] revise",
		"title: Parser cleanup -> Parser cleanup v2",
		"added task: Wire callers",
		"Normalize typed parser errors deps: - -> t0",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail output missing %q:\n%s", want, joined)
		}
	}
}

func TestRenderPlanDetailLinesLabelsAdoptedPriorPlanCouncilTurn(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID:  "plan_council_adopted",
			Title:   "Parser cleanup",
			Summary: "Keep adoption visible.",
			Lineage: "parser-cleanup",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 2,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{
				{
					Seat: "A",
					Turn: 1,
					Artifact: &adapter.CouncilTurnArtifact{
						Seat:    "A",
						Turn:    1,
						Stance:  "propose",
						Summary: "Initial proposal.",
						CandidatePlan: &adapter.PlanArtifact{
							Title: "Parser cleanup",
							Tasks: []adapter.PlanArtifactTask{{ID: "t1", Title: "Normalize parser errors", Prompt: "Do work", Complexity: "medium"}},
						},
					},
				},
				{
					Seat: "B",
					Turn: 2,
					Artifact: &adapter.CouncilTurnArtifact{
						Seat:             "B",
						Turn:             2,
						Stance:           "converged",
						AdoptedPriorPlan: true,
						Summary:          "Adopts the prior plan.",
						CandidatePlan:    nil,
					},
				},
			},
		},
		Progress: PlanProgress{
			PlanID: "plan_council_adopted",
			Phase:  "planning",
		},
	}
	model := kitchenTUIModel{detail: detail}

	joined := strings.Join(model.renderPlanDetailLines(80), "\n")
	if !strings.Contains(joined, "Seat B adopted prior plan (no changes)") {
		t.Fatalf("detail output missing adopted-prior-plan label:\n%s", joined)
	}
	if strings.Contains(joined, "no change (auto-converge eligible)") {
		t.Fatalf("detail output should not use generic no-change label for adopted plan:\n%s", joined)
	}
}

func TestRenderPlanDetailLinesShowsBlockingPlanDependencies(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID:  "plan_waiting",
			Title:   "Waiting plan",
			Summary: "Blocked on upstream plans.",
			Lineage: "dep-lineage",
			State:   planStateWaitingOnDependency,
		},
		Execution: ExecutionRecord{
			State: planStateWaitingOnDependency,
		},
		Progress: PlanProgress{
			PlanID:    "plan_waiting",
			Phase:     planStateWaitingOnDependency,
			DependsOn: []string{"plan_alpha", "plan_beta"},
		},
	}
	model := kitchenTUIModel{detail: detail}

	joined := strings.Join(model.renderPlanDetailLines(80), "\n")
	if !strings.Contains(joined, "Depends on: plan_alpha, plan_beta") {
		t.Fatalf("detail output missing dependency list:\n%s", joined)
	}
}

func TestRenderPlanDetailLinesShowsResearchMetadata(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID:  "plan_research",
			Title:   "Research OAuth callbacks",
			Summary: "Investigate callback forwarding.",
			Mode:    "research",
			State:   planStateResearchComplete,
		},
		Execution: ExecutionRecord{
			State:          planStateResearchComplete,
			ResearchOutput: "The callback is intercepted by the broker and forwarded to the initiating container.",
		},
		Progress: PlanProgress{
			PlanID: "plan_research",
			Phase:  planStateResearchComplete,
		},
	}
	model := kitchenTUIModel{detail: detail}

	joined := strings.Join(model.renderPlanDetailLines(80), "\n")
	for _, want := range []string{
		"Mode: research",
		"Research output:",
		"forwarded to the initiating",
		"container.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail output missing %q:\n%s", want, joined)
		}
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

func TestKitchenTUISubmitKeyOpensWithImplReviewEnabled(t *testing.T) {
	model := kitchenTUIModel{input: textInputWithValue("")}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputSubmit {
		t.Fatalf("inputMode = %q, want submit", got.inputMode)
	}
	if !got.submitImplReview {
		t.Fatal("submitImplReview = false, want true when opening submit")
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

func TestKitchenTUILoadCmdSkipsTaskOutputOutsideDetailPane(t *testing.T) {
	backend := &fakeKitchenTUIBackend{
		plans: []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		taskOutputs: map[string]string{
			"plan_a-t1": "final output",
		},
	}
	model := kitchenTUIModel{
		backend:      backend,
		leftMode:     kitchenTUILeftTasks,
		taskPaneMode: kitchenTUITaskPaneLogs,
		plans:        []kitchenTUIPlanItem{{Record: PlanRecord{PlanID: "plan_a", Title: "A"}}},
		tasks:        []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
	}

	msg := model.loadCmd()()
	loaded, ok := msg.(kitchenTUILoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUILoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loaded err = %v", loaded.err)
	}
	if backend.taskOutputCalls != 0 {
		t.Fatalf("taskOutputCalls = %d, want 0", backend.taskOutputCalls)
	}
}

func TestKitchenTUILoadCmdSwallowsMissingTaskOutput(t *testing.T) {
	backend := &fakeKitchenTUIBackend{
		plans:         []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		taskOutputErr: fmt.Errorf("read task output: %w", os.ErrNotExist),
	}
	model := kitchenTUIModel{
		backend:      backend,
		leftMode:     kitchenTUILeftTasks,
		taskPaneMode: kitchenTUITaskPaneDetail,
		plans:        []kitchenTUIPlanItem{{Record: PlanRecord{PlanID: "plan_a", Title: "A"}}},
		tasks:        []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
	}

	msg := model.loadCmd()()
	loaded, ok := msg.(kitchenTUILoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUILoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loaded err = %v, want swallowed not-exist", loaded.err)
	}
	if loaded.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want empty", loaded.taskOutput)
	}
	updated, _ := model.Update(loaded)
	got := updated.(kitchenTUIModel)
	if got.errText != "" {
		t.Fatalf("errText = %q, want empty", got.errText)
	}
}

func TestKitchenTUILoadCmdLoadsTaskOutputInDetailPane(t *testing.T) {
	backend := &fakeKitchenTUIBackend{
		plans: []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		taskOutputs: map[string]string{
			"plan_a-t1": "loaded final output",
		},
	}
	model := kitchenTUIModel{
		backend:      backend,
		leftMode:     kitchenTUILeftTasks,
		taskPaneMode: kitchenTUITaskPaneDetail,
		plans:        []kitchenTUIPlanItem{{Record: PlanRecord{PlanID: "plan_a", Title: "A"}}},
		tasks:        []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
	}

	msg := model.loadCmd()()
	loaded, ok := msg.(kitchenTUILoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUILoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loaded err = %v", loaded.err)
	}
	if backend.taskOutputCalls != 1 {
		t.Fatalf("taskOutputCalls = %d, want 1", backend.taskOutputCalls)
	}
	if loaded.taskOutput != "loaded final output" {
		t.Fatalf("taskOutput = %q, want loaded final output", loaded.taskOutput)
	}
}

func TestKitchenTUILoadCmdKeepsRefreshAliveOnTaskOutputError(t *testing.T) {
	backend := &fakeKitchenTUIBackend{
		plans:         []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		taskOutputErr: fmt.Errorf("output endpoint failed"),
	}
	model := kitchenTUIModel{
		backend:      backend,
		leftMode:     kitchenTUILeftTasks,
		taskPaneMode: kitchenTUITaskPaneDetail,
		plans:        []kitchenTUIPlanItem{{Record: PlanRecord{PlanID: "plan_a", Title: "A"}}},
		tasks:        []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
	}

	msg := model.loadCmd()()
	loaded, ok := msg.(kitchenTUILoadedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUILoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loaded err = %v, want degraded task output error", loaded.err)
	}
	if loaded.taskOutputErr == nil || !strings.Contains(loaded.taskOutputErr.Error(), "output endpoint failed") {
		t.Fatalf("taskOutputErr = %v, want preserved task output error", loaded.taskOutputErr)
	}
	if loaded.detail == nil || loaded.detail.Plan.PlanID != "plan_a" {
		t.Fatalf("detail = %+v, want loaded plan detail", loaded.detail)
	}
}

func TestKitchenTUILoadedMsgClearsTaskOutputOutsideTasks(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:          kitchenTUILeftPlans,
		taskOutput:        "stale output",
		taskOutputLoading: true,
	}

	updated, _ := model.Update(kitchenTUILoadedMsg{
		status: tuiStatusSnapshot{},
		plans:  []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		detail: &PlanDetail{Plan: PlanRecord{PlanID: "plan_a", Title: "A"}},
	})
	got := updated.(kitchenTUIModel)
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want cleared", got.taskOutput)
	}
	if got.taskOutputLoading {
		t.Fatal("taskOutputLoading = true, want cleared")
	}
}

func TestKitchenTUILoadedMsgTaskOutputErrorDoesNotAbortStateUpdate(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:          kitchenTUILeftTasks,
		taskPaneMode:      kitchenTUITaskPaneDetail,
		taskOutputLoading: true,
	}

	updated, _ := model.Update(kitchenTUILoadedMsg{
		status:                tuiStatusSnapshot{},
		plans:                 []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		detail:                &PlanDetail{Plan: PlanRecord{PlanID: "plan_a", Title: "A", Tasks: []PlanTask{{ID: "t1", Title: "Task 1"}}}},
		selectedTaskRuntimeID: "plan_a-t1",
		taskOutputTaskID:      "plan_a-t1",
		taskOutputErr:         fmt.Errorf("backend unavailable"),
	})
	got := updated.(kitchenTUIModel)
	if got.detail == nil || got.detail.Plan.PlanID != "plan_a" {
		t.Fatalf("detail = %+v, want loaded detail despite task output error", got.detail)
	}
	if !strings.Contains(got.errText, "task output: backend unavailable") {
		t.Fatalf("errText = %q, want task output warning", got.errText)
	}
	if got.taskOutputLoading {
		t.Fatal("taskOutputLoading = true, want cleared after response")
	}
}

func TestKitchenLocalBackendTaskOutput(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("KITCHEN_HOME", t.TempDir())
	paths, err := DefaultKitchenPaths()
	if err != nil {
		t.Fatalf("DefaultKitchenPaths: %v", err)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatalf("Ensure paths: %v", err)
	}
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatalf("Ensure project: %v", err)
	}
	taskID := "task_local_output"
	outputDir := filepath.Join(project.PoolsDir, defaultPoolStateName, "outputs")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("MkdirAll outputs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, taskID+".txt"), []byte("local backend output"), 0o644); err != nil {
		t.Fatalf("WriteFile output: %v", err)
	}

	backend := &kitchenLocalBackend{repoPath: repo}
	output, err := backend.TaskOutput(taskID)
	if err != nil {
		t.Fatalf("TaskOutput: %v", err)
	}
	if output != "local backend output" {
		t.Fatalf("output = %q, want local backend output", output)
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
	if !strings.Contains(footer, "PgUp/PgDn scroll") {
		t.Fatalf("footer = %q, want scroll hint", footer)
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
	if strings.Contains(footer, "C cancel") {
		t.Fatalf("footer = %q, did not expect cancel action for completed task", footer)
	}
}

func TestKitchenTUICancelShortcutRequiresShiftForPlans(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{{
			Record: PlanRecord{PlanID: "plan_cancel", Title: "Cancelable", State: planStateActive},
		}},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	got := updated.(kitchenTUIModel)
	if cmd != nil {
		t.Fatal("lowercase c unexpectedly produced a command")
	}
	if backend.cancelPlanCalls != 0 {
		t.Fatalf("cancel plan calls = %d, want 0", backend.cancelPlanCalls)
	}

	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	got = updated.(kitchenTUIModel)
	if cmd == nil {
		t.Fatal("uppercase C did not produce a cancel command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd message = %#v, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("cancel action err = %v", action.err)
	}
	if backend.cancelPlanCalls != 1 || backend.cancelledPlanID != "plan_cancel" {
		t.Fatalf("cancel plan calls = %d id = %q, want 1 and %q", backend.cancelPlanCalls, backend.cancelledPlanID, "plan_cancel")
	}
}

func TestKitchenTUICancelShortcutRequiresShiftForTasks(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{{
			ID:        "t1",
			RuntimeID: "plan_cancel-t1",
			Title:     "Cancelable task",
			State:     pool.TaskDispatched,
		}},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	got := updated.(kitchenTUIModel)
	if cmd != nil {
		t.Fatal("lowercase c unexpectedly produced a command")
	}
	if backend.cancelTaskCalls != 0 {
		t.Fatalf("cancel task calls = %d, want 0", backend.cancelTaskCalls)
	}

	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	if cmd == nil {
		t.Fatal("uppercase C did not produce a cancel command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd message = %#v, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("cancel action err = %v", action.err)
	}
	if backend.cancelTaskCalls != 1 || backend.cancelledTaskID != "plan_cancel-t1" {
		t.Fatalf("cancel task calls = %d id = %q, want 1 and %q", backend.cancelTaskCalls, backend.cancelledTaskID, "plan_cancel-t1")
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
	if !strings.Contains(footer, "PgUp/PgDn scroll") {
		t.Fatalf("footer = %q, want scroll hint", footer)
	}
	if !strings.Contains(footer, "p replan") || !strings.Contains(footer, "D delete") {
		t.Fatalf("footer = %q, want actionable plan actions", footer)
	}
	if strings.Contains(footer, "R retry") || strings.Contains(footer, "M merge") {
		t.Fatalf("footer = %q, did not expect unavailable actions", footer)
	}
}

func TestKitchenTUIFooterShowsPromoteForCompletedResearchPlan(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_research", Title: "Research OAuth", Mode: "research", State: planStateResearchComplete},
				Progress: &PlanProgress{State: planStateResearchComplete},
			},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "P promote") {
		t.Fatalf("footer = %q, want promote action for completed research", footer)
	}
}

func TestKitchenTUIFooterShowsPromoteForFlattenedCompletedResearchPlan(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_research", Title: "Research OAuth", Mode: "research", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "P promote") {
		t.Fatalf("footer = %q, want promote action for flattened completed research", footer)
	}
}

func TestKitchenTUIPromoteKeyOpensPromoteInput(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_research", Title: "Research OAuth", Mode: "research", State: planStateResearchComplete},
				Progress: &PlanProgress{State: planStateResearchComplete},
			},
		},
		input: textinput.New(),
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputPromote {
		t.Fatalf("inputMode = %q, want promote", got.inputMode)
	}
	if got.promotePlanID != "plan_research" {
		t.Fatalf("promotePlanID = %q, want plan_research", got.promotePlanID)
	}
}

func TestKitchenTUIPromoteResearchCallsBackend(t *testing.T) {
	backend := &fakeKitchenTUIBackend{promotePlanID: "plan_promoted_new"}
	model := kitchenTUIModel{
		backend:       backend,
		inputMode:     kitchenTUIInputPromote,
		promotePlanID: "plan_research",
	}
	model.input = textInputWithValue("oauth-direct-forward")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected promote command")
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
	if backend.promotedResearchPlanID != "plan_research" {
		t.Fatalf("promotedResearchPlanID = %q, want plan_research", backend.promotedResearchPlanID)
	}
	if backend.promotedLineage != "oauth-direct-forward" {
		t.Fatalf("promotedLineage = %q, want oauth-direct-forward", backend.promotedLineage)
	}
	if !backend.promotedImplReview {
		t.Fatal("expected promoted impl review to default to true")
	}
	if action.selectedPlanID != "plan_promoted_new" {
		t.Fatalf("selectedPlanID = %q, want promoted plan id", action.selectedPlanID)
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
	if !strings.Contains(footer, "v review") {
		t.Fatalf("footer = %q, want review action for completed plan", footer)
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
	if !strings.Contains(footer, "v review") {
		t.Fatalf("footer = %q, want review action after failed impl review", footer)
	}
}

func TestKitchenTUIReviewKeyRequestsReviewForSelectedPlan(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:  backend,
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_review", Title: "Review", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd == nil {
		t.Fatal("expected review command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if action.status != "review requested plan_review" {
		t.Fatalf("action status = %q", action.status)
	}
	if action.selectedPlanID != "plan_review" {
		t.Fatalf("selectedPlanID = %q, want plan_review", action.selectedPlanID)
	}
	if backend.requestReviewCalls != 1 || backend.requestedReviewPlanID != "plan_review" {
		t.Fatalf("backend review = %+v", backend)
	}
}

func TestKitchenTUIFooterShowsManualRemediationForPassedReviewFindings(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{{
			Record: PlanRecord{PlanID: "plan_fix_review", Title: "Fix review", State: planStateCompleted},
			Progress: &PlanProgress{
				State:               planStateCompleted,
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
				ImplReviewFollowups: []string{"[minor] add a regression test"},
			},
		}},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "f remediate") {
		t.Fatalf("footer = %q, want remediate action for passed review findings", footer)
	}
}

func TestKitchenTUIRemediationMenuQueuesSelectedScope(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:   backend,
		leftMode:  kitchenTUILeftPlans,
		inputMode: kitchenTUIInputRemediate,
		plans: []kitchenTUIPlanItem{{
			Record: PlanRecord{PlanID: "plan_fix_review", Title: "Fix review", State: planStateCompleted},
			Progress: &PlanProgress{
				State:               planStateCompleted,
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
				ImplReviewFollowups: []string{"[minor] add a regression test", "[nit] tighten wording"},
			},
		}},
		remediateSelected: 1,
	}

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected remediation command")
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if backend.remediateReviewCalls != 1 || backend.remediatedReviewPlanID != "plan_fix_review" || !backend.remediateIncludeNits {
		t.Fatalf("backend remediation = %+v", backend)
	}
	if action.selectedPlanID != "plan_fix_review" {
		t.Fatalf("selectedPlanID = %q, want plan_fix_review", action.selectedPlanID)
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

func TestRenderTaskDetailLinesIncludesFinalOutput(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{{
			ID:        "t1",
			RuntimeID: "plan_a-t1",
			Title:     "Task 1",
			Prompt:    "Prompt body",
			Summary:   "Summary body",
		}},
		taskOutput: "Final output body that should be rendered.",
	}

	lines := model.renderTaskDetailLines(40)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Final output:") {
		t.Fatalf("detail lines missing final output header:\n%s", joined)
	}
	if !strings.Contains(joined, "Final output body that should be") {
		t.Fatalf("detail lines missing final output body:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesIncludesImplementationReviewTLDRForApprovedReview(t *testing.T) {
	planID := "plan_review_pass"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State: planStateCompleted,
				ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
					Turn: 1,
					Artifact: &adapter.ReviewCouncilTurnArtifact{
						Seat:    "A",
						Turn:    1,
						Verdict: pool.ReviewPass,
						Summary: "All requested changes look correct.",
					},
				}},
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 1),
			RuntimeID: reviewCouncilTaskID(planID, 1),
			Kind:      "implementation-review",
			Title:     "Implementation review 1",
			State:     pool.TaskCompleted,
		}},
		taskOutput: "Final output body that should still render.",
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Review TL;DR:") {
		t.Fatalf("detail lines missing review TLDR header:\n%s", joined)
	}
	if !strings.Contains(joined, "Status: approved") {
		t.Fatalf("detail lines missing approved status:\n%s", joined)
	}
	if !strings.Contains(joined, "Findings: 0") {
		t.Fatalf("detail lines missing zero findings summary:\n%s", joined)
	}
	if !strings.Contains(joined, "All requested changes look correct.") {
		t.Fatalf("detail lines missing artifact summary:\n%s", joined)
	}
	if !strings.Contains(joined, "Final output:") {
		t.Fatalf("detail lines missing final output header:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesIncludesImplementationReviewTLDRForFailedReview(t *testing.T) {
	planID := "plan_review_fail"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State: planStateImplementationReviewFailed,
				ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
					Turn: 1,
					Artifact: &adapter.ReviewCouncilTurnArtifact{
						Seat:    "B",
						Turn:    1,
						Verdict: pool.ReviewFail,
						Summary: "The implementation needs another pass.",
						Findings: []adapter.ReviewFinding{
							{ID: "f1", File: "a.go", Category: "correctness", Description: "Missing guard", Severity: "major"},
							{ID: "f2", File: "b.go", Category: "coverage", Description: "Missing test", Severity: "major"},
							{ID: "f3", File: "b.go", Category: "resilience", Description: "Retry path ignored", Severity: "minor"},
						},
						Disagreements: []adapter.CouncilDisagreement{{
							ID:       "d1",
							Severity: pool.SeverityMajor,
							Category: "correctness",
							Title:    "Mismatch",
							Impact:   "Review cannot pass yet.",
						}},
					},
				}},
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusFailed,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 1),
			RuntimeID: reviewCouncilTaskID(planID, 1),
			Kind:      "implementation-review",
			Title:     "Implementation review 1",
			State:     pool.TaskFailed,
		}},
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Status: failed") {
		t.Fatalf("detail lines missing failed status:\n%s", joined)
	}
	if !strings.Contains(joined, "Findings: 3 across 2 files") {
		t.Fatalf("detail lines missing structured finding count:\n%s", joined)
	}
	if !strings.Contains(joined, "Disagreements: 1") {
		t.Fatalf("detail lines missing disagreement count:\n%s", joined)
	}
	if !strings.Contains(joined, "The implementation needs another pass.") {
		t.Fatalf("detail lines missing summary text:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesIncludesImplementationReviewTLDRForPendingReview(t *testing.T) {
	planID := "plan_review_pending"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State: planStateImplementationReview,
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 2),
			RuntimeID: reviewCouncilTaskID(planID, 2),
			Kind:      "implementation-review",
			Title:     "Implementation review 2",
			State:     pool.TaskQueued,
		}},
		taskOutput: "Pending review output placeholder.",
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Status: pending") {
		t.Fatalf("detail lines missing pending status:\n%s", joined)
	}
	if !strings.Contains(joined, "Implementation review is pending.") {
		t.Fatalf("detail lines missing pending summary:\n%s", joined)
	}
	if !strings.Contains(joined, "Final output:") {
		t.Fatalf("detail lines missing final output header:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesUsesSelectedImplementationReviewTurn(t *testing.T) {
	planID := "plan_review_turns"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State: planStateCompleted,
				ReviewCouncilTurns: []ReviewCouncilTurnRecord{
					{
						Turn: 1,
						Artifact: &adapter.ReviewCouncilTurnArtifact{
							Seat:    "A",
							Turn:    1,
							Verdict: pool.ReviewFail,
							Summary: "Turn one found missing tests.",
							Findings: []adapter.ReviewFinding{
								{ID: "f1", File: "first.go", Category: "coverage", Description: "Missing tests", Severity: "major"},
							},
						},
					},
					{
						Turn: 2,
						Artifact: &adapter.ReviewCouncilTurnArtifact{
							Seat:    "B",
							Turn:    2,
							Verdict: pool.ReviewPass,
							Summary: "Turn two cleared the review.",
						},
					},
				},
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 1),
			RuntimeID: reviewCouncilTaskID(planID, 1),
			Kind:      "implementation-review",
			Title:     "Implementation review 1",
			State:     pool.TaskFailed,
		}},
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Status: failed") {
		t.Fatalf("detail lines should follow selected failed turn:\n%s", joined)
	}
	if !strings.Contains(joined, "Turn one found missing tests.") {
		t.Fatalf("detail lines missing selected-turn summary:\n%s", joined)
	}
	if strings.Contains(joined, "Turn two cleared the review.") {
		t.Fatalf("detail lines should not show summary from another review turn:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesDoesNotMisattributeMissingReviewTurnArtifact(t *testing.T) {
	planID := "plan_review_missing_turn"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State:                       planStateCompleted,
				ReviewCouncilTurnsCompleted: 2,
				ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
					Turn: 2,
					Artifact: &adapter.ReviewCouncilTurnArtifact{
						Seat:    "B",
						Turn:    2,
						Verdict: pool.ReviewPass,
						Summary: "Later turn passed.",
					},
				}},
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 1),
			RuntimeID: reviewCouncilTaskID(planID, 1),
			Kind:      "implementation-review",
			Title:     "Implementation review 1",
			State:     pool.TaskFailed,
			Summary:   "Selected turn summary.",
		}},
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Status: failed") {
		t.Fatalf("detail lines should follow selected task state when turn data is missing:\n%s", joined)
	}
	if strings.Contains(joined, "Status: approved") {
		t.Fatalf("detail lines should not inherit overall approved status for missing selected turn:\n%s", joined)
	}
	if strings.Contains(joined, "Later turn passed.") {
		t.Fatalf("detail lines should not show another turn's summary when selected turn data is missing:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesPlacesReviewTLDRBeforeFinalOutput(t *testing.T) {
	planID := "plan_review_order"
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: planID},
			Execution: ExecutionRecord{
				State: planStateCompleted,
				ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
					Turn: 1,
					Artifact: &adapter.ReviewCouncilTurnArtifact{
						Seat:    "A",
						Turn:    1,
						Verdict: pool.ReviewPass,
						Summary: "Summary comes first.",
					},
				}},
			},
			Progress: PlanProgress{
				ImplReviewRequested: true,
				ImplReviewStatus:    planReviewStatusPassed,
			},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        reviewCouncilTaskID(planID, 1),
			RuntimeID: reviewCouncilTaskID(planID, 1),
			Kind:      "implementation-review",
			Title:     "Implementation review 1",
			State:     pool.TaskCompleted,
			Summary:   "Task summary body.",
		}},
		taskOutput: "Final output body.",
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if strings.Index(joined, "Review TL;DR:") > strings.Index(joined, "Final output:") {
		t.Fatalf("review TLDR should render before final output:\n%s", joined)
	}
	if strings.Index(joined, "Review TL;DR:") > strings.Index(joined, "Latest summary:") {
		t.Fatalf("review TLDR should render before latest summary:\n%s", joined)
	}
}

func TestRenderTaskDetailLinesOmitsReviewTLDRForNonReviewTasks(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: "plan_generic"},
		},
		tasks: []kitchenTUITaskItem{{
			ID:        "task-1",
			RuntimeID: "plan_generic-task-1",
			Kind:      "implementation",
			Title:     "Implementation task",
			State:     pool.TaskCompleted,
		}},
		taskOutput: "Implementation output.",
	}

	lines := model.renderTaskDetailLines(80)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Review TL;DR:") {
		t.Fatalf("non-review task should not render review TLDR:\n%s", joined)
	}
}

func TestRenderPlanDetailLinesShowsAutoRemediationVisibility(t *testing.T) {
	model := kitchenTUIModel{
		detail: &PlanDetail{
			Plan: PlanRecord{
				PlanID: "plan_auto_fix_detail",
				Title:  "Auto remediation detail",
			},
			Execution: ExecutionRecord{
				State: planStateActive,
			},
			Progress: PlanProgress{
				ImplReviewRequested:          true,
				AutoRemediationActive:        true,
				AutoRemediationAttempt:       1,
				AutoRemediationTaskID:        "plan_auto_fix_detail-review-fix-r1",
				AutoRemediationSourceVerdict: pool.ReviewFail,
				AutoRemediationFindings: []string{
					"[major] cmd/kitchen/planner.go:42 correctness - Create a remediation task",
				},
			},
		},
	}

	lines := model.renderPlanDetailLines(90)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Implementation review: auto-remediating (1/2)") {
		t.Fatalf("plan detail missing auto-remediation status:\n%s", joined)
	}
	if !strings.Contains(joined, "Auto-remediation task: plan_auto_fix_detail-review-fix-r1") {
		t.Fatalf("plan detail missing remediation task:\n%s", joined)
	}
	if !strings.Contains(joined, "Source verdict: fail") {
		t.Fatalf("plan detail missing source verdict:\n%s", joined)
	}
	if !strings.Contains(joined, "Auto-remediation findings:") {
		t.Fatalf("plan detail missing remediation findings header:\n%s", joined)
	}
}

func TestRenderTaskLogLinesHighlightsHeader(t *testing.T) {
	model := kitchenTUIModel{
		tasks: []kitchenTUITaskItem{{
			Title:     "Planner cycle 1",
			ID:        "t1",
			RuntimeID: "r1",
		}},
	}

	lines := model.renderTaskLogLines(80)
	if len(lines) == 0 {
		t.Fatal("expected at least one line")
	}
	expected := paneTitle("Planner cycle 1 · activity log", true)
	if lines[0] != expected {
		t.Fatalf("first line = %q, want %q", lines[0], expected)
	}
}

func TestRenderTaskDetailLinesUsesPlainBoldHeader(t *testing.T) {
	model := kitchenTUIModel{
		tasks: []kitchenTUITaskItem{{
			Title: "Planner cycle 1",
			ID:    "t1",
		}},
	}

	lines := model.renderTaskDetailLines(80)
	if len(lines) == 0 {
		t.Fatal("expected at least one line")
	}
	expected := lipgloss.NewStyle().Bold(true).Render("Planner cycle 1")
	if lines[0] != expected {
		t.Fatalf("first line = %q, want %q", lines[0], expected)
	}
}

func TestRenderTaskLogLinesTruncatesHeaderForNarrowPane(t *testing.T) {
	longTitle := strings.Repeat("ABC", 20)
	model := kitchenTUIModel{
		tasks: []kitchenTUITaskItem{{
			Title: longTitle,
			ID:    "t1",
		}},
	}

	lines := model.renderTaskLogLines(30)
	if len(lines) == 0 {
		t.Fatal("expected at least one line")
	}
	overhead := len(paneTitle("M", true)) - 1
	if len(lines[0]) > 30+overhead {
		t.Fatalf("first line len = %d, want <= %d", len(lines[0]), 30+overhead)
	}
	if !strings.HasSuffix(strings.TrimSpace(ansi.Strip(lines[0])), "…") {
		t.Fatalf("first line = %q, want stripped header to end with ellipsis", lines[0])
	}
}

func TestRenderTaskLogLinesSanitizesControlSequences(t *testing.T) {
	model := kitchenTUIModel{
		tasks: []kitchenTUITaskItem{{
			Title:     "Planner\ncycle\x1b[2K 1",
			ID:        "t1\r\n",
			RuntimeID: "plan_a-t1\t",
		}},
		taskLog: []pool.WorkerActivityRecord{{
			RecordedAt: time.Date(2026, time.April, 14, 12, 34, 56, 0, time.UTC),
			Activity: pool.WorkerActivity{
				Kind:    "status",
				Phase:   "completed",
				Name:    "response",
				Summary: "line 1\r\n\x1b[2Jline 2\tline 3",
			},
		}},
	}

	lines := model.renderTaskLogLines(80)
	if len(lines) < 5 {
		t.Fatalf("lines = %+v, want task log rows", lines)
	}
	if got := ansi.Strip(lines[0]); strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("header = %q, want sanitized single line", got)
	}
	if got := ansi.Strip(lines[0]); !strings.Contains(got, "Planner cycle 1 · activity log") {
		t.Fatalf("header = %q, want sanitized title", got)
	}
	last := lines[len(lines)-1]
	if strings.Contains(last, "\x1b") || strings.Contains(last, "\n") || strings.Contains(last, "\r") {
		t.Fatalf("log line = %q, want control characters removed", last)
	}
	if !strings.Contains(last, "status completed response  line 1 line 2 line 3") {
		t.Fatalf("log line = %q, want flattened summary", last)
	}
}

func TestWindowAndWrapLinesKeepsStyledHeaderOnSingleLine(t *testing.T) {
	header := paneTitle("Planner cycle 1 · activity log", true)
	rendered, total := windowAndWrapLines([]string{header}, ansi.StringWidth(header), 2, 0)
	rows := strings.Split(rendered, "\n")
	if total != 1 {
		t.Fatalf("total = %d, want 1 line", total)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 padded rows", rows)
	}
	if rows[0] != header {
		t.Fatalf("first row = %q, want header preserved", rows[0])
	}
	if rows[1] != "" {
		t.Fatalf("second row = %q, want empty padding row", rows[1])
	}
}

func TestWindowAndWrapLinesCountsEmbeddedNewlines(t *testing.T) {
	rendered, total := windowAndWrapLines([]string{"one\ntwo", "three"}, 10, 3, 0)
	if total != 3 {
		t.Fatalf("total = %d, want 3 split rows", total)
	}
	rows := strings.Split(rendered, "\n")
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	if rows[0] != "one" || rows[1] != "two" || rows[2] != "three" {
		t.Fatalf("rows = %+v, want embedded newline split into separate rows", rows)
	}
}

func TestRenderTaskDetailLinesShowsLoadingIndicator(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftTasks,
		tasks: []kitchenTUITaskItem{{
			ID:        "t1",
			RuntimeID: "plan_a-t1",
			Title:     "Task 1",
			Prompt:    "Prompt body",
		}},
		taskOutputLoading: true,
		taskOutput:        "stale output that should stay hidden",
	}

	lines := model.renderTaskDetailLines(40)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Final output:\nloading...") {
		t.Fatalf("detail lines missing loading indicator:\n%s", joined)
	}
	if strings.Contains(joined, "stale output that should stay hidden") {
		t.Fatalf("detail lines still show stale output while loading:\n%s", joined)
	}
}

func TestKitchenTUIEnterTaskDetailStartsTaskOutputReload(t *testing.T) {
	model := kitchenTUIModel{
		backend:          &fakeKitchenTUIBackend{},
		leftMode:         kitchenTUILeftPlans,
		taskPaneMode:     kitchenTUITaskPaneDetail,
		rightPaneOffsets: map[string]int{},
		tasks:            []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
		taskOutput:       "stale output",
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected reload command when entering task detail")
	}
	got := updated.(kitchenTUIModel)
	if got.leftMode != kitchenTUILeftTasks {
		t.Fatalf("leftMode = %q, want tasks", got.leftMode)
	}
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want cleared before reload", got.taskOutput)
	}
	if !got.taskOutputLoading {
		t.Fatal("taskOutputLoading = false, want loading state when entering task detail")
	}
}

func TestWindowAndWrapLinesSupportsOffsets(t *testing.T) {
	lines := []string{
		"one two three four five",
		"six seven eight nine ten",
	}

	rendered, total := windowAndWrapLines(lines, 10, 2, 1)
	if total <= 2 {
		t.Fatalf("total = %d, want more than visible height", total)
	}
	rows := strings.Split(rendered, "\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	if rows[0] == "one two" {
		t.Fatalf("rows = %+v, want offset window instead of first wrapped row", rows)
	}
}

func TestWindowAndWrapLinesClampsOverscroll(t *testing.T) {
	rendered, total := windowAndWrapLines([]string{"one two three four five"}, 8, 2, 99)
	if total == 0 {
		t.Fatal("total = 0, want wrapped content")
	}
	rows := strings.Split(rendered, "\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 padded rows", rows)
	}
	if strings.TrimSpace(rows[0]) == "" {
		t.Fatalf("rows = %+v, want clamped content instead of empty overscroll window", rows)
	}
}

func TestKitchenTUIPageDownScrollsTaskDetailPane(t *testing.T) {
	model := kitchenTUIModel{
		backend:          &fakeKitchenTUIBackend{},
		width:            120,
		height:           28,
		leftMode:         kitchenTUILeftTasks,
		taskPaneMode:     kitchenTUITaskPaneDetail,
		rightPaneOffsets: map[string]int{},
		tasks: []kitchenTUITaskItem{{
			ID:        "t1",
			RuntimeID: "plan_a-t1",
			Title:     "Task 1",
			Prompt:    strings.Repeat("scroll me ", 80),
		}},
		taskOutput: strings.Repeat("final output body ", 120),
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	got := updated.(kitchenTUIModel)
	if got.rightPaneOffsets["task_detail"] <= 0 {
		t.Fatalf("rightPaneOffsets = %+v, want task_detail offset to increase", got.rightPaneOffsets)
	}
}

func TestKitchenTUITaskSelectionReloadsDetailOutput(t *testing.T) {
	model := kitchenTUIModel{
		backend:          &fakeKitchenTUIBackend{},
		leftMode:         kitchenTUILeftTasks,
		taskPaneMode:     kitchenTUITaskPaneDetail,
		rightPaneOffsets: map[string]int{},
		selectedTask:     0,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"},
			{ID: "t2", RuntimeID: "plan_a-t2", Title: "Task 2"},
		},
		taskOutput: "stale output",
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("expected reload command after selecting next task in detail pane")
	}
	got := updated.(kitchenTUIModel)
	if got.selectedTask != 1 {
		t.Fatalf("selectedTask = %d, want 1", got.selectedTask)
	}
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want cleared before reload", got.taskOutput)
	}
	if !got.taskOutputLoading {
		t.Fatal("taskOutputLoading = false, want loading state while fetching next task output")
	}
}

func TestKitchenTUILoadedMsgIgnoresStaleTaskOutputForOldRuntime(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:          kitchenTUILeftTasks,
		taskPaneMode:      kitchenTUITaskPaneDetail,
		selectedTask:      1,
		taskOutputLoading: true,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"},
			{ID: "t2", RuntimeID: "plan_a-t2", Title: "Task 2"},
		},
	}

	updated, _ := model.Update(kitchenTUILoadedMsg{
		status:                tuiStatusSnapshot{},
		plans:                 []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		detail:                &PlanDetail{Plan: PlanRecord{PlanID: "plan_a", Title: "A", Tasks: []PlanTask{{ID: "t2", Title: "Task 2"}, {ID: "t1", Title: "Task 1"}}}},
		selectedTaskRuntimeID: "plan_a-t2",
		taskOutputTaskID:      "plan_a-t1",
		taskOutput:            "stale output",
	})
	got := updated.(kitchenTUIModel)
	if got.selectedTask != 0 {
		t.Fatalf("selectedTask = %d, want reselection by runtime ID", got.selectedTask)
	}
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want stale output ignored", got.taskOutput)
	}
	if !got.taskOutputLoading {
		t.Fatal("taskOutputLoading = false, want loading to continue until the matching response arrives")
	}
}

func TestKitchenTUILoadedMsgKeepsCurrentSelectionWhenOlderResponseArrives(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:          kitchenTUILeftTasks,
		taskPaneMode:      kitchenTUITaskPaneDetail,
		selectedTask:      1,
		taskOutputLoading: true,
		tasks: []kitchenTUITaskItem{
			{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"},
			{ID: "t2", RuntimeID: "plan_a-t2", Title: "Task 2"},
		},
	}

	updated, _ := model.Update(kitchenTUILoadedMsg{
		status:                tuiStatusSnapshot{},
		plans:                 []PlanRecord{{PlanID: "plan_a", Title: "A"}},
		detail:                &PlanDetail{Plan: PlanRecord{PlanID: "plan_a", Title: "A", Tasks: []PlanTask{{ID: "t1", Title: "Task 1"}, {ID: "t2", Title: "Task 2"}}}},
		selectedTaskRuntimeID: "plan_a-t1",
		taskOutputTaskID:      "plan_a-t1",
		taskOutput:            "stale output",
		taskOutputErr:         fmt.Errorf("stale request failed"),
	})
	got := updated.(kitchenTUIModel)
	if got.selectedTask != 1 {
		t.Fatalf("selectedTask = %d, want current selection preserved", got.selectedTask)
	}
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want stale output ignored", got.taskOutput)
	}
	if got.errText != "" {
		t.Fatalf("errText = %q, want stale task output error ignored", got.errText)
	}
	if !got.taskOutputLoading {
		t.Fatal("taskOutputLoading = false, want loading to continue for the current task")
	}
}

func TestKitchenTUISwitchingPanesResetsDestinationOffset(t *testing.T) {
	model := kitchenTUIModel{
		backend:          &fakeKitchenTUIBackend{},
		leftMode:         kitchenTUILeftTasks,
		taskPaneMode:     kitchenTUITaskPaneDetail,
		rightPaneOffsets: map[string]int{"task_detail": 4, "task_log": 7},
		tasks:            []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := updated.(kitchenTUIModel)
	if got.taskPaneMode != kitchenTUITaskPaneLogs {
		t.Fatalf("taskPaneMode = %q, want logs", got.taskPaneMode)
	}
	if _, exists := got.rightPaneOffsets["task_log"]; exists {
		t.Fatalf("rightPaneOffsets = %+v, want destination task_log offset reset", got.rightPaneOffsets)
	}
	if got.rightPaneOffsets["task_detail"] != 4 {
		t.Fatalf("rightPaneOffsets = %+v, want task_detail offset preserved", got.rightPaneOffsets)
	}
}

func TestKitchenTUIReturningFromLogsStartsTaskOutputReload(t *testing.T) {
	model := kitchenTUIModel{
		backend:          &fakeKitchenTUIBackend{},
		leftMode:         kitchenTUILeftTasks,
		taskPaneMode:     kitchenTUITaskPaneLogs,
		rightPaneOffsets: map[string]int{"task_detail": 2, "task_log": 7},
		tasks:            []kitchenTUITaskItem{{ID: "t1", RuntimeID: "plan_a-t1", Title: "Task 1"}},
		taskOutput:       "stale output",
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if cmd == nil {
		t.Fatal("expected reload command when returning to detail pane")
	}
	got := updated.(kitchenTUIModel)
	if got.taskPaneMode != kitchenTUITaskPaneDetail {
		t.Fatalf("taskPaneMode = %q, want detail", got.taskPaneMode)
	}
	if got.taskOutput != "" {
		t.Fatalf("taskOutput = %q, want cleared before reload", got.taskOutput)
	}
	if !got.taskOutputLoading {
		t.Fatal("taskOutputLoading = false, want loading state when returning to detail pane")
	}
}

func TestBuildTaskItemsIncludesImplementationReviewCycle(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{PlanID: "plan_impl_review"},
		Progress: PlanProgress{
			Cycles: []PlanCycleProgress{{
				Index:               1,
				ImplReviewTaskID:    reviewCouncilTaskID("plan_impl_review", 1),
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
				ID:     reviewCouncilTaskID("plan_impl_review", 1),
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
	if items[0].RuntimeID != reviewCouncilTaskID("plan_impl_review", 1) {
		t.Fatalf("runtimeID = %q, want impl review runtime id", items[0].RuntimeID)
	}
}

func TestBuildTaskItemsOrdersPlanningImplementationAndReviewPhases(t *testing.T) {
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID: "plan_phase_order",
			Tasks: []PlanTask{
				{ID: "task-b", Title: "Task B", Complexity: ComplexityMedium},
				{ID: "task-a", Title: "Task A", Complexity: ComplexityLow},
			},
		},
		Progress: PlanProgress{
			Cycles: []PlanCycleProgress{
				{
					Index:            1,
					PlannerTaskID:    "plan_phase_order-council-1",
					PlannerTaskState: pool.TaskCompleted,
				},
				{
					Index:               1,
					ImplReviewTaskID:    reviewCouncilTaskID("plan_phase_order", 1),
					ImplReviewTaskState: pool.TaskQueued,
				},
			},
		},
		History: []PlanHistoryEntry{{
			Type: planHistoryImplReviewRequested,
		}},
	}

	items := buildTaskItems(detail, tuiStatusSnapshot{})
	if len(items) != 4 {
		t.Fatalf("items = %+v, want planner, two implementation tasks, and implementation review", items)
	}

	gotKinds := []string{items[0].Kind, items[1].Kind, items[2].Kind, items[3].Kind}
	wantKinds := []string{"planner", "implementation", "implementation", "implementation-review"}
	if strings.Join(gotKinds, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("kinds = %v, want %v", gotKinds, wantKinds)
	}

	gotRuntimeIDs := []string{items[1].RuntimeID, items[2].RuntimeID, items[3].RuntimeID}
	wantRuntimeIDs := []string{
		planTaskRuntimeID("plan_phase_order", "task-b"),
		planTaskRuntimeID("plan_phase_order", "task-a"),
		reviewCouncilTaskID("plan_phase_order", 1),
	}
	if strings.Join(gotRuntimeIDs, ",") != strings.Join(wantRuntimeIDs, ",") {
		t.Fatalf("runtimeIDs = %v, want %v", gotRuntimeIDs, wantRuntimeIDs)
	}
}

func TestBuildTaskItemsInterleavesRepeatedImplementationAndReviewRounds(t *testing.T) {
	planID := "plan_rounds"
	taskOneRuntimeID := planTaskRuntimeID(planID, "task-1")
	taskTwoRuntimeID := planTaskRuntimeID(planID, "task-2")
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID: planID,
			Tasks: []PlanTask{
				{ID: "task-1", Title: "Task 1", Complexity: ComplexityMedium},
				{ID: "task-2", Title: "Task 2", Complexity: ComplexityLow},
			},
		},
		Progress: PlanProgress{
			Cycles: []PlanCycleProgress{
				{
					Index:               1,
					ImplReviewTaskID:    reviewCouncilTaskID(planID, 1),
					ImplReviewTaskState: pool.TaskFailed,
				},
				{
					Index:               2,
					ImplReviewTaskID:    reviewCouncilTaskID(planID, 2),
					ImplReviewTaskState: pool.TaskQueued,
				},
			},
		},
		History: []PlanHistoryEntry{
			{Type: planHistoryImplReviewRequested},
			{Type: planHistoryImplReviewFailed, Cycle: 1, TaskID: reviewCouncilTaskID(planID, 1)},
			{Type: planHistoryManualRetried, TaskID: taskOneRuntimeID},
			{Type: planHistoryImplReviewRequested},
			{Type: planHistoryReviewCouncilStarted},
		},
	}

	items := buildTaskItems(detail, tuiStatusSnapshot{})
	if len(items) != 6 {
		t.Fatalf("items = %+v, want two implementation blocks and two implementation reviews", items)
	}

	gotKinds := []string{
		items[0].Kind,
		items[1].Kind,
		items[2].Kind,
		items[3].Kind,
		items[4].Kind,
		items[5].Kind,
	}
	wantKinds := []string{
		"implementation",
		"implementation",
		"implementation-review",
		"implementation",
		"implementation",
		"implementation-review",
	}
	if strings.Join(gotKinds, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("kinds = %v, want %v", gotKinds, wantKinds)
	}

	gotRuntimeIDs := []string{
		items[0].RuntimeID,
		items[1].RuntimeID,
		items[2].RuntimeID,
		items[3].RuntimeID,
		items[4].RuntimeID,
		items[5].RuntimeID,
	}
	wantRuntimeIDs := []string{
		taskOneRuntimeID,
		taskTwoRuntimeID,
		reviewCouncilTaskID(planID, 1),
		taskOneRuntimeID,
		taskTwoRuntimeID,
		reviewCouncilTaskID(planID, 2),
	}
	if strings.Join(gotRuntimeIDs, ",") != strings.Join(wantRuntimeIDs, ",") {
		t.Fatalf("runtimeIDs = %v, want %v", gotRuntimeIDs, wantRuntimeIDs)
	}

	if items[0].RowKey == items[3].RowKey {
		t.Fatalf("row keys = %q and %q, want round-specific task row identity", items[0].RowKey, items[3].RowKey)
	}
}

func TestBuildTaskItemsPlacesLineageFixMergeTasksAfterReviewTimeline(t *testing.T) {
	planID := "plan_fix_timeline"
	fixTaskID := "fix-merge-20260413T225759"
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID: planID,
			Tasks: []PlanTask{
				{ID: "t1", Title: "Refresh docs", Complexity: ComplexityMedium},
				{ID: fixTaskID, Title: "Fix fix-stale-docs→feat/kitchen-headless merge conflicts", Complexity: ComplexityMedium},
			},
		},
		Progress: PlanProgress{
			Cycles: []PlanCycleProgress{
				{
					Index:            1,
					PlannerTaskID:    councilTaskID(planID, 1),
					PlannerTaskState: pool.TaskCompleted,
				},
				{
					Index:            2,
					PlannerTaskID:    councilTaskID(planID, 2),
					PlannerTaskState: pool.TaskCompleted,
				},
				{
					Index:               1,
					ImplReviewTaskID:    reviewCouncilTaskID(planID, 1),
					ImplReviewTaskState: pool.TaskCompleted,
				},
				{
					Index:               2,
					ImplReviewTaskID:    reviewCouncilTaskID(planID, 2),
					ImplReviewTaskState: pool.TaskCompleted,
				},
			},
		},
		History: []PlanHistoryEntry{
			{Type: planHistoryImplReviewRequested},
			{Type: planHistoryImplReviewPassed, Cycle: 1, TaskID: reviewCouncilTaskID(planID, 1)},
			{Type: planHistoryImplReviewRequested},
			{Type: planHistoryImplReviewPassed, Cycle: 2, TaskID: reviewCouncilTaskID(planID, 2)},
			{Type: planHistoryLineageFixMergeRequested, TaskID: planTaskRuntimeID(planID, fixTaskID)},
		},
	}
	snapshot := tuiStatusSnapshot{
		Queue: struct {
			AliveWorkers     int                `json:"aliveWorkers"`
			MaxWorkers       int                `json:"maxWorkers"`
			PendingQuestions int                `json:"pendingQuestions"`
			Tasks            []pool.TaskSummary `json:"tasks"`
		}{
			Tasks: []pool.TaskSummary{
				{ID: planTaskRuntimeID(planID, "t1"), Status: pool.TaskCompleted},
				{ID: planTaskRuntimeID(planID, fixTaskID), Status: pool.TaskDispatched},
			},
		},
	}

	items := buildTaskItems(detail, snapshot)
	gotRuntimeIDs := make([]string, 0, len(items))
	for _, item := range items {
		gotRuntimeIDs = append(gotRuntimeIDs, item.RuntimeID)
	}
	wantRuntimeIDs := []string{
		councilTaskID(planID, 1),
		councilTaskID(planID, 2),
		planTaskRuntimeID(planID, "t1"),
		reviewCouncilTaskID(planID, 1),
		reviewCouncilTaskID(planID, 2),
		planTaskRuntimeID(planID, fixTaskID),
	}
	if strings.Join(gotRuntimeIDs, ",") != strings.Join(wantRuntimeIDs, ",") {
		t.Fatalf("runtimeIDs = %v, want %v", gotRuntimeIDs, wantRuntimeIDs)
	}
	if items[len(items)-1].Kind != "implementation" || items[len(items)-1].ID != fixTaskID {
		t.Fatalf("last item = %+v, want trailing lineage fix-merge task", items[len(items)-1])
	}
}

func TestBuildTaskItemsShowsResearchTaskWithoutPlannerCycle(t *testing.T) {
	planID := "plan_research_task_items"
	researchTaskID := "research_" + planID
	detail := &PlanDetail{
		Plan: PlanRecord{
			PlanID:  planID,
			Mode:    "research",
			Title:   "Direct OAuth callback forwarding",
			Summary: "Investigate the OAuth callback flow",
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{researchTaskID},
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
				ID:     researchTaskID,
				Status: pool.TaskQueued,
			}},
		},
	}

	items := buildTaskItems(detail, snapshot)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one research task", items)
	}
	if items[0].Kind != "research" {
		t.Fatalf("kind = %q, want research", items[0].Kind)
	}
	if items[0].RuntimeID != researchTaskID {
		t.Fatalf("runtimeID = %q, want %q", items[0].RuntimeID, researchTaskID)
	}
	if items[0].Title != "Direct OAuth callback forwarding" {
		t.Fatalf("title = %q, want plan title", items[0].Title)
	}
}

func TestRenderTasksPaneUsesWholeTaskRows(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:     kitchenTUILeftTasks,
		selectedTask: 0,
		tasks: []kitchenTUITaskItem{
			{ID: "council_plan_a_t1", Title: "Planner cycle 1", Kind: "planner", State: pool.TaskCompleted},
			{ID: "council_plan_a_t2", Title: "Planner cycle 2", Kind: "planner", State: pool.TaskCompleted},
			{ID: "plan_a-t1", Title: "Implementation task", Kind: "implementation", State: pool.TaskCompleted},
			{ID: "review_council_plan_a_t1", Title: "Implementation review 1", Kind: "implementation-review", State: pool.TaskQueued},
		},
	}

	rendered := ansi.Strip(model.renderTasksPane(80, 9))
	if strings.Contains(rendered, "Implementation review 1") || strings.Contains(rendered, "review_council_plan_a_t1") {
		t.Fatalf("rendered pane = %q, want clipped tasks omitted entirely instead of partial trailing row", rendered)
	}
	if !strings.Contains(rendered, "Implementation task") || !strings.Contains(rendered, "plan_a-t1") {
		t.Fatalf("rendered pane = %q, want last fully visible task row preserved", rendered)
	}
}

func TestRenderTasksPaneKeepsSelectedTaskVisible(t *testing.T) {
	model := kitchenTUIModel{
		leftMode:     kitchenTUILeftTasks,
		selectedTask: 3,
		tasks: []kitchenTUITaskItem{
			{ID: "council_plan_a_t1", Title: "Planner cycle 1", Kind: "planner", State: pool.TaskCompleted},
			{ID: "council_plan_a_t2", Title: "Planner cycle 2", Kind: "planner", State: pool.TaskCompleted},
			{ID: "plan_a-t1", Title: "Implementation task", Kind: "implementation", State: pool.TaskCompleted},
			{ID: "review_council_plan_a_t1", Title: "Implementation review 1", Kind: "implementation-review", State: pool.TaskQueued},
		},
	}

	rendered := ansi.Strip(model.renderTasksPane(80, 9))
	if !strings.Contains(rendered, "Implementation review 1") || !strings.Contains(rendered, "review_council_plan_a_t1") {
		t.Fatalf("rendered pane = %q, want selected trailing task visible", rendered)
	}
	if strings.Contains(rendered, "Planner cycle 1") {
		t.Fatalf("rendered pane = %q, want window shifted to keep trailing selection visible", rendered)
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

func TestKitchenTUIMergeMenuOpensOnCapitalM(t *testing.T) {
	model := kitchenTUIModel{
		backend:  &fakeKitchenTUIBackend{},
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
		},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if cmd != nil {
		t.Fatalf("capital M should open menu without dispatch, got cmd %v", cmd)
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputMergeMenu {
		t.Fatalf("inputMode = %q, want merge menu", got.inputMode)
	}
	if got.mergeMenuSelected != 0 {
		t.Fatalf("mergeMenuSelected = %d, want 0", got.mergeMenuSelected)
	}
	detailPane := got.renderDetailPane(100, 20)
	if !strings.Contains(detailPane, "Merge Menu") || !strings.Contains(detailPane, "Reapply on base") {
		t.Fatalf("detail pane missing merge menu, got: %q", detailPane)
	}
}

func TestKitchenTUIPlanListMDoesNothing(t *testing.T) {
	model := kitchenTUIModel{
		backend:  &fakeKitchenTUIBackend{},
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if cmd != nil {
		t.Fatalf("lowercase m should not dispatch any action, got cmd %v", cmd)
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want none", got.inputMode)
	}
}

func TestKitchenTUIMergeMenuDispatchesReapply(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	model := kitchenTUIModel{
		backend:           backend,
		leftMode:          kitchenTUILeftPlans,
		inputMode:         kitchenTUIInputMergeMenu,
		mergeMenuSelected: 3,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
		detail: &PlanDetail{
			Plan: PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
		},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected action command for merge menu selection")
	}
	got := updated.(kitchenTUIModel)
	if got.inputMode != kitchenTUIInputNone {
		t.Fatalf("inputMode = %q, want closed merge menu", got.inputMode)
	}
	msg := cmd()
	action, ok := msg.(kitchenTUIActionMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
	}
	if action.err != nil {
		t.Fatalf("action err = %v", action.err)
	}
	if backend.reapplyCalls != 1 || backend.reappliedLineage != "parser-errors" {
		t.Fatalf("reapply backend calls = %d lineage = %q", backend.reapplyCalls, backend.reappliedLineage)
	}
	if action.status != "reapply: reapplied from main @abc1234" {
		t.Fatalf("action status = %q", action.status)
	}
}

func TestKitchenTUIMergeMenuDispatchesOtherActions(t *testing.T) {
	backend := &fakeKitchenTUIBackend{}
	cases := []struct {
		name       string
		selected   int
		wantStatus string
		verify     func(*testing.T)
	}{
		{
			name:       "check",
			selected:   0,
			wantStatus: "merge-check: clean=true base=main",
			verify: func(t *testing.T) {
				if backend.mergeCheckCalls != 1 || backend.mergeCheckedLineage != "parser-errors" {
					t.Fatalf("merge-check calls = %d lineage = %q", backend.mergeCheckCalls, backend.mergeCheckedLineage)
				}
			},
		},
		{
			name:       "merge",
			selected:   1,
			wantStatus: "merged squash into main",
			verify: func(t *testing.T) {
				if backend.mergeCalls != 1 || backend.mergedLineage != "parser-errors" {
					t.Fatalf("merge calls = %d lineage = %q", backend.mergeCalls, backend.mergedLineage)
				}
			},
		},
		{
			name:       "fix",
			selected:   2,
			wantStatus: "fix-merge task queued: fix-123",
			verify: func(t *testing.T) {
				if backend.fixMergeCalls != 1 || backend.fixMergeLineage != "parser-errors" {
					t.Fatalf("fix-merge calls = %d lineage = %q", backend.fixMergeCalls, backend.fixMergeLineage)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend.mergeCheckCalls = 0
			backend.mergeCheckedLineage = ""
			backend.mergeCalls = 0
			backend.mergedLineage = ""
			backend.fixMergeCalls = 0
			backend.fixMergeLineage = ""

			model := kitchenTUIModel{
				backend:           backend,
				leftMode:          kitchenTUILeftPlans,
				inputMode:         kitchenTUIInputMergeMenu,
				mergeMenuSelected: tc.selected,
				plans: []kitchenTUIPlanItem{
					{
						Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
						Progress: &PlanProgress{State: planStateCompleted},
					},
				},
				detail: &PlanDetail{
					Plan: PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
				},
			}

			_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd == nil {
				t.Fatal("expected action command")
			}
			msg := cmd()
			action, ok := msg.(kitchenTUIActionMsg)
			if !ok {
				t.Fatalf("cmd() returned %T, want kitchenTUIActionMsg", msg)
			}
			if action.status != tc.wantStatus {
				t.Fatalf("action status = %q, want %q", action.status, tc.wantStatus)
			}
			tc.verify(t)
		})
	}
}

func TestKitchenTUIMergeMenuNavigationMovesSelection(t *testing.T) {
	model := kitchenTUIModel{
		inputMode: kitchenTUIInputMergeMenu,
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	got := updated.(kitchenTUIModel)
	if got.mergeMenuSelected != 1 {
		t.Fatalf("mergeMenuSelected = %d, want 1", got.mergeMenuSelected)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyUp})
	got = updated.(kitchenTUIModel)
	if got.mergeMenuSelected != 0 {
		t.Fatalf("mergeMenuSelected = %d, want 0", got.mergeMenuSelected)
	}
}

func TestSummarizeReapply(t *testing.T) {
	if got := summarizeReapply(map[string]any{"status": "up-to-date", "baseBranch": "main"}); got != "reapply: up-to-date on main" {
		t.Fatalf("up-to-date summary = %q", got)
	}
	if got := summarizeReapply(map[string]any{"status": "fix-merge-queued", "baseBranch": "main", "newTaskId": "plan_fix-fix-merge-123", "conflicts": []string{"a.go", "b.go"}}); got != "reapply: fix-merge queued on main task=plan_fix-fix-merge-123 files=a.go, b.go" {
		t.Fatalf("fix-merge queued summary = %q", got)
	}
	if got := summarizeReapply(map[string]any{"status": "conflicts", "baseBranch": "main", "conflicts": []string{"a.go", "b.go"}}); got != "reapply: conflicts on main files=a.go, b.go" {
		t.Fatalf("conflicts summary = %q", got)
	}
}

func TestKitchenTUIFooterShowsMergeMenuHints(t *testing.T) {
	model := kitchenTUIModel{
		leftMode: kitchenTUILeftPlans,
		plans: []kitchenTUIPlanItem{
			{
				Record:   PlanRecord{PlanID: "plan_merge", Title: "Merge", Lineage: "parser-errors", State: planStateCompleted},
				Progress: &PlanProgress{State: planStateCompleted},
			},
		},
	}

	footer := model.renderFooter()
	if !strings.Contains(footer, "M merge-menu") {
		t.Fatalf("plans footer missing merge menu hint: %q", footer)
	}
	if strings.Contains(footer, "F fix-merge") {
		t.Fatalf("plans footer should not advertise old F fix-merge key: %q", footer)
	}

	model.inputMode = kitchenTUIInputMergeMenu
	footer = model.renderFooter()
	if !strings.Contains(footer, "↑/↓ navigate") || !strings.Contains(footer, "enter select") {
		t.Fatalf("merge menu footer missing navigation hints: %q", footer)
	}
}
