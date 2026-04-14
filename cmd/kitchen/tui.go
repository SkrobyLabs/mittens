package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const tuiRefreshInterval = 2 * time.Second

type kitchenTUIBackend interface {
	Label() string
	Status() (tuiStatusSnapshot, error)
	ListPlans() ([]PlanRecord, error)
	PlanDetail(planID string) (PlanDetail, error)
	TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error)
	TaskOutput(taskID string) (string, error)
	ListQuestions() ([]pool.Question, error)
	SubmitIdea(idea string, implReview bool, anchorRef string, dependsOn []string) (string, error)
	SubmitResearch(topic string) (string, error)
	PromoteResearch(planID, lineage string, auto, implReview bool) (string, error)
	ExtendCouncil(planID string, turns int) error
	RequestReview(planID string) error
	RemediateReview(planID string, includeNits bool) error
	ApprovePlan(planID string) error
	CancelPlan(planID string) error
	DeletePlan(planID string) error
	CancelTask(taskID string) error
	RetryTask(taskID string, requireFreshWorker bool) error
	FixConflicts(taskID string) (string, error)
	ReplanPlan(planID, reason string) (string, error)
	AnswerQuestion(id, answer string) error
	MergeCheck(lineage string) (string, error)
	MergeLineage(lineage string) (string, error)
	FixLineageConflicts(lineage string) (string, error)
	ReapplyLineage(lineage string) (string, error)
}

type tuiStatusSnapshot struct {
	RepoPath string         `json:"repoPath"`
	Anchor   PlanAnchor     `json:"anchor"`
	Lineages []LineageState `json:"lineages"`
	Plans    []PlanProgress `json:"plans"`
	Queue    struct {
		AliveWorkers     int                `json:"aliveWorkers"`
		MaxWorkers       int                `json:"maxWorkers"`
		PendingQuestions int                `json:"pendingQuestions"`
		Tasks            []pool.TaskSummary `json:"tasks"`
	} `json:"queue"`
	Workers   []pool.Worker          `json:"workers"`
	Providers map[string]HealthEntry `json:"providers"`
}

type kitchenAPIBackend struct {
	client *kitchenAPIClient
}

func (b *kitchenAPIBackend) Label() string { return "server" }
func (b *kitchenAPIBackend) Status() (tuiStatusSnapshot, error) {
	resp, err := b.client.Status(-1)
	if err != nil {
		return tuiStatusSnapshot{}, err
	}
	return decodeTUIStatus(resp)
}
func (b *kitchenAPIBackend) ListPlans() ([]PlanRecord, error) { return b.client.ListPlans(true) }
func (b *kitchenAPIBackend) PlanDetail(planID string) (PlanDetail, error) {
	return b.client.PlanDetail(planID)
}
func (b *kitchenAPIBackend) TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error) {
	return b.client.TaskActivity(taskID)
}
func (b *kitchenAPIBackend) TaskOutput(taskID string) (string, error) {
	return b.client.TaskOutput(taskID)
}
func (b *kitchenAPIBackend) ListQuestions() ([]pool.Question, error) { return b.client.ListQuestions() }
func (b *kitchenAPIBackend) SubmitIdea(idea string, implReview bool, anchorRef string, dependsOn []string) (string, error) {
	resp, err := b.client.SubmitIdeaAt(idea, "", false, implReview, anchorRef, dependsOn...)
	if err != nil {
		return "", err
	}
	planID, _ := resp["planId"].(string)
	if strings.TrimSpace(planID) == "" {
		return "", fmt.Errorf("submit response missing planId")
	}
	return planID, nil
}
func (b *kitchenAPIBackend) SubmitResearch(topic string) (string, error) {
	resp, err := b.client.SubmitResearch(topic)
	if err != nil {
		return "", err
	}
	planID, _ := resp["planId"].(string)
	if strings.TrimSpace(planID) == "" {
		return "", fmt.Errorf("submit response missing planId")
	}
	return planID, nil
}
func (b *kitchenAPIBackend) PromoteResearch(planID, lineage string, auto, implReview bool) (string, error) {
	resp, err := b.client.PromoteResearch(planID, lineage, auto, implReview)
	if err != nil {
		return "", err
	}
	newPlanID, _ := resp["planId"].(string)
	if strings.TrimSpace(newPlanID) == "" {
		return "", fmt.Errorf("promote response missing planId")
	}
	return newPlanID, nil
}
func (b *kitchenAPIBackend) ExtendCouncil(planID string, turns int) error {
	_, err := b.client.ExtendCouncil(planID, turns)
	return err
}
func (b *kitchenAPIBackend) RequestReview(planID string) error {
	_, err := b.client.RequestReview(planID)
	return err
}
func (b *kitchenAPIBackend) RemediateReview(planID string, includeNits bool) error {
	_, err := b.client.RemediateReview(planID, includeNits)
	return err
}
func (b *kitchenAPIBackend) ApprovePlan(planID string) error {
	_, err := b.client.ApprovePlan(planID)
	return err
}
func (b *kitchenAPIBackend) CancelPlan(planID string) error {
	_, err := b.client.CancelPlan(planID)
	return err
}
func (b *kitchenAPIBackend) DeletePlan(planID string) error {
	_, err := b.client.DeletePlan(planID)
	return err
}
func (b *kitchenAPIBackend) CancelTask(taskID string) error {
	_, err := b.client.CancelTask(taskID)
	return err
}
func (b *kitchenAPIBackend) RetryTask(taskID string, requireFreshWorker bool) error {
	_, err := b.client.RetryTask(taskID, requireFreshWorker)
	return err
}
func (b *kitchenAPIBackend) FixConflicts(taskID string) (string, error) {
	resp, err := b.client.FixConflicts(taskID)
	if err != nil {
		return "", err
	}
	newTaskID, _ := resp["newTaskId"].(string)
	return newTaskID, nil
}
func (b *kitchenAPIBackend) ReplanPlan(planID, reason string) (string, error) {
	resp, err := b.client.ReplanPlan(planID, reason)
	if err != nil {
		return "", err
	}
	newPlanID, _ := resp["newPlanId"].(string)
	if strings.TrimSpace(newPlanID) == "" {
		return "", fmt.Errorf("replan response missing newPlanId")
	}
	return newPlanID, nil
}
func (b *kitchenAPIBackend) AnswerQuestion(id, answer string) error {
	_, err := b.client.AnswerQuestion(id, answer)
	return err
}
func (b *kitchenAPIBackend) MergeCheck(lineage string) (string, error) {
	resp, err := b.client.MergeCheck(lineage)
	if err != nil {
		return "", err
	}
	return summarizeMergeCheck(resp), nil
}
func (b *kitchenAPIBackend) FixLineageConflicts(lineage string) (string, error) {
	resp, err := b.client.FixLineageConflicts(lineage)
	if err != nil {
		return "", err
	}
	newTaskID, _ := resp["newTaskId"].(string)
	if strings.TrimSpace(newTaskID) == "" {
		return "", fmt.Errorf("fix-lineage-merge response missing newTaskId")
	}
	return newTaskID, nil
}

func (b *kitchenAPIBackend) MergeLineage(lineage string) (string, error) {
	resp, err := b.client.MergeLineage(lineage, false)
	if err != nil {
		return "", err
	}
	return summarizeMerge(resp), nil
}

func (b *kitchenAPIBackend) ReapplyLineage(lineage string) (string, error) {
	resp, err := b.client.ReapplyLineage(lineage)
	if err != nil {
		return "", err
	}
	return summarizeReapply(resp), nil
}

type kitchenLocalBackend struct {
	repoPath string
}

func (b *kitchenLocalBackend) Label() string { return "local" }

func (b *kitchenLocalBackend) withKitchen(fn func(*Kitchen) error) error {
	k, closeFn, err := openKitchen(b.repoPath)
	if err != nil {
		return err
	}
	defer closeFn()
	return fn(k)
}

func (b *kitchenLocalBackend) Status() (tuiStatusSnapshot, error) {
	var snapshot tuiStatusSnapshot
	err := b.withKitchen(func(k *Kitchen) error {
		resp, err := k.StatusSnapshot()
		if err != nil {
			return err
		}
		snapshot, err = decodeTUIStatus(resp)
		return err
	})
	return snapshot, err
}

func (b *kitchenLocalBackend) ListPlans() ([]PlanRecord, error) {
	var plans []PlanRecord
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		plans, err = k.ListPlans(true)
		return err
	})
	return plans, err
}

func (b *kitchenLocalBackend) PlanDetail(planID string) (PlanDetail, error) {
	var detail PlanDetail
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		detail, err = k.PlanDetail(planID)
		return err
	})
	return detail, err
}

func (b *kitchenLocalBackend) TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error) {
	var transcript []pool.WorkerActivityRecord
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		transcript, err = k.TaskActivity(taskID)
		return err
	})
	return transcript, err
}

func (b *kitchenLocalBackend) TaskOutput(taskID string) (string, error) {
	var output string
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		output, err = k.TaskOutput(taskID)
		return err
	})
	return output, err
}

func (b *kitchenLocalBackend) ListQuestions() ([]pool.Question, error) {
	var questions []pool.Question
	err := b.withKitchen(func(k *Kitchen) error {
		questions = k.ListQuestions()
		return nil
	})
	return questions, err
}

func (b *kitchenLocalBackend) SubmitIdea(idea string, implReview bool, anchorRef string, dependsOn []string) (string, error) {
	var planID string
	err := b.withKitchen(func(k *Kitchen) error {
		bundle, err := k.SubmitIdeaAt(idea, "", false, implReview, anchorRef, dependsOn...)
		if err != nil {
			return err
		}
		planID = bundle.Plan.PlanID
		return nil
	})
	return planID, err
}

func (b *kitchenLocalBackend) SubmitResearch(topic string) (string, error) {
	var planID string
	err := b.withKitchen(func(k *Kitchen) error {
		bundle, err := k.SubmitResearch(topic)
		if err != nil {
			return err
		}
		planID = bundle.Plan.PlanID
		return nil
	})
	return planID, err
}

func (b *kitchenLocalBackend) PromoteResearch(planID, lineage string, auto, implReview bool) (string, error) {
	var newPlanID string
	err := b.withKitchen(func(k *Kitchen) error {
		bundle, err := k.PromoteResearch(planID, lineage, auto, implReview)
		if err != nil {
			return err
		}
		newPlanID = bundle.Plan.PlanID
		return nil
	})
	return newPlanID, err
}

func (b *kitchenLocalBackend) ExtendCouncil(planID string, turns int) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.ExtendCouncil(planID, turns)
	})
}

func (b *kitchenLocalBackend) RequestReview(planID string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.RequestReview(planID)
	})
}
func (b *kitchenLocalBackend) RemediateReview(planID string, includeNits bool) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.RemediateReview(planID, includeNits)
	})
}

func (b *kitchenLocalBackend) ApprovePlan(planID string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.ApprovePlan(planID)
	})
}

func (b *kitchenLocalBackend) CancelPlan(planID string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.CancelPlan(planID)
	})
}

func (b *kitchenLocalBackend) DeletePlan(planID string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.DeletePlan(planID)
	})
}

func (b *kitchenLocalBackend) CancelTask(taskID string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.CancelTask(taskID)
	})
}

func (b *kitchenLocalBackend) RetryTask(taskID string, requireFreshWorker bool) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.RetryTask(taskID, requireFreshWorker)
	})
}

func (b *kitchenLocalBackend) FixConflicts(taskID string) (string, error) {
	var newTaskID string
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		newTaskID, err = k.FixConflicts(taskID)
		return err
	})
	return newTaskID, err
}

func (b *kitchenLocalBackend) ReplanPlan(planID, reason string) (string, error) {
	var newPlanID string
	err := b.withKitchen(func(k *Kitchen) error {
		var err error
		newPlanID, err = k.Replan(planID, reason)
		return err
	})
	return newPlanID, err
}

func (b *kitchenLocalBackend) AnswerQuestion(id, answer string) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.AnswerQuestion(id, answer)
	})
}

func (b *kitchenLocalBackend) MergeCheck(lineage string) (string, error) {
	var summary string
	err := b.withKitchen(func(k *Kitchen) error {
		gitMgr, err := k.gitManager()
		if err != nil {
			return err
		}
		baseBranch := k.baseBranchForLineage(lineage)
		clean, conflicts, err := gitMgr.MergeCheck(lineage, baseBranch)
		if err != nil {
			return err
		}
		summary = fmt.Sprintf("merge-check: clean=%t base=%s", clean, baseBranch)
		if len(conflicts) > 0 {
			summary += " conflicts=" + strings.Join(conflicts, ", ")
		}
		return nil
	})
	return summary, err
}

func (b *kitchenLocalBackend) FixLineageConflicts(lineage string) (string, error) {
	var newTaskID string
	err := b.withKitchen(func(k *Kitchen) error {
		var innerErr error
		newTaskID, innerErr = k.FixLineageConflicts(lineage)
		return innerErr
	})
	return newTaskID, err
}

func (b *kitchenLocalBackend) MergeLineage(lineage string) (string, error) {
	var summary string
	err := b.withKitchen(func(k *Kitchen) error {
		resp, err := k.MergeLineage(lineage)
		if err != nil {
			return err
		}
		summary = summarizeMerge(resp)
		return nil
	})
	return summary, err
}

func (b *kitchenLocalBackend) ReapplyLineage(lineage string) (string, error) {
	var summary string
	err := b.withKitchen(func(k *Kitchen) error {
		resp, err := k.ReapplyLineage(lineage)
		if err != nil {
			return err
		}
		summary = summarizeReapply(resp)
		return nil
	})
	return summary, err
}

func openKitchenTUIBackend(repoPath string) (kitchenTUIBackend, error) {
	client, ok, err := openKitchenAPIClient(repoPath)
	if err != nil {
		return nil, err
	}
	if ok {
		return &kitchenAPIBackend{client: client}, nil
	}
	return &kitchenLocalBackend{repoPath: repoPath}, nil
}

