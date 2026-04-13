package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	submittedImplReview       bool
	submittedAnchorRef        string
	submittedDependsOn        []string
	retriedTaskID             string
	retriedRequireFreshWorker bool
	retryCalls                int
	answeredQuestionID        string
	answeredQuestionAnswer    string
	taskOutputs               map[string]string
	taskOutputErr             error
	taskOutputCalls           int
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
func (b *fakeKitchenTUIBackend) ExtendCouncil(planID string, turns int) error { return nil }
func (b *fakeKitchenTUIBackend) ApprovePlan(planID string) error              { return nil }
func (b *fakeKitchenTUIBackend) CancelPlan(planID string) error               { return nil }
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
	if !strings.Contains(footer, "PgUp/PgDn scroll") {
		t.Fatalf("footer = %q, want scroll hint", footer)
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