func decodeTUIStatus(src map[string]any) (tuiStatusSnapshot, error) {
	var snapshot tuiStatusSnapshot
	data, err := json.Marshal(src)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

type kitchenTUILoadedMsg struct {
	status                tuiStatusSnapshot
	plans                 []PlanRecord
	questions             []pool.Question
	detail                *PlanDetail
	selectedTaskRowKey    string
	selectedTaskRuntimeID string
	taskLogTaskID         string
	taskLog               []pool.WorkerActivityRecord
	taskOutputTaskID      string
	taskOutput            string
	taskOutputErr         error
	err                   error
	newBackend            kitchenTUIBackend
}

type kitchenTUIActionMsg struct {
	status         string
	selectedPlanID string
	err            error
}

type kitchenTUITickMsg time.Time

type kitchenTUIInputMode string
type kitchenTUILeftMode string
type kitchenTUITaskPaneMode string

const (
	kitchenTUIInputNone      kitchenTUIInputMode = ""
	kitchenTUIInputSubmit    kitchenTUIInputMode = "submit"
	kitchenTUIInputPromote   kitchenTUIInputMode = "promote"
	kitchenTUIInputReplan    kitchenTUIInputMode = "replan"
	kitchenTUIInputAnswer    kitchenTUIInputMode = "answer"
	kitchenTUIInputMergeMenu kitchenTUIInputMode = "merge-menu"
	kitchenTUIInputRemediate kitchenTUIInputMode = "remediate-review"

	kitchenTUILeftPlans     kitchenTUILeftMode = "plans"
	kitchenTUILeftTasks     kitchenTUILeftMode = "tasks"
	kitchenTUILeftQuestions kitchenTUILeftMode = "questions"

	kitchenTUITaskPaneDetail kitchenTUITaskPaneMode = "detail"
	kitchenTUITaskPaneLogs   kitchenTUITaskPaneMode = "logs"
)

type kitchenTUIPlanItem struct {
	Record   PlanRecord
	Progress *PlanProgress
	Active   bool
}

type kitchenTUITaskItem struct {
	ID          string
	RuntimeID   string
	RowKey      string
	Kind        string
	Title       string
	State       string
	WorkerID    string
	Complexity  string
	Prompt      string
	Summary     string
	HasConflict bool
}

func taskHasConflictInfo(t kitchenTUITaskItem) bool {
	return t.HasConflict
}

type kitchenTUIModel struct {
	backend           kitchenTUIBackend
	repoPath          string
	width             int
	height            int
	status            tuiStatusSnapshot
	plans             []kitchenTUIPlanItem
	tasks             []kitchenTUITaskItem
	questions         []pool.Question
	selectedPlan      int
	selectedTask      int
	selectedQuestion  int
	selectedOption    int
	mergeMenuSelected int
	remediateSelected int
	leftMode          kitchenTUILeftMode
	taskPaneMode      kitchenTUITaskPaneMode
	detail            *PlanDetail
	taskLog           []pool.WorkerActivityRecord
	taskOutput        string
	taskOutputLoading bool
	rightPaneOffsets  map[string]int
	input             textinput.Model
	inputMode         kitchenTUIInputMode
	submitImplReview  bool
	submitResearch    bool
	submitDependsOn   []string
	submitSelecting   bool
	submitPrevLeft    kitchenTUILeftMode
	promotePlanID     string
	flash             string
	flashUntil        time.Time
	errText           string
	loading           bool
	pendingSelectedID string
}

func runKitchenTUI(repoPath string) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("interactive terminal required; use kitchen status/plan/evidence in headless mode")
	}
	backend, err := openKitchenTUIBackend(repoPath)
	if err != nil {
		return err
	}
	input := textinput.New()
	input.CharLimit = 1000
	input.Prompt = "> "
	model := kitchenTUIModel{
		backend:          backend,
		repoPath:         repoPath,
		input:            input,
		loading:          true,
		leftMode:         kitchenTUILeftPlans,
		taskPaneMode:     kitchenTUITaskPaneDetail,
		rightPaneOffsets: map[string]int{},
	}
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func (m kitchenTUIModel) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), kitchenTUITick())
}

func (m kitchenTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.inputMode != kitchenTUIInputNone {
			m.input.Width = max(20, msg.Width-14)
		}
		return m, nil
	case kitchenTUILoadedMsg:
		m.loading = false
		currentSelectedTaskRowKey := ""
		currentSelectedTaskRuntimeID := ""
		if m.leftMode == kitchenTUILeftTasks {
			if task := m.selectedTaskItem(); task != nil {
				currentSelectedTaskRowKey = taskRowKey(*task)
				currentSelectedTaskRuntimeID = strings.TrimSpace(task.RuntimeID)
			}
		}
		if msg.newBackend != nil {
			m.backend = msg.newBackend
		}
		if msg.err != nil {
			m.taskOutputLoading = false
			m.errText = msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		m.plans = buildPlanItems(msg.plans, msg.status)
		m.questions = append([]pool.Question(nil), msg.questions...)
		m.syncPlanSelection()
		m.detail = msg.detail
		m.tasks = buildTaskItems(m.detail, m.status)
		selectionRowKey := currentSelectedTaskRowKey
		if selectionRowKey == "" {
			selectionRowKey = strings.TrimSpace(msg.selectedTaskRowKey)
		}
		if idx := findTaskIndexByRowKey(m.tasks, selectionRowKey); idx >= 0 {
			m.selectedTask = idx
		} else {
			selectionRuntimeID := currentSelectedTaskRuntimeID
			if selectionRuntimeID == "" {
				selectionRuntimeID = msg.selectedTaskRuntimeID
			}
			if idx := findTaskIndexByRuntimeID(m.tasks, selectionRuntimeID); idx >= 0 {
				m.selectedTask = idx
			} else if m.selectedTask >= len(m.tasks) {
				m.selectedTask = max(0, len(m.tasks)-1)
			}
		}
		if m.selectedTask >= len(m.tasks) {
			m.selectedTask = max(0, len(m.tasks)-1)
		}
		if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneLogs && runtimeIDMatchesSelectedTask(m.tasks, m.selectedTask, msg.taskLogTaskID) {
			m.taskLog = append([]pool.WorkerActivityRecord(nil), msg.taskLog...)
		} else if m.leftMode != kitchenTUILeftTasks || m.taskPaneMode != kitchenTUITaskPaneLogs {
			m.taskLog = nil
		}
		if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneDetail && runtimeIDMatchesSelectedTask(m.tasks, m.selectedTask, msg.taskOutputTaskID) {
			m.taskOutput = msg.taskOutput
			m.taskOutputLoading = false
		} else if m.leftMode != kitchenTUILeftTasks || m.taskPaneMode != kitchenTUITaskPaneDetail {
			m.taskOutput = ""
			m.taskOutputLoading = false
		}
		if m.leftMode != kitchenTUILeftTasks {
			m.taskLog = nil
			m.taskOutput = ""
			m.taskOutputLoading = false
		}
		if m.leftMode == kitchenTUILeftTasks && len(m.tasks) == 0 {
			m.leftMode = kitchenTUILeftPlans
			m.taskPaneMode = kitchenTUITaskPaneDetail
			m.taskLog = nil
			m.taskOutput = ""
			m.taskOutputLoading = false
		}
		if m.flash == "refreshing..." {
			m.flash = ""
			m.flashUntil = time.Time{}
		}
		m.errText = ""
		if msg.taskOutputErr != nil && runtimeIDMatchesSelectedTask(m.tasks, m.selectedTask, msg.taskOutputTaskID) {
			m.errText = fmt.Sprintf("task output: %v", msg.taskOutputErr)
		}
		return m, nil
	case kitchenTUIActionMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.closeInput()
		m.errText = ""
		m.flash = msg.status
		m.flashUntil = time.Now().Add(4 * time.Second)
		if strings.TrimSpace(msg.selectedPlanID) != "" {
			m.pendingSelectedID = msg.selectedPlanID
		}
		// Closing the input bar shrinks the view, and the upcoming
		// loadCmd may resize the plan list out from under the
		// previous selection. Force a full redraw so the terminal
		// doesn't retain stale lines from the taller prior frame
		// (header summary, removed plan rows, etc.).
		return m, tea.Batch(tea.ClearScreen, m.loadCmd())
	case kitchenTUITickMsg:
		if !m.flashUntil.IsZero() && time.Now().After(m.flashUntil) {
			m.flash = ""
			m.flashUntil = time.Time{}
		}
		return m, tea.Batch(m.loadCmd(), kitchenTUITick())
	case tea.KeyMsg:
		if m.inputMode == kitchenTUIInputAnswer && m.selectedQuestionItem() != nil && len(m.selectedQuestionItem().Options) > 0 {
			return m.updateMultipleChoiceInput(msg)
		}
		if m.inputMode == kitchenTUIInputMergeMenu {
			return m.updateMergeMenuInput(msg)
		}
		if m.inputMode == kitchenTUIInputRemediate {
			return m.updateRemediateMenuInput(msg)
		}
		if m.inputMode != kitchenTUIInputNone {
			return m.updateInput(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r":
			m.flash = "refreshing..."
			m.flashUntil = time.Now().Add(5 * time.Second)
			return m, m.loadCmd()
		case "up", "k":
			if m.leftMode == kitchenTUILeftTasks {
				moved := false
				if m.selectedTask > 0 {
					m.selectedTask--
					(&m).resetCurrentRightPaneOffset()
					moved = true
				}
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
					return m, m.loadCmd()
				}
				if moved {
					m.taskOutput = ""
					m.taskOutputLoading = true
					return m, m.loadCmd()
				}
				return m, nil
			}
			if m.leftMode == kitchenTUILeftQuestions {
				if m.selectedQuestion > 0 {
					m.selectedQuestion--
					(&m).resetCurrentRightPaneOffset()
				}
				return m, nil
			}
			if m.selectedPlan > 0 {
				m.selectedPlan--
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				(&m).resetCurrentRightPaneOffset()
				return m, m.loadCmd()
			}
			return m, nil
		case "down", "j":
			if m.leftMode == kitchenTUILeftTasks {
				moved := false
				if m.selectedTask < len(m.tasks)-1 {
					m.selectedTask++
					(&m).resetCurrentRightPaneOffset()
					moved = true
				}
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
					return m, m.loadCmd()
				}
				if moved {
					m.taskOutput = ""
					m.taskOutputLoading = true
					return m, m.loadCmd()
				}
				return m, nil
			}
			if m.leftMode == kitchenTUILeftQuestions {
				planID := ""
				if plan := m.selectedPlanItem(); plan != nil {
					planID = strings.TrimSpace(plan.Record.PlanID)
				}
				count := 0
				if planID != "" {
					prefix := planID + "-"
					for _, q := range m.questions {
						if strings.HasPrefix(strings.TrimSpace(q.TaskID), prefix) {
							count++
						}
					}
				}
				if m.selectedQuestion < count-1 {
					m.selectedQuestion++
					(&m).resetCurrentRightPaneOffset()
				}
				return m, nil
			}
			if m.selectedPlan < len(m.plans)-1 {
				m.selectedPlan++
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				(&m).resetCurrentRightPaneOffset()
				return m, m.loadCmd()
			}
			return m, nil
		case "enter", "right", "l":
			if m.leftMode == kitchenTUILeftPlans && len(m.tasks) > 0 {
				m.leftMode = kitchenTUILeftTasks
				m.taskPaneMode = kitchenTUITaskPaneDetail
				(&m).resetCurrentRightPaneOffset()
				m.taskOutput = ""
				m.taskOutputLoading = true
				return m, m.loadCmd()
			}
			if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneDetail && len(m.tasks) > 0 {
				m.taskPaneMode = kitchenTUITaskPaneLogs
				(&m).resetCurrentRightPaneOffset()
				return m, m.loadCmd()
			}
			return m, nil
		case "left", "h", "backspace", "esc":
			if m.leftMode == kitchenTUILeftTasks {
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
					m.taskPaneMode = kitchenTUITaskPaneDetail
					(&m).resetCurrentRightPaneOffset()
					m.taskOutput = ""
					m.taskOutputLoading = true
					return m, m.loadCmd()
				}
				m.leftMode = kitchenTUILeftPlans
				m.taskPaneMode = kitchenTUITaskPaneDetail
				(&m).resetCurrentRightPaneOffset()
			} else if m.leftMode == kitchenTUILeftQuestions {
				m.leftMode = kitchenTUILeftPlans
				(&m).resetCurrentRightPaneOffset()
			}
			return m, nil
		case "?":
			if m.leftMode == kitchenTUILeftPlans {
				m.leftMode = kitchenTUILeftQuestions
				m.selectedQuestion = 0
				(&m).resetCurrentRightPaneOffset()
			}
			return m, nil
		case "pgup":
			lines, innerWidth, _ := m.measureCurrentRightPane()
			if _, total := windowAndWrapLines(lines, innerWidth, 0, 0); total > 0 {
				(&m).setRightPaneOffset(m.rightPaneOffset() - max(1, m.pageScrollDelta()))
			}
			return m, nil
		case "pgdown":
			lines, innerWidth, _ := m.measureCurrentRightPane()
			if _, total := windowAndWrapLines(lines, innerWidth, 0, 0); total > 0 {
				(&m).setRightPaneOffset(m.rightPaneOffset() + max(1, m.pageScrollDelta()))
			}
			return m, nil
		case "home":
			(&m).setRightPaneOffset(0)
			return m, nil
		case "end":
			lines, innerWidth, height := m.measureCurrentRightPane()
			_, total := windowAndWrapLines(lines, innerWidth, 0, 0)
			maxOffset := total - height
			if maxOffset < 0 {
				maxOffset = 0
			}
			(&m).setRightPaneOffset(maxOffset)
			return m, nil
		case "n":
			m.openSubmitInput()
			return m, nil
		case "e":
			if m.leftMode != kitchenTUILeftPlans {
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				if !m.canExtendSelectedPlan() {
					m.flash = "selected plan cannot be extended"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.ExtendCouncil(plan.Record.PlanID, 2)
					return "extended council " + plan.Record.PlanID, plan.Record.PlanID, err
				})
			}
			return m, nil
		case "a":
			if m.leftMode == kitchenTUILeftQuestions {
				if q := m.selectedQuestionItem(); q != nil {
					if len(q.Options) > 0 {
						m.inputMode = kitchenTUIInputAnswer
						m.selectedOption = 0
					} else {
						m.openInput(kitchenTUIInputAnswer, "Answer", "Type your answer...")
					}
				}
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				if planDisplayState(*plan) == planStatePendingApproval {
					return m, m.actionCmd(func() (string, string, error) {
						err := m.backend.ApprovePlan(plan.Record.PlanID)
						return "approved " + plan.Record.PlanID, plan.Record.PlanID, err
					})
				}
				m.flash = "selected plan is not awaiting approval"
				m.flashUntil = time.Now().Add(4 * time.Second)
			}
			return m, nil
		case "C":
			if m.leftMode == kitchenTUILeftTasks {
				if task := m.selectedTaskItem(); task != nil {
					if !m.canCancelSelectedTask() {
						m.flash = "selected task cannot be cancelled"
						m.flashUntil = time.Now().Add(4 * time.Second)
						return m, nil
					}
					return m, m.actionCmd(func() (string, string, error) {
						err := m.backend.CancelTask(task.RuntimeID)
						return "cancelled " + task.RuntimeID, m.selectedPlanID(), err
					})
				}
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				if !m.canCancelSelectedPlan() {
					m.flash = "selected plan cannot be cancelled"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.CancelPlan(plan.Record.PlanID)
					return "cancelled " + plan.Record.PlanID, plan.Record.PlanID, err
				})
			}
			return m, nil
		case "D":
			if m.leftMode != kitchenTUILeftPlans {
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				nextPlanID := m.nextPlanSelectionAfterDeletion()
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.DeletePlan(plan.Record.PlanID)
					return "deleted " + plan.Record.PlanID, nextPlanID, err
				})
			}
			return m, nil
		case "p":
			if m.selectedPlanItem() != nil {
				m.openInput(kitchenTUIInputReplan, "Replan reason", "Need a narrower retry")
			}
			return m, nil
		case "P":
			if m.leftMode != kitchenTUILeftPlans {
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				if !m.canPromoteSelectedPlan() {
					m.flash = "selected plan cannot be promoted"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				m.openPromoteInput(plan.Record.PlanID)
			}
			return m, nil
		case "v":
			if m.leftMode != kitchenTUILeftPlans {
				return m, nil
			}
			if plan := m.selectedPlanItem(); plan != nil {
				if !m.canRequestReviewSelectedPlan() {
					m.flash = "selected plan cannot be reviewed"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.RequestReview(plan.Record.PlanID)
					return "review requested " + plan.Record.PlanID, plan.Record.PlanID, err
				})
			}
			return m, nil
		case "M":
			if m.leftMode != kitchenTUILeftPlans {
				return m, nil
			}
			if m.canMergeSelectedPlan() {
				m.inputMode = kitchenTUIInputMergeMenu
				m.mergeMenuSelected = 0
			}
			return m, nil
		case "R":
			if m.leftMode != kitchenTUILeftTasks {
				return m, nil
			}
			if task := m.selectedTaskItem(); task != nil {
				if !m.canRetrySelectedTask() {
					m.flash = "selected task cannot be retried"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.RetryTask(task.RuntimeID, true)
					return m.selectedTaskRetryStatus() + " " + task.RuntimeID, m.selectedPlanID(), err
				})
			}
			return m, nil
		case "U":
			if m.leftMode != kitchenTUILeftTasks {
				return m, nil
			}
			if task := m.selectedTaskItem(); task != nil {
				if !m.canRetrySelectedTask() {
					m.flash = "selected task cannot be retried"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					err := m.backend.RetryTask(task.RuntimeID, false)
					return m.selectedTaskRetryStatus() + " " + task.RuntimeID + " (reuse allowed)", m.selectedPlanID(), err
				})
			}
			return m, nil
		case "f":
			if m.leftMode == kitchenTUILeftPlans {
				if !m.canRemediateSelectedPlan() {
					m.flash = "selected plan has no lower-severity findings to remediate"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				m.inputMode = kitchenTUIInputRemediate
				m.remediateSelected = 0
				return m, nil
			}
			if m.leftMode != kitchenTUILeftTasks {
				return m, nil
			}
			if task := m.selectedTaskItem(); task != nil {
				if !m.canFixConflictsSelectedTask() {
					m.flash = "selected task has no conflict info"
					m.flashUntil = time.Now().Add(4 * time.Second)
					return m, nil
				}
				return m, m.actionCmd(func() (string, string, error) {
					newTaskID, err := m.backend.FixConflicts(task.RuntimeID)
					if err != nil {
						return err.Error(), m.selectedPlanID(), err
					}
					return "Conflict fix task created: " + newTaskID, m.selectedPlanID(), nil
				})
			}
			return m, nil
		}
	}
	return m, nil
}

var kitchenTUIMergeMenuOptions = []string{
	"Check merge",
	"Execute merge",
	"Fix conflicts",
	"Reapply on base",
}

var kitchenTUIRemediationOptions = []string{
	"Fix minor findings",
	"Fix minor findings and nits",
}

func (m kitchenTUIModel) updateMergeMenuInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputMode = kitchenTUIInputNone
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.mergeMenuSelected > 0 {
			m.mergeMenuSelected--
		}
		return m, nil
	case "down", "j":
		if m.mergeMenuSelected < len(kitchenTUIMergeMenuOptions)-1 {
			m.mergeMenuSelected++
		}
		return m, nil
	case "enter":
		plan := m.selectedPlanItem()
		m.inputMode = kitchenTUIInputNone
		if plan == nil || strings.TrimSpace(plan.Record.Lineage) == "" {
			return m, nil
		}
		switch m.mergeMenuSelected {
		case 0:
			return m, m.actionCmd(func() (string, string, error) {
				summary, err := m.backend.MergeCheck(plan.Record.Lineage)
				return summary, plan.Record.PlanID, err
			})
		case 1:
			return m, m.actionCmd(func() (string, string, error) {
				summary, err := m.backend.MergeLineage(plan.Record.Lineage)
				return summary, plan.Record.PlanID, err
			})
		case 2:
			return m, m.actionCmd(func() (string, string, error) {
				newTaskID, err := m.backend.FixLineageConflicts(plan.Record.Lineage)
				if err != nil {
					return "", plan.Record.PlanID, err
				}
				return "fix-merge task queued: " + newTaskID, plan.Record.PlanID, nil
			})
		case 3:
			return m, m.actionCmd(func() (string, string, error) {
				summary, err := m.backend.ReapplyLineage(plan.Record.Lineage)
				return summary, plan.Record.PlanID, err
			})
		}
	}
	return m, nil
}

func (m kitchenTUIModel) updateRemediateMenuInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputMode = kitchenTUIInputNone
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.remediateSelected > 0 {
			m.remediateSelected--
		}
		return m, nil
	case "down", "j":
		if m.remediateSelected < len(kitchenTUIRemediationOptions)-1 {
			m.remediateSelected++
		}
		return m, nil
	case "enter":
		plan := m.selectedPlanItem()
		m.inputMode = kitchenTUIInputNone
		if plan == nil {
			return m, nil
		}
		includeNits := m.remediateSelected == 1
		scope := "minor findings"
		if includeNits {
			scope = "minor findings and nits"
		}
		return m, m.actionCmd(func() (string, string, error) {
			err := m.backend.RemediateReview(plan.Record.PlanID, includeNits)
			return "queued remediation for " + scope, plan.Record.PlanID, err
		})
	}
	return m, nil
}

func (m kitchenTUIModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inputMode == kitchenTUIInputSubmit && m.submitSelecting {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc", "D":
			m.finishSubmitDependencySelection()
			return m, nil
		case "up", "k":
			if m.selectedPlan > 0 {
				m.selectedPlan--
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				(&m).resetCurrentRightPaneOffset()
				return m, m.loadCmd()
			}
			return m, nil
		case "down", "j":
			if m.selectedPlan < len(m.plans)-1 {
				m.selectedPlan++
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				(&m).resetCurrentRightPaneOffset()
				return m, m.loadCmd()
			}
			return m, nil
		case "enter", " ":
			if plan := m.selectedPlanItem(); plan != nil {
				m.toggleSubmitDependency(plan.Record.PlanID)
			}
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.closeInput()
		return m, nil
	case "ctrl+r":
		if m.inputMode == kitchenTUIInputSubmit {
			m.submitResearch = !m.submitResearch
			m.refreshSubmitInputPresentation()
			return m, nil
		}
	case "tab":
		if m.inputMode == kitchenTUIInputSubmit && !m.submitResearch {
			m.submitImplReview = !m.submitImplReview
			return m, nil
		}
	case "D":
		if m.inputMode == kitchenTUIInputSubmit && !m.submitResearch {
			m.beginSubmitDependencySelection()
			return m, nil
		}
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		switch m.inputMode {
		case kitchenTUIInputSubmit:
			if m.submitResearch {
				if value == "" {
					m.errText = "research topic must not be empty"
					return m, nil
				}
				m.closeInput()
				return m, m.actionCmd(func() (string, string, error) {
					planID, err := m.backend.SubmitResearch(value)
					if err != nil {
						return "", "", err
					}
					return "submitted research " + planID, planID, nil
				})
			}
			idea, anchorRef, err := parseSubmitInput(value)
			if err != nil {
				m.errText = err.Error()
				return m, nil
			}
			implReview := m.submitImplReview
			dependsOn := append([]string(nil), m.submitDependsOn...)
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				planID, err := m.backend.SubmitIdea(idea, implReview, anchorRef, dependsOn)
				if err != nil {
					return "", "", err
				}
				status := "submitted " + planID
				if implReview {
					status += " with impl review"
				}
				return status, planID, nil
			})
		case kitchenTUIInputPromote:
			planID := strings.TrimSpace(m.promotePlanID)
			if planID == "" {
				if plan := m.selectedPlanItem(); plan != nil {
					planID = strings.TrimSpace(plan.Record.PlanID)
				}
			}
			if planID == "" {
				m.closeInput()
				return m, nil
			}
			lineage := value
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				newPlanID, err := m.backend.PromoteResearch(planID, lineage, false, true)
				if err != nil {
					return "", "", err
				}
				return "promoted as " + newPlanID, newPlanID, nil
			})
		case kitchenTUIInputReplan:
			plan := m.selectedPlanItem()
			if plan == nil {
				m.closeInput()
				return m, nil
			}
			if value == "" {
				value = "Retry with revised plan."
			}
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				newPlanID, err := m.backend.ReplanPlan(plan.Record.PlanID, value)
				if err != nil {
					return "", "", err
				}
				return "replanned as " + newPlanID, newPlanID, nil
			})
		case kitchenTUIInputAnswer:
			q := m.selectedQuestionItem()
			if q == nil || value == "" {
				m.closeInput()
				return m, nil
			}
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				err := m.backend.AnswerQuestion(q.ID, value)
				return "Answered", m.selectedPlanID(), err
			})
		}
	case "ctrl+c":
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m kitchenTUIModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading Kitchen..."
	}

	header := m.renderHeader()
	footer := m.renderFooter()

	var bar string
	if m.inputMode != kitchenTUIInputNone {
		bar = m.renderInputBar()
	}
	barHeight := lipgloss.Height(bar)
	if bar == "" {
		barHeight = 0
	}

	bodyHeight := max(10, m.height-lipgloss.Height(header)-lipgloss.Height(footer)-barHeight-2)
	leftWidth := max(54, (m.width*2)/5)
	rightWidth := max(58, m.width-leftWidth-4)

	left := m.renderLeftPane(leftWidth, bodyHeight)
	right := m.renderDetailPane(rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)

	if bar != "" {
		return lipgloss.JoinVertical(lipgloss.Left, header, body, bar, footer)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *kitchenTUIModel) syncPlanSelection() {
	if len(m.plans) == 0 {
		m.selectedPlan = 0
		m.detail = nil
		m.tasks = nil
		return
	}
	target := strings.TrimSpace(m.pendingSelectedID)
	if target != "" {
		for i, plan := range m.plans {
			if plan.Record.PlanID == target {
				m.selectedPlan = i
				m.pendingSelectedID = ""
				break
			}
		}
	}
	if m.selectedPlan >= len(m.plans) {
		m.selectedPlan = len(m.plans) - 1
	}
}

func (m *kitchenTUIModel) openInput(mode kitchenTUIInputMode, prompt, placeholder string) {
	m.inputMode = mode
	m.input.Prompt = prompt + ": "
	m.input.Placeholder = placeholder
	m.input.SetValue("")
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *kitchenTUIModel) openSubmitInput() {
	m.submitImplReview = true
	m.submitResearch = false
	m.submitDependsOn = nil
	m.submitSelecting = false
	m.submitPrevLeft = m.leftMode
	m.openInput(kitchenTUIInputSubmit, "Submit idea", "Add typed parser errors")
	m.refreshSubmitInputPresentation()
}

func (m *kitchenTUIModel) openPromoteInput(planID string) {
	m.promotePlanID = strings.TrimSpace(planID)
	m.openInput(kitchenTUIInputPromote, "Promote research", "Optional lineage")
}

func (m *kitchenTUIModel) closeInput() {
	if m.inputMode == kitchenTUIInputSubmit {
		m.finishSubmitDependencySelection()
		m.submitDependsOn = nil
	}
	m.inputMode = kitchenTUIInputNone
	m.submitImplReview = false
	m.submitResearch = false
	m.promotePlanID = ""
	m.input.Blur()
	m.input.SetValue("")
}

func (m *kitchenTUIModel) beginSubmitDependencySelection() {
	if m == nil || m.inputMode != kitchenTUIInputSubmit || m.submitResearch {
		return
	}
	m.submitPrevLeft = m.leftMode
	m.submitSelecting = true
	m.leftMode = kitchenTUILeftPlans
	m.taskPaneMode = kitchenTUITaskPaneDetail
	m.input.Blur()
	m.errText = ""
}

func (m *kitchenTUIModel) finishSubmitDependencySelection() {
	if m == nil || !m.submitSelecting {
		return
	}
	m.submitSelecting = false
	if m.submitPrevLeft != "" {
		m.leftMode = m.submitPrevLeft
	}
	m.input.Focus()
}

func (m *kitchenTUIModel) refreshSubmitInputPresentation() {
	if m == nil || m.inputMode != kitchenTUIInputSubmit {
		return
	}
	if m.submitResearch {
		m.input.Prompt = "Submit research: "
		m.input.Placeholder = "How does OAuth callback forwarding work end to end?"
		m.input.SetValue(stripSubmitAnchorPrefix(m.input.Value()))
		m.input.CursorEnd()
		m.input.Focus()
		return
	}
	m.input.Prompt = "Submit idea: "
	m.input.Placeholder = "Add typed parser errors"
	if strings.TrimSpace(m.input.Value()) == "" {
		if ref := defaultSubmitAnchorRef(m.repoPath); ref != "" {
			m.input.SetValue("[ref=" + ref + "] ")
		}
	}
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *kitchenTUIModel) toggleSubmitDependency(planID string) {
	if m == nil {
		return
	}
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return
	}
	for i, depID := range m.submitDependsOn {
		if depID == planID {
			m.submitDependsOn = append(m.submitDependsOn[:i], m.submitDependsOn[i+1:]...)
			return
		}
	}
	m.submitDependsOn = append(m.submitDependsOn, planID)
	sort.Strings(m.submitDependsOn)
}

func (m kitchenTUIModel) submitDependencySelected(planID string) bool {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return false
	}
	for _, depID := range m.submitDependsOn {
		if depID == planID {
			return true
		}
	}
	return false
}

func (m kitchenTUIModel) selectedQuestionItem() *pool.Question {
	planID := ""
	if plan := m.selectedPlanItem(); plan != nil {
		planID = strings.TrimSpace(plan.Record.PlanID)
	}
	if planID == "" {
		return nil
	}
	prefix := planID + "-"
	var filtered []pool.Question
	for _, q := range m.questions {
		if strings.HasPrefix(strings.TrimSpace(q.TaskID), prefix) {
			filtered = append(filtered, q)
		}
	}
	if m.selectedQuestion < 0 || m.selectedQuestion >= len(filtered) {
		return nil
	}
	return &filtered[m.selectedQuestion]
}

func (m kitchenTUIModel) updateMultipleChoiceInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := m.selectedQuestionItem()
	if q == nil {
		m.closeInput()
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.closeInput()
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.selectedOption > 0 {
			m.selectedOption--
		}
		return m, nil
	case "down", "j":
		if m.selectedOption < len(q.Options)-1 {
			m.selectedOption++
		}
		return m, nil
	case "enter":
		if m.selectedOption >= 0 && m.selectedOption < len(q.Options) {
			answer := q.Options[m.selectedOption]
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				err := m.backend.AnswerQuestion(q.ID, answer)
				return "Answered", m.selectedPlanID(), err
			})
		}
	}
	return m, nil
}

func (m kitchenTUIModel) selectedPlanID() string {
	if plan := m.selectedPlanItem(); plan != nil {
		return plan.Record.PlanID
	}
	return ""
}

func (m kitchenTUIModel) selectedPlanItem() *kitchenTUIPlanItem {
	if m.selectedPlan < 0 || m.selectedPlan >= len(m.plans) {
		return nil
	}
	return &m.plans[m.selectedPlan]
}

func (m kitchenTUIModel) selectedTaskItem() *kitchenTUITaskItem {
	if m.selectedTask < 0 || m.selectedTask >= len(m.tasks) {
		return nil
	}
	return &m.tasks[m.selectedTask]
}

func taskRowKey(task kitchenTUITaskItem) string {
	if rowKey := strings.TrimSpace(task.RowKey); rowKey != "" {
		return rowKey
	}
	return strings.TrimSpace(task.RuntimeID)
}

func findTaskIndexByRowKey(tasks []kitchenTUITaskItem, rowKey string) int {
	rowKey = strings.TrimSpace(rowKey)
	if rowKey == "" {
		return -1
	}
	for i, task := range tasks {
		if taskRowKey(task) == rowKey {
			return i
		}
	}
	return -1
}

func findTaskIndexByRuntimeID(tasks []kitchenTUITaskItem, runtimeID string) int {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return -1
	}
	for i, task := range tasks {
		if strings.TrimSpace(task.RuntimeID) == runtimeID {
			return i
		}
	}
	return -1
}

func runtimeIDMatchesSelectedTask(tasks []kitchenTUITaskItem, selected int, runtimeID string) bool {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" || selected < 0 || selected >= len(tasks) {
		return false
	}
	return strings.TrimSpace(tasks[selected].RuntimeID) == runtimeID
}

func (m kitchenTUIModel) canApproveSelectedPlan() bool {
	plan := m.selectedPlanItem()
	return plan != nil && planDisplayState(*plan) == planStatePendingApproval
}

func (m kitchenTUIModel) canExtendSelectedPlan() bool {
	if m.detail == nil {
		return false
	}
	return canExtendCouncil(m.detail.Plan.State, m.detail.Execution) || canExtendReviewCouncil(m.detail.Plan.State, m.detail.Execution)
}

func (m kitchenTUIModel) canRequestReviewSelectedPlan() bool {
	plan := m.selectedPlanItem()
	if plan == nil {
		return false
	}
	switch planDisplayState(*plan) {
	case planStateCompleted, planStateImplementationReviewFailed:
		return true
	default:
		return false
	}
}

func (m kitchenTUIModel) selectedPlanProgress() *PlanProgress {
	if plan := m.selectedPlanItem(); plan != nil && plan.Progress != nil {
		return plan.Progress
	}
	if m.detail != nil {
		planID := strings.TrimSpace(m.selectedPlanID())
		if planID != "" && strings.TrimSpace(m.detail.Plan.PlanID) == planID {
			return &m.detail.Progress
		}
	}
	return nil
}

func (m kitchenTUIModel) canRemediateSelectedPlan() bool {
	progress := m.selectedPlanProgress()
	if progress == nil {
		return false
	}
	if strings.TrimSpace(progress.State) != planStateCompleted {
		return false
	}
	if strings.TrimSpace(progress.ImplReviewStatus) != planReviewStatusPassed {
		return false
	}
	return len(progress.ImplReviewFollowups) > 0
}

func (m kitchenTUIModel) canCancelSelectedPlan() bool {
	plan := m.selectedPlanItem()
	if plan == nil {
		return false
	}
	switch planDisplayState(*plan) {
	case "", "cancelled", planStateCompleted, planStateMerged, planStateClosed, planStateRejected:
		return false
	default:
		return true
	}
}

func (m kitchenTUIModel) canCancelSelectedTask() bool {
	task := m.selectedTaskItem()
	if task == nil || strings.TrimSpace(task.RuntimeID) == "" {
		return false
	}
	switch strings.TrimSpace(task.State) {
	case "", "planned", pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled:
		return false
	default:
		return true
	}
}

func (m kitchenTUIModel) canRetrySelectedTask() bool {
	task := m.selectedTaskItem()
	if task == nil {
		return false
	}
	switch strings.TrimSpace(task.State) {
	case pool.TaskFailed, pool.TaskCompleted:
		return true
	default:
		return false
	}
}

func (m kitchenTUIModel) canFixConflictsSelectedTask() bool {
	task := m.selectedTaskItem()
	return task != nil && strings.TrimSpace(task.State) == pool.TaskFailed && taskHasConflictInfo(*task)
}

func (m kitchenTUIModel) selectedTaskRetryLabel() string {
	task := m.selectedTaskItem()
	if task != nil && strings.TrimSpace(task.State) == pool.TaskCompleted {
		return "again"
	}
	return "retry"
}

func (m kitchenTUIModel) selectedTaskRetryStatus() string {
	task := m.selectedTaskItem()
	if task != nil && strings.TrimSpace(task.State) == pool.TaskCompleted {
		return "queued again"
	}
	return "retried"
}

func (m kitchenTUIModel) canCheckMergeSelectedPlan() bool {
	plan := m.selectedPlanItem()
	if plan == nil || strings.TrimSpace(plan.Record.Lineage) == "" {
		return false
	}
	switch planDisplayState(*plan) {
	case "", "cancelled", planStatePlanning, planStateReviewing, planStateImplementationReview, planStatePlanningFailed, planStateClosed, planStateRejected, planStateMerged, planStateWaitingOnDependency:
		return false
	default:
		return true
	}
}

func (m kitchenTUIModel) canMergeSelectedPlan() bool {
	plan := m.selectedPlanItem()
	if plan == nil || strings.TrimSpace(plan.Record.Lineage) == "" {
		return false
	}
	if implementationReviewFailed(plan.Progress) {
		return false
	}
	return planDisplayState(*plan) == planStateCompleted
}

func (m kitchenTUIModel) canPromoteSelectedPlan() bool {
	plan := m.selectedPlanItem()
	if plan == nil {
		return false
	}
	if strings.TrimSpace(plan.Record.Mode) != "research" {
		return false
	}
	switch planDisplayState(*plan) {
	case planStateResearchComplete, planStateCompleted:
		return true
	default:
		return false
	}
}

func (m kitchenTUIModel) nextPlanSelectionAfterDeletion() string {
	if len(m.plans) <= 1 {
		return ""
	}
	if m.selectedPlan > 0 && m.selectedPlan < len(m.plans) {
		return m.plans[m.selectedPlan-1].Record.PlanID
	}
	if m.selectedPlan+1 < len(m.plans) {
		return m.plans[m.selectedPlan+1].Record.PlanID
	}
	return ""
}

func (m kitchenTUIModel) footerActions() []string {
	if m.inputMode != kitchenTUIInputNone {
		switch m.inputMode {
		case kitchenTUIInputSubmit:
			if m.submitSelecting {
				return []string{"↑/↓ choose plan", "enter toggle dep", "D done", "esc done", "ctrl+c quit"}
			}
			submitMode := "idea"
			if m.submitResearch {
				submitMode = "research"
			}
			actions := []string{"enter submit", "ctrl+r mode:" + submitMode}
			if m.submitResearch {
				actions = append(actions, "esc cancel", "ctrl+c quit")
				return actions
			}
			implReviewMode := "off"
			if m.submitImplReview {
				implReviewMode = "on"
			}
			deps := fmt.Sprintf("%d", len(m.submitDependsOn))
			actions = append(actions, "tab impl-review:"+implReviewMode, "D deps:"+deps, "esc cancel", "ctrl+c quit")
			return actions
		case kitchenTUIInputPromote:
			return []string{"enter promote", "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputReplan:
			return []string{"enter replan", "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputAnswer:
			if q := m.selectedQuestionItem(); q != nil && len(q.Options) > 0 {
				return []string{"↑/↓ navigate", "enter select", "esc cancel", "ctrl+c quit"}
			}
			return []string{"enter answer", "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputMergeMenu:
			return []string{"↑/↓ navigate", "enter select", "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputRemediate:
			return []string{"↑/↓ navigate", "enter select", "esc cancel", "ctrl+c quit"}
		default:
			return []string{"esc cancel", "ctrl+c quit"}
		}
	}

	actions := []string{"n submit"}
	if m.leftMode == kitchenTUILeftTasks {
		actions = append(actions, "PgUp/PgDn scroll")
		if m.canCancelSelectedTask() {
			actions = append(actions, "C cancel")
		}
		if m.canRetrySelectedTask() {
			actions = append(actions, "R "+m.selectedTaskRetryLabel())
			actions = append(actions, "U reuse")
		}
		if m.canFixConflictsSelectedTask() {
			actions = append(actions, "f fix-conflicts")
		}
	} else if m.leftMode == kitchenTUILeftQuestions {
		actions = append(actions, "a answer", "esc back")
	} else {
		if m.canRemediateSelectedPlan() {
			actions = append(actions, "f remediate")
		}
		if m.canApproveSelectedPlan() {
			actions = append(actions, "a approve")
		}
		if m.canExtendSelectedPlan() {
			actions = append(actions, "e extend")
		}
		if m.canRequestReviewSelectedPlan() {
			actions = append(actions, "v review")
		}
		if m.canCancelSelectedPlan() {
			actions = append(actions, "C cancel")
		}
		if m.selectedPlanItem() != nil {
			actions = append(actions, "p replan")
			actions = append(actions, "D delete")
			if m.canPromoteSelectedPlan() {
				actions = append(actions, "P promote")
			}
			if m.canMergeSelectedPlan() {
				actions = append(actions, "M merge-menu")
			}
			actions = append(actions, "? questions")
		}
		actions = append(actions, "PgUp/PgDn scroll")
	}
	actions = append(actions, "r refresh", "q quit")
	return actions
}

func (m kitchenTUIModel) loadCmd() tea.Cmd {
	selectedPlanID := m.selectedPlanID()
	selectedTaskLogID := ""
	selectedTaskOutputID := ""
	selectedTaskRowKey := ""
	selectedTaskRuntimeID := ""
	currentTaskOutput := m.taskOutput
	if m.leftMode == kitchenTUILeftTasks {
		if task := m.selectedTaskItem(); task != nil {
			selectedTaskRowKey = taskRowKey(*task)
			selectedTaskRuntimeID = strings.TrimSpace(task.RuntimeID)
		}
	}
	if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneLogs {
		if task := m.selectedTaskItem(); task != nil {
			selectedTaskLogID = strings.TrimSpace(task.RuntimeID)
		}
	}
	if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneDetail {
		if task := m.selectedTaskItem(); task != nil {
			selectedTaskOutputID = strings.TrimSpace(task.RuntimeID)
		}
	}
	if strings.TrimSpace(m.pendingSelectedID) != "" {
		selectedPlanID = m.pendingSelectedID
	}
	currentLabel := ""
	if m.backend != nil {
		currentLabel = m.backend.Label()
	}
	repoPath := m.repoPath
	return func() tea.Msg {
		// If we're on local backend, see if `kitchen serve` has
		// come up since the last load so we can switch over and
		// stop dispatching via transient Kitchen instances.
		var upgraded kitchenTUIBackend
		if currentLabel == "local" && strings.TrimSpace(repoPath) != "" {
			if probe, err := openKitchenTUIBackend(repoPath); err == nil && probe.Label() != "local" {
				upgraded = probe
			}
		}
		backend := m.backend
		if upgraded != nil {
			backend = upgraded
		}
		status, err := backend.Status()
		if err != nil {
			return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
		}
		plans, err := backend.ListPlans()
		if err != nil {
			return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
		}
		questions, err := backend.ListQuestions()
		if err != nil {
			return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
		}
		planID := strings.TrimSpace(selectedPlanID)
		if planID != "" {
			found := false
			for _, plan := range plans {
				if plan.PlanID == planID {
					found = true
					break
				}
			}
			if !found {
				planID = ""
				selectedTaskLogID = ""
				selectedTaskOutputID = ""
			}
		}
		if planID == "" && len(plans) > 0 {
			planID = plans[0].PlanID
		}
		var detail *PlanDetail
		if planID != "" {
			got, err := backend.PlanDetail(planID)
			if err != nil {
				return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
			}
			detail = &got
		}
		var taskLog []pool.WorkerActivityRecord
		if selectedTaskLogID != "" {
			taskLog, err = backend.TaskActivity(selectedTaskLogID)
			if err != nil {
				return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
			}
		}
		taskOutput := currentTaskOutput
		var taskOutputErr error
		if selectedTaskOutputID != "" {
			out, err := backend.TaskOutput(selectedTaskOutputID)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					taskOutput = ""
					taskOutputErr = err
				} else {
					taskOutput = ""
				}
			} else {
				taskOutput = out
			}
		}
		return kitchenTUILoadedMsg{
			status:                status,
			plans:                 plans,
			questions:             questions,
			detail:                detail,
			selectedTaskRowKey:    selectedTaskRowKey,
			selectedTaskRuntimeID: selectedTaskRuntimeID,
			taskLogTaskID:         selectedTaskLogID,
			taskLog:               taskLog,
			taskOutputTaskID:      selectedTaskOutputID,
			taskOutput:            taskOutput,
			taskOutputErr:         taskOutputErr,
			newBackend:            upgraded,
		}
	}
}

func (m kitchenTUIModel) actionCmd(fn func() (string, string, error)) tea.Cmd {
	return func() tea.Msg {
		status, selectedPlanID, err := fn()
		return kitchenTUIActionMsg{
			status:         status,
			selectedPlanID: selectedPlanID,
			err:            err,
		}
	}
}

func (m kitchenTUIModel) renderHeader() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")).Padding(0, 1)
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	repo := m.status.RepoPath
	if repo == "" {
		repo = "."
	}
	branch := m.status.Anchor.Branch
	if branch == "" {
		branch = "unknown"
	}
	backendLabel := "unknown"
	if m.backend != nil {
		backendLabel = m.backend.Label()
	}
	headline := fmt.Sprintf("Kitchen  %s  %s@%s", repo, branch, shortenSHA(m.status.Anchor.Commit))
	summary := fmt.Sprintf("backend=%s  workers=%d/%d  pendingQuestions=%d  plans=%d", backendLabel, m.status.Queue.AliveWorkers, m.status.Queue.MaxWorkers, len(m.questions), len(m.plans))
	rows := []string{titleStyle.Render(headline), metaStyle.Render(summary)}
	// In local backend mode the TUI talks to a transient Kitchen that
	// does not run the scheduler, so any queued task will sit there
	// forever. Surface that clearly so the operator knows to start
	// `kitchen serve` in another terminal.
	if backendLabel == "local" {
		queued := 0
		for _, t := range m.status.Queue.Tasks {
			if t.Status == pool.TaskQueued {
				queued++
			}
		}
		if queued > 0 {
			warnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("202")).Padding(0, 1)
			rows = append(rows, warnStyle.Render(fmt.Sprintf("⚠  %d queued task(s) will not dispatch — run `kitchen serve` to start the scheduler", queued)))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m kitchenTUIModel) renderLeftPane(width, height int) string {
	if m.leftMode == kitchenTUILeftTasks {
		return m.renderTasksPane(width, height)
	}
	if m.leftMode == kitchenTUILeftQuestions {
		return m.renderQuestionsPane(width, height)
	}
	return m.renderPlansPane(width, height)
}

func (m kitchenTUIModel) renderPlansPane(width, height int) string {
	innerWidth, innerHeight := paneContentSize(width, height)
	title := paneTitle("Plans", true)
	if len(m.plans) == 0 {
		return paneBox(width, height, title+"\n\nNo plans.")
	}
	listLineBudget := max(1, innerHeight-1)
	lines := make([]string, 0, len(m.plans)*2)
	for i, plan := range m.plans {
		state := padRight(compactState(planDisplayState(plan)), 6)
		marker := rowMarker(i == m.selectedPlan && m.leftMode == kitchenTUILeftPlans)
		badge := ""
		badgeWidth := 0
		if nq := pendingQuestionCountForPlan(plan.Record.PlanID, m.questions); nq > 0 {
			badgeText := fmt.Sprintf(" [%d?]", nq)
			badgeWidth = len(badgeText)
			badge = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render(badgeText)
		}
		primary := truncate(fmt.Sprintf("%s %s %s", marker, state, plan.Record.Title), innerWidth-badgeWidth) + badge
		secondary := fmt.Sprintf("    lineage: %s  plan: %s", firstNonEmpty(plan.Record.Lineage, "-"), plan.Record.PlanID)
		if m.submitDependencySelected(plan.Record.PlanID) {
			secondary += " [dep]"
		}
		secondary = truncate(secondary, innerWidth)
		lines = append(lines, renderSelectableRow(i == m.selectedPlan && m.leftMode == kitchenTUILeftPlans, primary, secondary)...)
	}
	return paneBox(width, height, title+"\n"+fitListLines(lines, listLineBudget))
}

func (m kitchenTUIModel) renderTasksPane(width, height int) string {
	innerWidth, innerHeight := paneContentSize(width, height)
	titleText := "Tasks"
	if plan := m.selectedPlanItem(); plan != nil {
		titleText = "Tasks · " + truncate(plan.Record.Title, max(1, innerWidth-10))
	}
	title := paneTitle(titleText, true)
	if len(m.tasks) == 0 {
		return paneBox(width, height, title+"\n\nSelected plan has no task list.")
	}
	visibleLineBudget := max(1, innerHeight-1)
	visibleItems := max(1, visibleLineBudget/2)
	start, end := selectableItemWindow(len(m.tasks), m.selectedTask, visibleItems)
	lines := make([]string, 0, (end-start)*2)
	for i := start; i < end; i++ {
		task := m.tasks[i]
		marker := rowMarker(i == m.selectedTask && m.leftMode == kitchenTUILeftTasks)
		primary := truncate(fmt.Sprintf("%s %s %s", marker, padRight(compactState(task.State), 6), task.Title), innerWidth)
		secondary := truncate("    "+task.ID+" · "+task.Kind, innerWidth)
		lines = append(lines, renderSelectableRow(i == m.selectedTask && m.leftMode == kitchenTUILeftTasks, primary, secondary)...)
	}
	return paneBox(width, height, title+"\n"+fitListLines(lines, visibleLineBudget))
}

func (m kitchenTUIModel) renderQuestionsPane(width, height int) string {
	innerWidth, innerHeight := paneContentSize(width, height)
	titleText := "Questions"
	if plan := m.selectedPlanItem(); plan != nil {
		titleText = "Questions · " + truncate(plan.Record.Title, max(1, innerWidth-14))
	}
	title := paneTitle(titleText, true)

	planID := ""
	if plan := m.selectedPlanItem(); plan != nil {
		planID = strings.TrimSpace(plan.Record.PlanID)
	}
	var filtered []pool.Question
	if planID != "" {
		prefix := planID + "-"
		for _, q := range m.questions {
			if strings.HasPrefix(strings.TrimSpace(q.TaskID), prefix) {
				filtered = append(filtered, q)
			}
		}
	}

	if len(filtered) == 0 {
		return paneBox(width, height, title+"\n\nNo pending questions.")
	}

	listLineBudget := max(1, innerHeight-1)
	lines := make([]string, 0, len(filtered)*2)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	for i, q := range filtered {
		selected := i == m.selectedQuestion
		marker := rowMarker(selected)
		text := truncate(q.Question, 60)
		suffix := ""
		if len(q.Options) > 0 {
			suffix = dimStyle.Render(fmt.Sprintf(" (options: %d)", len(q.Options)))
		}
		primary := truncate(fmt.Sprintf("%s %d. %s", marker, i+1, text), innerWidth) + suffix
		secondary := truncate(fmt.Sprintf("    id: %s  task: %s", q.ID, q.TaskID), innerWidth)
		lines = append(lines, renderSelectableRow(selected, primary, secondary)...)
	}
	return paneBox(width, height, title+"\n"+fitListLines(lines, listLineBudget))
}

func (m kitchenTUIModel) renderDetailPane(width, height int) string {
	if m.leftMode == kitchenTUILeftQuestions {
		return m.renderQuestionDetailPane(width, height)
	}
	if m.detail == nil {
		return paneBox(width, height, "Detail\n\nNo selected plan.")
	}
	innerWidth := max(24, width-6)
	var lines []string
	if m.leftMode == kitchenTUILeftTasks {
		if m.taskPaneMode == kitchenTUITaskPaneLogs {
			lines = m.renderTaskLogLines(innerWidth)
		} else {
			lines = m.renderTaskDetailLines(innerWidth)
		}
	} else if m.inputMode == kitchenTUIInputMergeMenu {
		lines = append(lines, m.renderMergeMenuLines(innerWidth)...)
		lines = append(lines, "")
		lines = append(lines, m.renderPlanDetailLines(innerWidth)...)
	} else if m.inputMode == kitchenTUIInputRemediate {
		lines = append(lines, m.renderRemediationMenuLines(innerWidth)...)
		lines = append(lines, "")
		lines = append(lines, m.renderPlanDetailLines(innerWidth)...)
	} else {
		lines = m.renderPlanDetailLines(innerWidth)
	}
	rendered, _ := windowAndWrapLines(lines, innerWidth, height, m.rightPaneOffset())
	return paneBox(width, height, rendered)
}

func (m kitchenTUIModel) renderQuestionDetailPane(width, height int) string {
	q := m.selectedQuestionItem()
	if q == nil {
		return paneBox(width, height, "Question Detail\n\nNo selected question.")
	}
	innerWidth := max(24, width-6)
	lines := m.renderQuestionDetailLines(innerWidth)
	rendered, _ := windowAndWrapLines(lines, innerWidth, height, m.rightPaneOffset())
	return paneBox(width, height, rendered)
}

func (m kitchenTUIModel) renderMergeMenuLines(innerWidth int) []string {
	highlightStyle := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("Merge Menu"),
		"Choose an action for the selected lineage.",
	}
	for i, option := range kitchenTUIMergeMenuOptions {
		if i == m.mergeMenuSelected {
			lines = append(lines, truncate(highlightStyle.Render("> "+option), innerWidth))
			continue
		}
		lines = append(lines, truncate("  "+option, innerWidth))
	}
	return lines
}

func (m kitchenTUIModel) renderRemediationMenuLines(innerWidth int) []string {
	highlightStyle := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("Review Remediation"),
		"Choose which lower-severity findings to fix before re-review.",
	}
	for i, option := range kitchenTUIRemediationOptions {
		if i == m.remediateSelected {
			lines = append(lines, truncate(highlightStyle.Render("> "+option), innerWidth))
			continue
		}
		lines = append(lines, truncate("  "+option, innerWidth))
	}
	return lines
}

func (m kitchenTUIModel) renderQuestionDetailLines(innerWidth int) []string {
	q := m.selectedQuestionItem()
	if q == nil {
		return []string{"Question Detail", "", "No selected question."}
	}
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	boldStyle := lipgloss.NewStyle().Bold(true)
	highlightStyle := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)

	lines := []string{
		dimStyle.Render(fmt.Sprintf("Question %s · %s", q.ID, firstNonEmpty(q.Category, "general"))),
		"",
		boldStyle.Render("Question:"),
	}
	lines = append(lines, wrapText(q.Question, innerWidth)...)
	lines = append(lines, "")
	if strings.TrimSpace(q.Context) != "" {
		lines = append(lines, dimStyle.Render("Context:"))
		lines = append(lines, wrapText(q.Context, innerWidth)...)
		lines = append(lines, "")
	}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("Worker: %s  Task: %s", firstNonEmpty(q.WorkerID, "-"), firstNonEmpty(q.TaskID, "-"))))
	if !q.AskedAt.IsZero() {
		lines = append(lines, dimStyle.Render("Asked: "+q.AskedAt.UTC().Format("2006-01-02 15:04:05")))
	}
	lines = append(lines, "")
	if q.Answered {
		lines = append(lines, boldStyle.Render("Answer: ")+q.Answer)
		return lines
	}
	if len(q.Options) > 0 {
		lines = append(lines, boldStyle.Render("Options:"))
		for i, opt := range q.Options {
			prefix := fmt.Sprintf("  %d. ", i+1)
			if m.inputMode == kitchenTUIInputAnswer && i == m.selectedOption {
				lines = append(lines, highlightStyle.Render(prefix+opt))
			} else {
				lines = append(lines, prefix+opt)
			}
		}
		if m.inputMode == kitchenTUIInputAnswer {
			lines = append(lines, "", dimStyle.Render("↑/↓ navigate · enter select · esc cancel"))
		} else {
			lines = append(lines, "", dimStyle.Render("Press 'a' to answer"))
		}
		return lines
	}
	if m.inputMode != kitchenTUIInputAnswer {
		lines = append(lines, dimStyle.Render("Press 'a' to answer"))
	}
	return lines
}

func (m kitchenTUIModel) currentRightPaneKey() string {
	switch m.leftMode {
	case kitchenTUILeftPlans:
		return "plan"
	case kitchenTUILeftTasks:
		if m.taskPaneMode == kitchenTUITaskPaneLogs {
			return "task_log"
		}
		return "task_detail"
	case kitchenTUILeftQuestions:
		return "question"
	default:
		return ""
	}
}

func (m kitchenTUIModel) rightPaneOffset() int {
	key := m.currentRightPaneKey()
	if key == "" || m.rightPaneOffsets == nil {
		return 0
	}
	offset := m.rightPaneOffsets[key]
	if offset < 0 {
		return 0
	}
	return offset
}

func (m *kitchenTUIModel) setRightPaneOffset(v int) {
	if m == nil {
		return
	}
	if v < 0 {
		v = 0
	}
	key := m.currentRightPaneKey()
	if key == "" {
		return
	}
	lines, innerWidth, height := m.measureCurrentRightPane()
	_, total := windowAndWrapLines(lines, innerWidth, 0, 0)
	maxOffset := total - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if v > maxOffset {
		v = maxOffset
	}
	if m.rightPaneOffsets == nil {
		m.rightPaneOffsets = map[string]int{}
	}
	if v == 0 {
		delete(m.rightPaneOffsets, key)
		return
	}
	m.rightPaneOffsets[key] = v
}

func (m *kitchenTUIModel) resetCurrentRightPaneOffset() {
	if m == nil || m.rightPaneOffsets == nil {
		return
	}
	key := m.currentRightPaneKey()
	if key == "" {
		return
	}
	delete(m.rightPaneOffsets, key)
}

func (m kitchenTUIModel) measureCurrentRightPane() ([]string, int, int) {
	_, height, innerWidth := m.rightPaneDimensions()
	switch m.leftMode {
	case kitchenTUILeftPlans:
		if m.detail == nil {
			return []string{"No selected plan."}, innerWidth, height
		}
		if m.inputMode == kitchenTUIInputMergeMenu {
			lines := append([]string(nil), m.renderMergeMenuLines(innerWidth)...)
			lines = append(lines, "")
			lines = append(lines, m.renderPlanDetailLines(innerWidth)...)
			return lines, innerWidth, height
		}
		return m.renderPlanDetailLines(innerWidth), innerWidth, height
	case kitchenTUILeftTasks:
		if m.taskPaneMode == kitchenTUITaskPaneLogs {
			return m.renderTaskLogLines(innerWidth), innerWidth, height
		}
		return m.renderTaskDetailLines(innerWidth), innerWidth, height
	case kitchenTUILeftQuestions:
		return m.renderQuestionDetailLines(innerWidth), innerWidth, height
	default:
		return nil, innerWidth, height
	}
}

func (m kitchenTUIModel) rightPaneDimensions() (width, height, innerWidth int) {
	header := m.renderHeader()
	footer := m.renderFooter()
	bar := ""
	if m.inputMode != kitchenTUIInputNone {
		bar = m.renderInputBar()
	}
	barHeight := lipgloss.Height(bar)
	if bar == "" {
		barHeight = 0
	}
	height = max(10, m.height-lipgloss.Height(header)-lipgloss.Height(footer)-barHeight-2)
	leftWidth := max(54, (m.width*2)/5)
	width = max(58, m.width-leftWidth-4)
	innerWidth = max(24, width-6)
	return width, height, innerWidth
}

func (m kitchenTUIModel) pageScrollDelta() int {
	_, height, _ := m.rightPaneDimensions()
	if height <= 1 {
		return 1
	}
	return max(1, height-2)
}

func (m kitchenTUIModel) renderPlanDetailLines(innerWidth int) []string {
	detail := *m.detail
	state := firstNonEmpty(planDisplayState(kitchenTUIPlanItem{Record: detail.Plan, Progress: &detail.Progress}), detail.Execution.State, detail.Plan.State)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(detail.Plan.Title),
		fmt.Sprintf("Plan ID: %s", detail.Plan.PlanID),
		fmt.Sprintf("Mode: %s", firstNonEmpty(detail.Plan.Mode, "implementation")),
		fmt.Sprintf("State: %s", state),
		fmt.Sprintf("Phase: %s", detail.Progress.Phase),
		fmt.Sprintf("Lineage: %s", detail.Plan.Lineage),
		fmt.Sprintf("Anchor: %s@%s", detail.Plan.Anchor.Branch, shortenSHA(detail.Plan.Anchor.Commit)),
		fmt.Sprintf("Tasks: %d", len(m.tasks)),
		fmt.Sprintf("Pending questions: %s", pendingQuestionSummaryForPlan(detail.Plan.PlanID, m.questions)),
		"",
		"Summary:",
	}
	lines = append(lines, wrapText(firstNonEmpty(detail.Plan.Summary, "-"), innerWidth)...)
	lines = append(lines,
		"",
		fmt.Sprintf("Active tasks: %s", joinOrDash(detail.Progress.ActiveTaskIDs)),
		fmt.Sprintf("Completed tasks: %s", joinOrDash(detail.Progress.CompletedTaskIDs)),
		fmt.Sprintf("Failed tasks: %s", joinOrDash(detail.Progress.FailedTaskIDs)),
	)
	if strings.TrimSpace(detail.Plan.ResearchPlanID) != "" {
		lines = append(lines, fmt.Sprintf("Research source: %s", detail.Plan.ResearchPlanID))
	}
	if strings.TrimSpace(detail.Execution.ResearchOutput) != "" {
		lines = append(lines, "", "Research output:")
		lines = append(lines, wrapText(detail.Execution.ResearchOutput, innerWidth)...)
	}
	if len(detail.Progress.DependsOn) > 0 {
		lines = append(lines, fmt.Sprintf("Depends on: %s", strings.Join(detail.Progress.DependsOn, ", ")))
	}
	if detail.Progress.ImplReviewRequested {
		status := firstNonEmpty(detail.Progress.ImplReviewStatus, "pending")
		if detail.Progress.AutoRemediationActive {
			if detail.Progress.ReviewRemediationMode == reviewRemediationModeManual {
				status = "manual remediation in progress"
			} else {
				status = fmt.Sprintf("auto-remediating (%d/%d)", max(1, detail.Progress.AutoRemediationAttempt), AutoRemediationHardCap)
			}
		}
		lines = append(lines, fmt.Sprintf("Implementation review: %s", status))
		if detail.Progress.AutoRemediationActive {
			taskLabel := "Auto-remediation task"
			findingsLabel := "Auto-remediation findings:"
			if detail.Progress.ReviewRemediationMode == reviewRemediationModeManual {
				taskLabel = "Remediation task"
				findingsLabel = "Remediation findings:"
			}
			lines = append(lines, fmt.Sprintf("%s: %s", taskLabel, firstNonEmpty(detail.Progress.AutoRemediationTaskID, "-")))
			if detail.Progress.AutoRemediationSourceVerdict != "" {
				lines = append(lines, fmt.Sprintf("Source verdict: %s", detail.Progress.AutoRemediationSourceVerdict))
			}
			if len(detail.Progress.AutoRemediationFindings) > 0 {
				lines = append(lines, findingsLabel)
				for _, finding := range detail.Progress.AutoRemediationFindings {
					lines = append(lines, wrapText("- "+finding, innerWidth)...)
				}
			}
		} else if len(detail.Progress.ImplReviewFollowups) > 0 {
			lines = append(lines, "Implementation review follow-up findings:")
			for _, finding := range detail.Progress.ImplReviewFollowups {
				lines = append(lines, wrapText("- "+finding, innerWidth)...)
			}
		}
	}
	if detail.Execution.State == planStateImplementationReview || hasReviewCouncilState(detail.Execution) {
		activeTurn := detail.Execution.ReviewCouncilTurnsCompleted + 1
		for _, cycle := range detail.Progress.Cycles {
			if cycle.ImplReviewTaskID != "" && cycle.Index > activeTurn {
				activeTurn = cycle.Index
			}
		}
		if strings.TrimSpace(detail.Execution.ReviewCouncilFinalDecision) != "" && detail.Execution.ReviewCouncilTurnsCompleted > 0 {
			activeTurn = detail.Execution.ReviewCouncilTurnsCompleted
		}
		activeSeat := reviewCouncilSeatForTurn(detail.Execution.ReviewCouncilTurnsCompleted + 1)
		if strings.TrimSpace(detail.Execution.ReviewCouncilFinalDecision) != "" && len(detail.Execution.ReviewCouncilTurns) > 0 {
			last := detail.Execution.ReviewCouncilTurns[len(detail.Execution.ReviewCouncilTurns)-1]
			activeSeat = firstNonEmpty(strings.TrimSpace(last.Seat), reviewCouncilSeatForTurn(last.Turn))
		} else if activeTurn > 0 {
			activeSeat = reviewCouncilSeatForTurn(activeTurn)
		}
		status := firstNonEmpty(strings.TrimSpace(detail.Execution.ReviewCouncilFinalDecision), "reviewing")
		if detail.Execution.State == planStateImplementationReviewFailed && strings.TrimSpace(detail.Execution.ReviewCouncilFinalDecision) == "" {
			status = "failed"
		}
		reviewLabel := fmt.Sprintf("Review Council: turn %d/%d · Seat %s [%s]", max(1, activeTurn), max(1, detail.Execution.ReviewCouncilMaxTurns), activeSeat, status)
		if detail.Progress.ReviewCouncilCycle > 1 {
			reviewLabel = fmt.Sprintf("Review Council: cycle %d turn %d/%d · Seat %s [%s]", detail.Progress.ReviewCouncilCycle, max(1, activeTurn), max(1, detail.Execution.ReviewCouncilMaxTurns), activeSeat, status)
		}
		trajectory := make([]string, 0, len(detail.Execution.ReviewCouncilTurns))
		for _, turn := range detail.Execution.ReviewCouncilTurns {
			verdict := "-"
			if turn.Artifact != nil && strings.TrimSpace(turn.Artifact.Verdict) != "" {
				verdict = strings.TrimSpace(turn.Artifact.Verdict)
			}
			trajectory = append(trajectory, fmt.Sprintf("%s: %s", firstNonEmpty(turn.Seat, reviewCouncilSeatForTurn(turn.Turn)), verdict))
		}
		if detail.Execution.State == planStateImplementationReview && strings.TrimSpace(detail.Execution.ReviewCouncilFinalDecision) == "" {
			trajectory = append(trajectory, fmt.Sprintf("%s: -", activeSeat))
		}
		lines = append(lines,
			reviewLabel,
			fmt.Sprintf("Review verdicts: %s", firstNonEmpty(strings.Join(trajectory, ", "), "-")),
		)
	}
	if hasCouncilState(detail.Execution) {
		now := time.Now().UTC()
		lines = append(lines, "", "Council:")
		for i, seat := range detail.Execution.CouncilSeats {
			seatName := strings.TrimSpace(seat.Seat)
			if seatName == "" {
				if i == 1 {
					seatName = "B"
				} else {
					seatName = "A"
				}
			}
			seatParts := []string{"worker " + firstNonEmpty(strings.TrimSpace(seat.WorkerID), "-")}
			if seat.IdleSince != nil {
				seatParts = append(seatParts, "idle "+formatShortDuration(now.Sub(seat.IdleSince.UTC())))
			}
			if seat.Rehydrated {
				seatParts = append(seatParts, "rehydrated")
			}
			lines = append(lines, fmt.Sprintf("Seat %s: %s", seatName, strings.Join(seatParts, ", ")))
		}
		lines = append(lines,
			fmt.Sprintf("Turns: %d/%d (hard cap %d)", detail.Execution.CouncilTurnsCompleted, detail.Execution.CouncilMaxTurns, CouncilHardCap),
			fmt.Sprintf("Decision: %s", firstNonEmpty(detail.Execution.CouncilFinalDecision, "(pending)")),
		)
		if len(detail.Execution.CouncilTurns) == 0 {
			lines = append(lines, "No council turns recorded.")
		} else {
			lines = append(lines, "Turn summaries:")
			for i, turn := range detail.Execution.CouncilTurns {
				if turn.Artifact == nil {
					lines = append(lines, fmt.Sprintf("Turn %d [%s]", turn.Turn, firstNonEmpty(turn.Seat, "-")))
					continue
				}
				lines = append(lines, fmt.Sprintf("Turn %d [%s] %s", turn.Turn, firstNonEmpty(turn.Artifact.Seat, turn.Seat, "-"), firstNonEmpty(turn.Artifact.Stance, "-")))
				lines = appendWrappedPrefixed(lines, "  ", firstNonEmpty(turn.Artifact.Summary, turn.Artifact.SeatMemo), max(1, innerWidth))
				lines = append(lines, fmt.Sprintf("  disagreements: %d  blocking questions: %d", len(turn.Artifact.Disagreements), countBlockingCouncilQuestions(turn.Artifact.QuestionsForUser)))
				var prev *adapter.PlanArtifact
				if i > 0 && detail.Execution.CouncilTurns[i-1].Artifact != nil {
					prev = detail.Execution.CouncilTurns[i-1].Artifact.CandidatePlan
				}
				if turn.Artifact.CandidatePlan == nil && turn.Artifact.AdoptedPriorPlan {
					lines = append(lines, fmt.Sprintf("  Seat %s adopted prior plan (no changes)", firstNonEmpty(turn.Artifact.Seat, turn.Seat, "-")))
					continue
				}
				lines = append(lines, renderCouncilTurnDiffLines(prev, turn.Artifact.CandidatePlan, innerWidth, 12)...)
			}
		}
		switch detail.Plan.State {
		case planStateRejected:
			lines = appendCouncilDisagreements(lines, "Unresolved disagreements:", detail.Execution.CouncilUnresolvedDisagreements, innerWidth)
		case planStatePendingApproval:
			lines = appendCouncilDisagreements(lines, "Council warnings:", detail.Execution.CouncilWarnings, innerWidth)
		}
	}
	lines = append(lines, "", "Recent history:")
	history := detail.History
	if len(history) > 6 {
		history = history[len(history)-6:]
	}
	if len(history) == 0 {
		lines = append(lines, "No history.")
	} else {
		for _, entry := range history {
			lines = append(lines, fmt.Sprintf("%s: %s", entry.Type, summarizePlanHistoryEntry(entry)))
		}
	}
	return lines
}

func (m kitchenTUIModel) renderTaskDetailLines(innerWidth int) []string {
	task := m.selectedTaskItem()
	if task == nil {
		return []string{"No selected task."}
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(task.Title),
		fmt.Sprintf("Task ID: %s", task.ID),
		fmt.Sprintf("Runtime ID: %s", task.RuntimeID),
		fmt.Sprintf("Kind: %s", task.Kind),
		fmt.Sprintf("State: %s", task.State),
	}
	if task.WorkerID != "" {
		lines = append(lines, fmt.Sprintf("Worker: %s", task.WorkerID))
	}
	if task.Complexity != "" {
		lines = append(lines, fmt.Sprintf("Complexity: %s", task.Complexity))
	}
	if task.Kind == "implementation-review" {
		if summary := m.renderImplementationReviewTLDR(*task, innerWidth); len(summary) > 0 {
			lines = append(lines, "")
			lines = append(lines, summary...)
		}
	}
	if task.Summary != "" {
		lines = append(lines, "", "Latest summary:")
		lines = append(lines, wrapText(task.Summary, innerWidth)...)
	}
	if task.Prompt != "" {
		lines = append(lines, "", "Prompt:")
		lines = append(lines, wrapText(task.Prompt, innerWidth)...)
	}
	if m.taskOutputLoading {
		lines = append(lines, "", "Final output:", "loading...")
	} else if strings.TrimSpace(m.taskOutput) != "" {
		lines = append(lines, "", "Final output:")
		lines = append(lines, wrapText(m.taskOutput, innerWidth)...)
	}
	return lines
}

type implementationReviewTLDR struct {
	Status              string
	Summary             string
	FindingCount        int
	DistinctFileCount   int
	DisagreementCount   int
	HasStructuredCounts bool
}

func (m kitchenTUIModel) renderImplementationReviewTLDR(task kitchenTUITaskItem, innerWidth int) []string {
	tldr, ok := summarizeImplementationReviewTask(m.detail, task)
	if !ok {
		return nil
	}

	lines := []string{
		"Review TL;DR:",
		fmt.Sprintf("Status: %s", firstNonEmpty(tldr.Status, "pending")),
	}
	if tldr.HasStructuredCounts {
		findings := fmt.Sprintf("%d", tldr.FindingCount)
		if tldr.DistinctFileCount > 0 {
			findings = fmt.Sprintf("%d across %d files", tldr.FindingCount, tldr.DistinctFileCount)
		}
		lines = append(lines, fmt.Sprintf("Findings: %s", findings))
	} else if tldr.Status == "failed" && tldr.FindingCount > 0 {
		lines = append(lines, fmt.Sprintf("Findings: %d", tldr.FindingCount))
	}
	if tldr.DisagreementCount > 0 {
		lines = append(lines, fmt.Sprintf("Disagreements: %d", tldr.DisagreementCount))
	}
	if strings.TrimSpace(tldr.Summary) != "" {
		lines = append(lines, "Summary:")
		lines = append(lines, wrapText(tldr.Summary, innerWidth)...)
	}
	return lines
}

func summarizeImplementationReviewTask(detail *PlanDetail, task kitchenTUITaskItem) (implementationReviewTLDR, bool) {
	if detail == nil || task.Kind != "implementation-review" {
		return implementationReviewTLDR{}, false
	}

	turn := implementationReviewTurnForTask(detail.Plan.PlanID, task.RuntimeID)
	if record := findReviewCouncilTurn(detail.Execution.ReviewCouncilTurns, turn); record != nil && record.Artifact != nil {
		artifact := record.Artifact
		status := "failed"
		if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
			status = "approved"
		}
		return implementationReviewTLDR{
			Status:              status,
			Summary:             firstNonEmpty(strings.TrimSpace(artifact.Summary), strings.TrimSpace(task.Summary), defaultImplementationReviewSummary(status)),
			FindingCount:        len(artifact.Findings),
			DistinctFileCount:   countDistinctReviewFindingFiles(artifact.Findings),
			DisagreementCount:   len(artifact.Disagreements),
			HasStructuredCounts: true,
		}, true
	}

	if !shouldUsePlanLevelImplementationReviewFallback(detail, task, turn) {
		status := summarizeImplementationReviewStatus(nil, task)
		return implementationReviewTLDR{
			Status:              status,
			Summary:             firstNonEmpty(strings.TrimSpace(task.Summary), defaultImplementationReviewSummary(status)),
			FindingCount:        0,
			DistinctFileCount:   0,
			DisagreementCount:   0,
			HasStructuredCounts: false,
		}, true
	}

	status := summarizeImplementationReviewStatus(detail, task)
	findingCount := len(detail.Progress.ImplReviewFindings)
	if status == "approved" {
		findingCount = len(detail.Progress.ImplReviewFollowups)
	}
	summary := firstNonEmpty(strings.TrimSpace(task.Summary), firstPlanReviewFinding(planReviewFollowupStrings(detail.Progress)), defaultImplementationReviewSummary(status))
	if status == "approved" && findingCount == 0 {
		findingCount = 0
	}
	return implementationReviewTLDR{
		Status:              status,
		Summary:             summary,
		FindingCount:        findingCount,
		DistinctFileCount:   0,
		DisagreementCount:   0,
		HasStructuredCounts: status == "approved" || status == "failed",
	}, true
}

func shouldUsePlanLevelImplementationReviewFallback(detail *PlanDetail, task kitchenTUITaskItem, turn int) bool {
	if detail == nil {
		return false
	}
	if turn <= 0 || len(detail.Execution.ReviewCouncilTurns) == 0 {
		return true
	}
	switch strings.TrimSpace(task.State) {
	case pool.TaskQueued, pool.TaskDispatched, "active", "planned":
		return true
	}
	return turn > detail.Execution.ReviewCouncilTurnsCompleted
}

func summarizeImplementationReviewStatus(detail *PlanDetail, task kitchenTUITaskItem) string {
	if detail != nil {
		switch strings.TrimSpace(detail.Progress.ImplReviewStatus) {
		case planReviewStatusPassed:
			return "approved"
		case planReviewStatusFailed:
			return "failed"
		}
		switch detail.Execution.State {
		case planStateCompleted:
			if detail.Progress.ImplReviewRequested {
				return "approved"
			}
		case planStateImplementationReviewFailed:
			return "failed"
		case planStateImplementationReview:
			switch strings.TrimSpace(task.State) {
			case pool.TaskDispatched, "active":
				return "reviewing"
			default:
				return "pending"
			}
		}
	}

	switch strings.TrimSpace(task.State) {
	case pool.TaskCompleted:
		return "approved"
	case pool.TaskFailed:
		return "failed"
	case pool.TaskDispatched, "active":
		return "reviewing"
	default:
		return "pending"
	}
}

func defaultImplementationReviewSummary(status string) string {
	switch strings.TrimSpace(status) {
	case "approved":
		return "Implementation review approved."
	case "failed":
		return "Implementation review failed."
	case "reviewing":
		return "Implementation review is in progress."
	default:
		return "Implementation review is pending."
	}
}

func firstPlanReviewFinding(findings []string) string {
	for _, finding := range findings {
		if finding = strings.TrimSpace(finding); finding != "" {
			return finding
		}
	}
	return ""
}

func planReviewFollowupStrings(progress PlanProgress) []string {
	if strings.TrimSpace(progress.ImplReviewStatus) == planReviewStatusPassed {
		return append([]string(nil), progress.ImplReviewFollowups...)
	}
	return append([]string(nil), progress.ImplReviewFindings...)
}

func implementationReviewTurnForTask(planID, runtimeID string) int {
	return reviewCouncilTurnNumberFromTaskID(planID, runtimeID)
}

func findReviewCouncilTurn(turns []ReviewCouncilTurnRecord, turn int) *ReviewCouncilTurnRecord {
	if turn <= 0 {
		return nil
	}
	for i := range turns {
		if turns[i].Turn == turn {
			return &turns[i]
		}
	}
	return nil
}

func countDistinctReviewFindingFiles(findings []adapter.ReviewFinding) int {
	if len(findings) == 0 {
		return 0
	}
	paths := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if path := strings.TrimSpace(finding.File); path != "" {
			paths[path] = struct{}{}
		}
	}
	return len(paths)
}

func (m kitchenTUIModel) renderTaskLogLines(innerWidth int) []string {
	task := m.selectedTaskItem()
	if task == nil {
		return []string{"No selected task."}
	}
	rawHeader := sanitizeInlineTerminalText(task.Title) + " · activity log"
	rawHeader = ansi.Truncate(rawHeader, max(1, innerWidth-2), "…")
	lines := []string{
		paneTitle(rawHeader, true),
		fmt.Sprintf("Task ID: %s", sanitizeInlineTerminalText(task.ID)),
		fmt.Sprintf("Runtime ID: %s", sanitizeInlineTerminalText(task.RuntimeID)),
		"",
	}
	if len(m.taskLog) == 0 {
		lines = append(lines, "No activity log available for this task.")
		return lines
	}
	for i := len(m.taskLog) - 1; i >= 0; i-- {
		record := m.taskLog[i]
		ts := record.RecordedAt.UTC().Format("2006-01-02 15:04:05")
		label := strings.Join(strings.Fields(strings.TrimSpace(strings.Join([]string{
			firstNonEmpty(record.Activity.Kind, "activity"),
			record.Activity.Phase,
			record.Activity.Name,
		}, " "))), " ")
		label = sanitizeInlineTerminalText(label)
		if label == "" {
			label = "activity"
		}
		line := fmt.Sprintf("%s  %s", ts, label)
		if summary := sanitizeInlineTerminalText(record.Activity.Summary); summary != "" {
			line += "  " + summary
		}
		lines = append(lines, line)
	}
	return lines
}

func (m kitchenTUIModel) renderInputBar() string {
	// Multiple-choice answer mode doesn't use the input bar
	if m.inputMode == kitchenTUIInputMergeMenu || m.inputMode == kitchenTUIInputRemediate || (m.inputMode == kitchenTUIInputAnswer && m.selectedQuestionItem() != nil && len(m.selectedQuestionItem().Options) > 0) {
		return ""
	}
	label := "Input"
	switch m.inputMode {
	case kitchenTUIInputSubmit:
		label = "Submit"
		if m.submitResearch {
			label += " · research"
		} else if m.submitImplReview {
			label += " · impl review on"
		}
		if !m.submitResearch && len(m.submitDependsOn) > 0 {
			label += fmt.Sprintf(" · deps %d", len(m.submitDependsOn))
		}
		if !m.submitResearch && m.submitSelecting {
			label += " · selecting dependencies"
		}
	case kitchenTUIInputPromote:
		label = "Promote research"
	case kitchenTUIInputReplan:
		label = "Replan"
	case kitchenTUIInputAnswer:
		label = "Answer"
	case kitchenTUIInputRemediate:
		label = "Review remediation"
	}
	body := m.input.View()
	height := 3
	if m.inputMode == kitchenTUIInputSubmit && !m.submitResearch {
		depsLine := "Depends on: -"
		if len(m.submitDependsOn) > 0 {
			depsLine = "Depends on: " + strings.Join(m.submitDependsOn, ", ")
		}
		body += "\n" + truncate(depsLine, max(20, m.width-6))
		height = 4
	}
	return paneBox(m.width, height, label+"\n"+body)
}

func (m kitchenTUIModel) renderFooter() string {
	help := strings.Join(m.footerActions(), "  ")
	footer := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(help)
	if strings.TrimSpace(m.errText) != "" {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(m.errText)
	} else if strings.TrimSpace(m.flash) != "" {
		footer = lipgloss.NewStyle().Foreground(lipgloss.Color("84")).Render(m.flash)
	}
	return footer
}

func paneTitle(title string, focused bool) string {
	style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	if focused {
		style = style.Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Padding(0, 1)
	}
	return style.Render(title)
}

func paneBox(width, height int, body string) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(width).
		Height(height).
		Render(body)
}

func paneContentSize(width, height int) (innerWidth, innerHeight int) {
	innerWidth = max(1, width-4)
	innerHeight = max(1, height-2)
	return innerWidth, innerHeight
}

func buildPlanItems(plans []PlanRecord, snapshot tuiStatusSnapshot) []kitchenTUIPlanItem {
	progressByID := make(map[string]*PlanProgress, len(snapshot.Plans))
	for i := range snapshot.Plans {
		progress := snapshot.Plans[i]
		progressByID[progress.PlanID] = &progress
	}
	activePlanIDs := make(map[string]bool, len(snapshot.Lineages))
	for _, lineage := range snapshot.Lineages {
		if strings.TrimSpace(lineage.ActivePlan) != "" {
			activePlanIDs[lineage.ActivePlan] = true
		}
	}

	items := make([]kitchenTUIPlanItem, 0, len(plans))
	for _, plan := range plans {
		items = append(items, kitchenTUIPlanItem{
			Record:   plan,
			Progress: progressByID[plan.PlanID],
			Active:   activePlanIDs[plan.PlanID],
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Record.UpdatedAt.After(items[j].Record.UpdatedAt)
	})
	return items
}

func buildTaskItems(detail *PlanDetail, snapshot tuiStatusSnapshot) []kitchenTUITaskItem {
	if detail == nil {
		return nil
	}
	taskSummaryByID := make(map[string]pool.TaskSummary, len(snapshot.Queue.Tasks))
	for _, task := range snapshot.Queue.Tasks {
		taskSummaryByID[task.ID] = task
	}
	if strings.TrimSpace(detail.Plan.Mode) == "research" {
		return []kitchenTUITaskItem{buildResearchTaskItem(*detail, taskSummaryByID)}
	}

	var items []kitchenTUITaskItem
	var implementationTasks []PlanTask
	var trailingTimelineTasks []PlanTask
	for _, task := range detail.Plan.Tasks {
		if isLineageFixMergePlanTask(task.ID) {
			trailingTimelineTasks = append(trailingTimelineTasks, task)
			continue
		}
		implementationTasks = append(implementationTasks, task)
	}
	for _, cycle := range detail.Progress.Cycles {
		if cycle.PlannerTaskID != "" {
			items = append(items, buildCycleTaskItem("planner", cycle.PlannerTaskID, "Planner cycle "+fmt.Sprint(cycle.Index), cycle.PlannerTaskState, taskSummaryByID[cycle.PlannerTaskID]))
		}
		if cycle.ReviewTaskID != "" {
			items = append(items, buildCycleTaskItem("reviewer", cycle.ReviewTaskID, "Review cycle "+fmt.Sprint(cycle.Index), cycle.ReviewTaskState, taskSummaryByID[cycle.ReviewTaskID]))
		}
	}
	roundCount, reviewCyclesByRound := planExecutionRounds(*detail)
	if roundCount == 0 {
		roundCount = 1
	}
	for round := 1; round <= roundCount; round++ {
		for _, task := range implementationTasks {
			items = append(items, buildImplementationTaskItem(*detail, task, round, taskSummaryByID))
		}
		for _, cycle := range reviewCyclesByRound[round] {
			title := "Implementation review " + fmt.Sprint(max(1, cycle.Index))
			if cycle.ImplReviewCycle > 1 {
				title = fmt.Sprintf("Implementation review %d.%d", cycle.ImplReviewCycle, max(1, cycle.ImplReviewTurn))
			}
			items = append(items, buildCycleTaskItem("implementation-review", cycle.ImplReviewTaskID, title, cycle.ImplReviewTaskState, taskSummaryByID[cycle.ImplReviewTaskID]))
		}
	}
	for _, task := range orderTrailingTimelineTasks(trailingTimelineTasks, detail.History) {
		items = append(items, buildImplementationTaskItem(*detail, task, 1, taskSummaryByID))
	}
	return items
}

func isLineageFixMergePlanTask(taskID string) bool {
	return strings.HasPrefix(strings.TrimSpace(taskID), "fix-merge-")
}

func orderTrailingTimelineTasks(tasks []PlanTask, history []PlanHistoryEntry) []PlanTask {
	if len(tasks) <= 1 {
		return append([]PlanTask(nil), tasks...)
	}
	orderIndex := make(map[string]int, len(tasks))
	for idx, task := range tasks {
		orderIndex[strings.TrimSpace(task.ID)] = len(history) + idx
	}
	for idx, entry := range history {
		if strings.TrimSpace(entry.Type) != planHistoryLineageFixMergeRequested {
			continue
		}
		taskID := strings.TrimSpace(entry.TaskID)
		if _, ok := orderIndex[taskID]; ok {
			orderIndex[taskID] = idx
		}
	}
	ordered := append([]PlanTask(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return orderIndex[strings.TrimSpace(ordered[i].ID)] < orderIndex[strings.TrimSpace(ordered[j].ID)]
	})
	return ordered
}

type executionRoundInfo struct {
	ReviewRequested       bool
	MaxExplicitReviewTurn int
	MaxTerminalReviewTurn int
}

type reviewCycleIndex struct {
	turns  []int
	byTurn map[int]PlanCycleProgress
}

func planExecutionRounds(detail PlanDetail) (int, map[int][]PlanCycleProgress) {
	roundCount := 1
	currentRound := 1
	reviewSeenInRound := false
	rounds := map[int]*executionRoundInfo{
		1: {},
	}

	for _, entry := range detail.History {
		switch strings.TrimSpace(entry.Type) {
		case planHistoryImplReviewRequested:
			rounds[currentRound].ReviewRequested = true
			reviewSeenInRound = true
		case planHistoryReviewCouncilTurnCompleted:
			turn := reviewTurnForHistoryEntry(detail.Plan.PlanID, entry)
			if turn > rounds[currentRound].MaxExplicitReviewTurn {
				rounds[currentRound].MaxExplicitReviewTurn = turn
			}
			if turn > 0 {
				reviewSeenInRound = true
			}
		case planHistoryImplReviewPassed, planHistoryImplReviewFailed:
			turn := reviewTurnForHistoryEntry(detail.Plan.PlanID, entry)
			if turn > rounds[currentRound].MaxExplicitReviewTurn {
				rounds[currentRound].MaxExplicitReviewTurn = turn
			}
			if turn > rounds[currentRound].MaxTerminalReviewTurn {
				rounds[currentRound].MaxTerminalReviewTurn = turn
			}
			if turn > 0 {
				reviewSeenInRound = true
			}
		case planHistoryManualRetried, planHistoryConflictRetried:
			if reviewSeenInRound && isImplementationTaskRuntimeID(detail.Plan.PlanID, entry.TaskID) {
				currentRound++
				if currentRound > roundCount {
					roundCount = currentRound
				}
				if _, ok := rounds[currentRound]; !ok {
					rounds[currentRound] = &executionRoundInfo{}
				}
				reviewSeenInRound = false
			}
		}
	}

	reviews := reviewCyclesByTurn(detail.Progress.Cycles)
	grouped := make(map[int][]PlanCycleProgress)
	if len(reviews.turns) == 0 {
		return roundCount, grouped
	}

	reviewRounds := make([]int, 0, roundCount)
	for round := 1; round <= roundCount; round++ {
		info, ok := rounds[round]
		if !ok {
			info = &executionRoundInfo{}
			rounds[round] = info
		}
		if info.ReviewRequested || info.MaxExplicitReviewTurn > 0 {
			reviewRounds = append(reviewRounds, round)
		}
	}
	if len(reviewRounds) == 0 {
		reviewRounds = append(reviewRounds, 1)
	}

	nextTurn := 0
	for idx, round := range reviewRounds {
		info := rounds[round]
		for nextTurn < len(reviews.turns) && reviews.turns[nextTurn] <= info.MaxExplicitReviewTurn {
			turn := reviews.turns[nextTurn]
			grouped[round] = append(grouped[round], reviews.byTurn[turn])
			nextTurn++
		}
		if idx == len(reviewRounds)-1 {
			for nextTurn < len(reviews.turns) {
				turn := reviews.turns[nextTurn]
				grouped[round] = append(grouped[round], reviews.byTurn[turn])
				nextTurn++
			}
			continue
		}
		remainingRounds := len(reviewRounds) - idx - 1
		if info.MaxTerminalReviewTurn == 0 && nextTurn < len(reviews.turns) && len(reviews.turns)-nextTurn > remainingRounds {
			turn := reviews.turns[nextTurn]
			grouped[round] = append(grouped[round], reviews.byTurn[turn])
			nextTurn++
		}
	}

	return roundCount, grouped
}

func reviewCyclesByTurn(cycles []PlanCycleProgress) reviewCycleIndex {
	index := reviewCycleIndex{byTurn: make(map[int]PlanCycleProgress)}
	for _, cycle := range cycles {
		if strings.TrimSpace(cycle.ImplReviewTaskID) == "" {
			continue
		}
		turn := max(1, cycle.Index)
		index.turns = append(index.turns, turn)
		index.byTurn[turn] = cycle
	}
	sort.Ints(index.turns)
	return index
}

func reviewTurnForHistoryEntry(planID string, entry PlanHistoryEntry) int {
	if entry.Cycle > 0 {
		return entry.Cycle
	}
	return reviewCouncilTurnNumberFromTaskID(planID, entry.TaskID)
}

func isImplementationTaskRuntimeID(planID, taskID string) bool {
	planID = strings.TrimSpace(planID)
	taskID = strings.TrimSpace(taskID)
	if planID == "" || taskID == "" {
		return false
	}
	if !strings.HasPrefix(taskID, planID+"-") {
		return false
	}
	return councilTurnNumberFromTaskID(planID, taskID) == 0 && reviewCouncilTurnNumberFromTaskID(planID, taskID) == 0
}

func buildImplementationTaskItem(detail PlanDetail, task PlanTask, round int, taskSummaryByID map[string]pool.TaskSummary) kitchenTUITaskItem {
	runtimeID := planTaskRuntimeID(detail.Plan.PlanID, task.ID)
	state := planTaskRuntimeState(detail, task.ID)
	summary := taskSummaryByID[runtimeID]
	if strings.TrimSpace(summary.Status) != "" {
		state = summary.Status
	}
	return kitchenTUITaskItem{
		ID:          task.ID,
		RuntimeID:   runtimeID,
		RowKey:      fmt.Sprintf("%s#round-%d", runtimeID, max(1, round)),
		Kind:        "implementation",
		Title:       firstNonEmpty(task.Title, task.ID),
		State:       state,
		WorkerID:    summary.WorkerID,
		Complexity:  string(task.Complexity),
		Prompt:      task.Prompt,
		Summary:     summarizeTaskHistory(detail.History, runtimeID),
		HasConflict: summary.HasConflict,
	}
}

func buildResearchTaskItem(detail PlanDetail, taskSummaryByID map[string]pool.TaskSummary) kitchenTUITaskItem {
	runtimeID := "research_" + detail.Plan.PlanID
	summary := taskSummaryByID[runtimeID]
	state := string(planProgressTaskState(detail.Execution, runtimeID, nil))
	if strings.TrimSpace(summary.Status) != "" {
		state = summary.Status
	}
	return kitchenTUITaskItem{
		ID:        runtimeID,
		RuntimeID: runtimeID,
		RowKey:    runtimeID,
		Kind:      "research",
		Title:     firstNonEmpty(strings.TrimSpace(detail.Plan.Title), "Research"),
		State:     state,
		WorkerID:  summary.WorkerID,
		Prompt:    strings.TrimSpace(detail.Plan.Summary),
		Summary:   strings.TrimSpace(detail.Execution.ResearchOutput),
	}
}

func buildCycleTaskItem(kind, runtimeID, title, state string, summary pool.TaskSummary) kitchenTUITaskItem {
	if strings.TrimSpace(summary.Status) != "" {
		state = summary.Status
	}
	return kitchenTUITaskItem{
		ID:        runtimeID,
		RuntimeID: runtimeID,
		RowKey:    runtimeID,
		Kind:      kind,
		Title:     title,
		State:     state,
		WorkerID:  summary.WorkerID,
	}
}

func planDisplayState(plan kitchenTUIPlanItem) string {
	if implementationReviewFailed(plan.Progress) {
		return planStateImplementationReviewFailed
	}
	if plan.Progress != nil && strings.TrimSpace(plan.Progress.State) != "" {
		return plan.Progress.State
	}
	return firstNonEmpty(plan.Record.State, "-")
}

func implementationReviewFailed(progress *PlanProgress) bool {
	return progress != nil &&
		progress.ImplReviewRequested &&
		strings.TrimSpace(progress.ImplReviewStatus) == planReviewStatusFailed
}

func hasCouncilState(exec ExecutionRecord) bool {
	if exec.CouncilMaxTurns > 0 || exec.CouncilTurnsCompleted > 0 || strings.TrimSpace(exec.CouncilFinalDecision) != "" || len(exec.CouncilTurns) > 0 || len(exec.CouncilWarnings) > 0 || len(exec.CouncilUnresolvedDisagreements) > 0 {
		return true
	}
	for _, seat := range exec.CouncilSeats {
		if strings.TrimSpace(seat.Seat) != "" || strings.TrimSpace(seat.WorkerID) != "" || seat.IdleSince != nil || seat.Rehydrated {
			return true
		}
	}
	return false
}

func hasReviewCouncilState(exec ExecutionRecord) bool {
	if exec.ReviewCouncilMaxTurns > 0 || exec.ReviewCouncilTurnsCompleted > 0 || strings.TrimSpace(exec.ReviewCouncilFinalDecision) != "" || len(exec.ReviewCouncilTurns) > 0 || len(exec.ReviewCouncilWarnings) > 0 || len(exec.ReviewCouncilUnresolvedDisagreements) > 0 {
		return true
	}
	for _, seat := range exec.ReviewCouncilSeats {
		if strings.TrimSpace(seat.Seat) != "" || strings.TrimSpace(seat.WorkerID) != "" || seat.IdleSince != nil || seat.Rehydrated {
			return true
		}
	}
	return false
}

func appendCouncilDisagreements(lines []string, heading string, items []adapter.CouncilDisagreement, width int) []string {
	if len(items) == 0 {
		return lines
	}
	lines = append(lines, heading)
	for _, item := range items {
		lines = appendWrappedPrefixed(lines, "- ", fmt.Sprintf("[%s] %s", firstNonEmpty(item.Severity, "unknown"), firstNonEmpty(item.Title, item.ID)), width)
		lines = appendWrappedPrefixed(lines, "  Impact: ", item.Impact, width)
		if strings.TrimSpace(item.SuggestedChange) != "" {
			lines = appendWrappedPrefixed(lines, "  Suggested: ", item.SuggestedChange, width)
		}
	}
	return lines
}

func appendWrappedPrefixed(lines []string, prefix, text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return lines
	}
	if width <= len(prefix)+1 {
		return append(lines, prefix+text)
	}
	for _, line := range wrapText(text, width-len(prefix)) {
		lines = append(lines, prefix+line)
	}
	return lines
}

func countBlockingCouncilQuestions(items []adapter.CouncilUserQuestion) int {
	count := 0
	for _, item := range items {
		if item.Blocking {
			count++
		}
	}
	return count
}

func renderCouncilTurnDiffLines(prev, curr *adapter.PlanArtifact, width, budget int) []string {
	diff := councilTurnDiff(prev, curr)
	if diff.IsInitial {
		return nil
	}
	if !diff.HasChanges {
		return []string{"  no change (auto-converge eligible)"}
	}
	lines := make([]string, 0, 12)
	if diff.TitleChanged {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("title: %s -> %s", firstNonEmpty(diff.PrevTitle, "-"), firstNonEmpty(diff.CurrTitle, "-")), max(1, width))
	}
	if diff.SummaryChanged {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("summary: %s -> %s", firstNonEmpty(diff.PrevSummary, "-"), firstNonEmpty(diff.CurrSummary, "-")), max(1, width))
	}
	if diff.LineageChanged {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("lineage: %s -> %s", firstNonEmpty(diff.PrevLineage, "-"), firstNonEmpty(diff.CurrLineage, "-")), max(1, width))
	}
	if diff.OwnershipChanged {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("ownership: %s -> %s", firstNonEmpty(diff.PrevOwnership, "-"), firstNonEmpty(diff.CurrOwnership, "-")), max(1, width))
	}
	if diff.TaskCountDelta != 0 {
		lines = append(lines, fmt.Sprintf("  task count delta: %+d", diff.TaskCountDelta))
	}
	if diff.TaskOrderChanged {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("task order: %s -> %s", firstNonEmpty(diff.PrevTaskOrder, "-"), firstNonEmpty(diff.CurrTaskOrder, "-")), max(1, width))
	}
	for _, item := range diff.AddedTasks {
		lines = appendWrappedPrefixed(lines, "  ", "added task: "+item, max(1, width))
	}
	for _, item := range diff.RemovedTasks {
		lines = appendWrappedPrefixed(lines, "  ", "removed task: "+item, max(1, width))
	}
	for _, item := range diff.RenamedTasks {
		lines = appendWrappedPrefixed(lines, "  ", fmt.Sprintf("renamed task %s: %s -> %s", item.ID, firstNonEmpty(item.PrevTitle, "-"), firstNonEmpty(item.CurrTitle, "-")), max(1, width))
	}
	for _, item := range diff.PromptChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	for _, item := range diff.ComplexityChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	for _, item := range diff.DependencyChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	for _, item := range diff.FileListChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	for _, item := range diff.ArtifactListChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	for _, item := range diff.SuccessCriteriaChanges {
		lines = appendWrappedPrefixed(lines, "  ", item, max(1, width))
	}
	if len(lines) == 0 {
		lines = append(lines, "  candidate changed")
	}
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	trimmed := append([]string(nil), lines[:budget-1]...)
	trimmed = append(trimmed, fmt.Sprintf("  … (%d more changes)", len(lines)-(budget-1)))
	return trimmed
}

func formatShortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func planTaskRuntimeState(detail PlanDetail, logicalTaskID string) string {
	taskID := planTaskRuntimeID(detail.Plan.PlanID, logicalTaskID)
	if contains(detail.Progress.ActiveTaskIDs, taskID) {
		return "active"
	}
	if contains(detail.Progress.CompletedTaskIDs, taskID) {
		return "completed"
	}
	if contains(detail.Progress.FailedTaskIDs, taskID) {
		return "failed"
	}
	return "planned"
}

func summarizeTaskHistory(history []PlanHistoryEntry, runtimeID string) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].TaskID == runtimeID {
			return summarizePlanHistoryEntry(history[i])
		}
	}
	return ""
}

func pendingQuestionCountForPlan(planID string, questions []pool.Question) int {
	if strings.TrimSpace(planID) == "" {
		return 0
	}
	prefix := strings.TrimSpace(planID) + "-"
	count := 0
	for _, q := range questions {
		if strings.HasPrefix(strings.TrimSpace(q.TaskID), prefix) {
			count++
		}
	}
	return count
}

func pendingQuestionSummaryForPlan(planID string, questions []pool.Question) string {
	if strings.TrimSpace(planID) == "" {
		return "-"
	}
	var ids []string
	prefix := strings.TrimSpace(planID) + "-"
	for _, q := range questions {
		if strings.HasPrefix(strings.TrimSpace(q.TaskID), prefix) {
			ids = append(ids, q.ID)
		}
	}
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ", ")
}

func renderSelectableRow(selected bool, primary, secondary string) []string {
	if selected {
		main := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)
		sub := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("252"))
		return []string{main.Render(primary), sub.Render(secondary)}
	}
	return []string{
		primary,
		lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(secondary),
	}
}

func selectableItemWindow(totalItems, selected, visibleItems int) (start, end int) {
	if totalItems <= 0 {
		return 0, 0
	}
	if visibleItems <= 0 || visibleItems >= totalItems {
		return 0, totalItems
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= totalItems {
		selected = totalItems - 1
	}
	start = selected - visibleItems + 1
	if start < 0 {
		start = 0
	}
	end = start + visibleItems
	if end > totalItems {
		end = totalItems
		start = end - visibleItems
	}
	if start < 0 {
		start = 0
	}
	return start, end
}

func fitListLines(lines []string, height int) string {
	if height <= 0 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	out := make([]string, 0, height)
	out = append(out, lines...)
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func fitAndWrapLines(lines []string, width, height int) string {
	rendered, _ := windowAndWrapLines(lines, width, height, 0)
	return rendered
}

func windowAndWrapLines(lines []string, width, height, offset int) (string, int) {
	if width <= 0 {
		width = 1
	}
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, chunk := range splitRenderedLines(line) {
			if strings.TrimSpace(chunk) == "" {
				wrapped = append(wrapped, "")
				continue
			}
			if ansi.StringWidth(chunk) <= width {
				wrapped = append(wrapped, chunk)
				continue
			}
			for _, item := range wrapText(chunk, width) {
				wrapped = append(wrapped, truncate(item, width))
			}
		}
	}
	total := len(wrapped)
	if height <= 0 {
		return "", total
	}
	if offset < 0 {
		offset = 0
	}
	maxOffset := total - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := offset + height
	if end > total {
		end = total
	}
	view := append([]string(nil), wrapped[offset:end]...)
	for len(view) < height {
		view = append(view, "")
	}
	return strings.Join(view, "\n"), total
}

func splitRenderedLines(line string) []string {
	line = strings.ReplaceAll(line, "\r\n", "\n")
	line = strings.ReplaceAll(line, "\r", "\n")
	return strings.Split(line, "\n")
}

func sanitizeInlineTerminalText(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, s)
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func wrapText(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(paragraph)
		line := words[0]
		for _, word := range words[1:] {
			if len(line)+1+len(word) <= width {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func padRight(v string, width int) string {
	if len(v) >= width {
		return v[:width]
	}
	return v + strings.Repeat(" ", width-len(v))
}

func truncate(v string, width int) string {
	if width <= 0 || len(v) <= width {
		return v
	}
	if width <= 1 {
		return v[:width]
	}
	return v[:width-1] + "…"
}

func shortenSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func kitchenTUITick() tea.Cmd {
	return tea.Tick(tuiRefreshInterval, func(t time.Time) tea.Msg {
		return kitchenTUITickMsg(t)
	})
}

func summarizeMergeCheck(resp map[string]any) string {
	clean, _ := resp["clean"].(bool)
	baseBranch, _ := resp["baseBranch"].(string)
	conflicts := stringSliceFromAny(resp["conflicts"])
	summary := fmt.Sprintf("merge-check: clean=%t base=%s", clean, baseBranch)
	if len(conflicts) > 0 {
		summary += " conflicts=" + strings.Join(conflicts, ", ")
	}
	return summary
}

func summarizeMerge(resp map[string]any) string {
	status, _ := resp["status"].(string)
	baseBranch, _ := resp["baseBranch"].(string)
	mode, _ := resp["mode"].(string)
	if status == "" {
		status = "merged"
	}
	return fmt.Sprintf("%s %s into %s", status, mode, baseBranch)
}

func summarizeReapply(resp map[string]any) string {
	status, _ := resp["status"].(string)
	baseBranch, _ := resp["baseBranch"].(string)
	conflicts := stringSliceFromAny(resp["conflicts"])
	switch strings.TrimSpace(status) {
	case "up-to-date":
		return fmt.Sprintf("reapply: up-to-date on %s", baseBranch)
	case "fix-merge-queued":
		newTaskID, _ := resp["newTaskId"].(string)
		summary := fmt.Sprintf("reapply: fix-merge queued on %s", baseBranch)
		if strings.TrimSpace(newTaskID) != "" {
			summary += " task=" + newTaskID
		}
		if len(conflicts) > 0 {
			summary += " files=" + strings.Join(conflicts, ", ")
		}
		return summary
	case "conflicts":
		summary := fmt.Sprintf("reapply: conflicts on %s", baseBranch)
		if len(conflicts) > 0 {
			summary += " files=" + strings.Join(conflicts, ", ")
		}
		return summary
	default:
		newAnchor, _ := resp["newAnchor"].(string)
		summary := fmt.Sprintf("reapply: %s from %s", firstNonEmpty(status, "reapplied"), baseBranch)
		if strings.TrimSpace(newAnchor) != "" {
			summary += " @" + shortenSHA(newAnchor)
		}
		return summary
	}
}

func stringSliceFromAny(v any) []string {
	switch got := v.(type) {
	case []string:
		return append([]string(nil), got...)
	case []any:
		out := make([]string, 0, len(got))
		for _, item := range got {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func rowMarker(selected bool) string {
	if selected {
		return ">"
	}
	return " "
}

func compactState(state string) string {
	switch strings.TrimSpace(state) {
	case planStatePendingApproval:
		return "await"
	case planStatePlanningFailed:
		return "failed"
	case planStatePlanning:
		return "plan"
	case planStateReviewing:
		return "review"
	case planStateImplementationReviewFailed:
		return "impl-fail"
	case planStateImplementationReview:
		return "impl-rev"
	case planStateActive:
		return "active"
	case planStateCompleted:
		return "done"
	case planStateMerged:
		return "merged"
	case planStateWaitingOnDependency:
		return "wait-dep"
	case pool.TaskQueued:
		return "queued"
	case pool.TaskDispatched:
		return "active"
	case pool.TaskFailed:
		return "failed"
	case pool.TaskCanceled:
		return "cancel"
	case "":
		return "-"
	default:
		return state
	}
}

func defaultSubmitAnchorRef(repoPath string) string {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return ""
	}
	branch, err := runGit(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		branch = strings.TrimSpace(branch)
		if branch != "" && branch != "HEAD" {
			return branch
		}
	}
	commit, err := runGit(repoPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(commit)
}

func parseSubmitInput(value string) (idea, anchorRef string, err error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[ref=") {
		end := strings.Index(value, "]")
		if end < 0 {
			return "", "", fmt.Errorf("submit reference must close with ]")
		}
		anchorRef = strings.TrimSpace(value[len("[ref="):end])
		if anchorRef == "" {
			return "", "", fmt.Errorf("submit reference must not be empty")
		}
		value = strings.TrimSpace(value[end+1:])
	}
	if value == "" {
		return "", "", fmt.Errorf("idea must not be empty")
	}
	return value, anchorRef, nil
}

func stripSubmitAnchorPrefix(value string) string {
	value = strings.TrimSpace(value)
	idea, _, err := parseSubmitInput(value)
	if err == nil {
		return idea
	}
	if strings.HasPrefix(value, "[ref=") {
		if end := strings.Index(value, "]"); end >= 0 {
			return strings.TrimSpace(value[end+1:])
		}
	}
	return value
}
