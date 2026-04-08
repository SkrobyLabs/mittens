package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	ListQuestions() ([]pool.Question, error)
	SubmitIdea(idea string, implReview bool) (string, error)
	ExtendCouncil(planID string, turns int) error
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
func (b *kitchenAPIBackend) ListQuestions() ([]pool.Question, error) { return b.client.ListQuestions() }
func (b *kitchenAPIBackend) SubmitIdea(idea string, implReview bool) (string, error) {
	resp, err := b.client.SubmitIdea(idea, "", false, implReview)
	if err != nil {
		return "", err
	}
	planID, _ := resp["planId"].(string)
	if strings.TrimSpace(planID) == "" {
		return "", fmt.Errorf("submit response missing planId")
	}
	return planID, nil
}
func (b *kitchenAPIBackend) ExtendCouncil(planID string, turns int) error {
	_, err := b.client.ExtendCouncil(planID, turns)
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

func (b *kitchenLocalBackend) ListQuestions() ([]pool.Question, error) {
	var questions []pool.Question
	err := b.withKitchen(func(k *Kitchen) error {
		questions = k.ListQuestions()
		return nil
	})
	return questions, err
}

func (b *kitchenLocalBackend) SubmitIdea(idea string, implReview bool) (string, error) {
	var planID string
	err := b.withKitchen(func(k *Kitchen) error {
		bundle, err := k.SubmitIdea(idea, "", false, implReview)
		if err != nil {
			return err
		}
		planID = bundle.Plan.PlanID
		return nil
	})
	return planID, err
}

func (b *kitchenLocalBackend) ExtendCouncil(planID string, turns int) error {
	return b.withKitchen(func(k *Kitchen) error {
		return k.ExtendCouncil(planID, turns)
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
	status     tuiStatusSnapshot
	plans      []PlanRecord
	questions  []pool.Question
	detail     *PlanDetail
	taskLog    []pool.WorkerActivityRecord
	err        error
	newBackend kitchenTUIBackend
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
	kitchenTUIInputNone   kitchenTUIInputMode = ""
	kitchenTUIInputSubmit kitchenTUIInputMode = "submit"
	kitchenTUIInputReplan kitchenTUIInputMode = "replan"
	kitchenTUIInputAnswer kitchenTUIInputMode = "answer"

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
	leftMode          kitchenTUILeftMode
	taskPaneMode      kitchenTUITaskPaneMode
	detail            *PlanDetail
	taskLog           []pool.WorkerActivityRecord
	input             textinput.Model
	inputMode         kitchenTUIInputMode
	submitImplReview  bool
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
		backend:      backend,
		repoPath:     repoPath,
		input:        input,
		loading:      true,
		leftMode:     kitchenTUILeftPlans,
		taskPaneMode: kitchenTUITaskPaneDetail,
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
		if msg.newBackend != nil {
			m.backend = msg.newBackend
		}
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.errText = ""
		m.status = msg.status
		m.plans = buildPlanItems(msg.plans, msg.status)
		m.questions = append([]pool.Question(nil), msg.questions...)
		m.syncPlanSelection()
		m.detail = msg.detail
		m.taskLog = append([]pool.WorkerActivityRecord(nil), msg.taskLog...)
		m.tasks = buildTaskItems(m.detail, m.status)
		if m.selectedTask >= len(m.tasks) {
			m.selectedTask = max(0, len(m.tasks)-1)
		}
		if m.leftMode == kitchenTUILeftTasks && len(m.tasks) == 0 {
			m.leftMode = kitchenTUILeftPlans
			m.taskPaneMode = kitchenTUITaskPaneDetail
			m.taskLog = nil
		}
		if m.flash == "refreshing..." {
			m.flash = ""
			m.flashUntil = time.Time{}
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
				if m.selectedTask > 0 {
					m.selectedTask--
				}
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
					return m, m.loadCmd()
				}
				return m, nil
			}
			if m.leftMode == kitchenTUILeftQuestions {
				if m.selectedQuestion > 0 {
					m.selectedQuestion--
				}
				return m, nil
			}
			if m.selectedPlan > 0 {
				m.selectedPlan--
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				return m, m.loadCmd()
			}
			return m, nil
		case "down", "j":
			if m.leftMode == kitchenTUILeftTasks {
				if m.selectedTask < len(m.tasks)-1 {
					m.selectedTask++
				}
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
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
				}
				return m, nil
			}
			if m.selectedPlan < len(m.plans)-1 {
				m.selectedPlan++
				m.pendingSelectedID = m.selectedPlanID()
				m.selectedTask = 0
				return m, m.loadCmd()
			}
			return m, nil
		case "enter", "right", "l":
			if m.leftMode == kitchenTUILeftPlans && len(m.tasks) > 0 {
				m.leftMode = kitchenTUILeftTasks
				m.taskPaneMode = kitchenTUITaskPaneDetail
				return m, nil
			}
			if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneDetail && len(m.tasks) > 0 {
				m.taskPaneMode = kitchenTUITaskPaneLogs
				return m, m.loadCmd()
			}
			return m, nil
		case "left", "h", "backspace", "esc":
			if m.leftMode == kitchenTUILeftTasks {
				if m.taskPaneMode == kitchenTUITaskPaneLogs {
					m.taskPaneMode = kitchenTUITaskPaneDetail
					return m, nil
				}
				m.leftMode = kitchenTUILeftPlans
				m.taskPaneMode = kitchenTUITaskPaneDetail
			} else if m.leftMode == kitchenTUILeftQuestions {
				m.leftMode = kitchenTUILeftPlans
			}
			return m, nil
		case "?":
			if m.leftMode == kitchenTUILeftPlans {
				m.leftMode = kitchenTUILeftQuestions
				m.selectedQuestion = 0
			}
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
		case "c":
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
		case "m":
			if plan := m.selectedPlanItem(); plan != nil {
				return m, m.actionCmd(func() (string, string, error) {
					summary, err := m.backend.MergeCheck(plan.Record.Lineage)
					return summary, plan.Record.PlanID, err
				})
			}
			return m, nil
		case "M":
			if plan := m.selectedPlanItem(); plan != nil {
				return m, m.actionCmd(func() (string, string, error) {
					summary, err := m.backend.MergeLineage(plan.Record.Lineage)
					return summary, plan.Record.PlanID, err
				})
			}
			return m, nil
		case "F":
			if plan := m.selectedPlanItem(); plan != nil {
				return m, m.actionCmd(func() (string, string, error) {
					newTaskID, err := m.backend.FixLineageConflicts(plan.Record.Lineage)
					if err != nil {
						return "", plan.Record.PlanID, err
					}
					return "fix-merge task queued: " + newTaskID, plan.Record.PlanID, nil
				})
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

func (m kitchenTUIModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.closeInput()
		return m, nil
	case "tab":
		if m.inputMode == kitchenTUIInputSubmit {
			m.submitImplReview = !m.submitImplReview
			return m, nil
		}
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		switch m.inputMode {
		case kitchenTUIInputSubmit:
			if value == "" {
				m.errText = "idea must not be empty"
				return m, nil
			}
			implReview := m.submitImplReview
			m.closeInput()
			return m, m.actionCmd(func() (string, string, error) {
				planID, err := m.backend.SubmitIdea(value, implReview)
				if err != nil {
					return "", "", err
				}
				status := "submitted " + planID
				if implReview {
					status += " with impl review"
				}
				return status, planID, nil
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
	m.openInput(kitchenTUIInputSubmit, "Submit idea", "Add typed parser errors")
}

func (m *kitchenTUIModel) closeInput() {
	m.inputMode = kitchenTUIInputNone
	m.submitImplReview = false
	m.input.Blur()
	m.input.SetValue("")
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

func (m kitchenTUIModel) canApproveSelectedPlan() bool {
	plan := m.selectedPlanItem()
	return plan != nil && planDisplayState(*plan) == planStatePendingApproval
}

func (m kitchenTUIModel) canExtendSelectedPlan() bool {
	if m.detail == nil {
		return false
	}
	return canExtendCouncil(m.detail.Plan.State, m.detail.Execution)
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
	case "", "planned", pool.TaskCompleted, pool.TaskAccepted, pool.TaskFailed, pool.TaskCanceled, pool.TaskRejected, pool.TaskEscalated:
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
	case "", "cancelled", planStatePlanning, planStateReviewing, planStateImplementationReview, planStatePlanningFailed, planStateClosed, planStateRejected, planStateMerged:
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
			mode := "off"
			if m.submitImplReview {
				mode = "on"
			}
			return []string{"enter submit", "tab impl-review:" + mode, "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputReplan:
			return []string{"enter replan", "esc cancel", "ctrl+c quit"}
		case kitchenTUIInputAnswer:
			if q := m.selectedQuestionItem(); q != nil && len(q.Options) > 0 {
				return []string{"↑/↓ navigate", "enter select", "esc cancel", "ctrl+c quit"}
			}
			return []string{"enter answer", "esc cancel", "ctrl+c quit"}
		default:
			return []string{"esc cancel", "ctrl+c quit"}
		}
	}

	actions := []string{"n submit"}
	if m.leftMode == kitchenTUILeftTasks {
		if m.canCancelSelectedTask() {
			actions = append(actions, "c cancel")
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
		if m.canApproveSelectedPlan() {
			actions = append(actions, "a approve")
		}
		if m.canExtendSelectedPlan() {
			actions = append(actions, "e extend")
		}
		if m.canCancelSelectedPlan() {
			actions = append(actions, "c cancel")
		}
		if m.selectedPlanItem() != nil {
			actions = append(actions, "p replan")
			actions = append(actions, "D delete")
			if m.canCheckMergeSelectedPlan() {
				actions = append(actions, "m check")
			}
			if m.canMergeSelectedPlan() {
				actions = append(actions, "M merge")
			}
			actions = append(actions, "F fix-merge")
			actions = append(actions, "? questions")
		}
	}
	actions = append(actions, "r refresh", "q quit")
	return actions
}

func (m kitchenTUIModel) loadCmd() tea.Cmd {
	selectedPlanID := m.selectedPlanID()
	selectedTaskID := ""
	if m.leftMode == kitchenTUILeftTasks && m.taskPaneMode == kitchenTUITaskPaneLogs {
		if task := m.selectedTaskItem(); task != nil {
			selectedTaskID = strings.TrimSpace(task.RuntimeID)
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
				selectedTaskID = ""
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
		if selectedTaskID != "" {
			taskLog, err = backend.TaskActivity(selectedTaskID)
			if err != nil {
				return kitchenTUILoadedMsg{err: err, newBackend: upgraded}
			}
		}
		return kitchenTUILoadedMsg{
			status:     status,
			plans:      plans,
			questions:  questions,
			detail:     detail,
			taskLog:    taskLog,
			newBackend: upgraded,
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
	headline := fmt.Sprintf("Kitchen  %s  %s@%s", repo, branch, shortenSHA(m.status.Anchor.Commit))
	summary := fmt.Sprintf("backend=%s  workers=%d/%d  pendingQuestions=%d  plans=%d", m.backend.Label(), m.status.Queue.AliveWorkers, m.status.Queue.MaxWorkers, len(m.questions), len(m.plans))
	rows := []string{titleStyle.Render(headline), metaStyle.Render(summary)}
	// In local backend mode the TUI talks to a transient Kitchen that
	// does not run the scheduler, so any queued task will sit there
	// forever. Surface that clearly so the operator knows to start
	// `kitchen serve` in another terminal.
	if m.backend.Label() == "local" {
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
	title := paneTitle("Plans", true)
	if len(m.plans) == 0 {
		return paneBox(width, height, title+"\n\nNo plans.")
	}
	innerWidth := max(20, width-6)
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
		secondary := truncate(fmt.Sprintf("    lineage: %s  plan: %s", firstNonEmpty(plan.Record.Lineage, "-"), plan.Record.PlanID), innerWidth)
		lines = append(lines, renderSelectableRow(i == m.selectedPlan && m.leftMode == kitchenTUILeftPlans, primary, secondary)...)
	}
	return paneBox(width, height, title+"\n"+fitListLines(lines, height-1))
}

func (m kitchenTUIModel) renderTasksPane(width, height int) string {
	titleText := "Tasks"
	if plan := m.selectedPlanItem(); plan != nil {
		titleText = "Tasks · " + truncate(plan.Record.Title, max(10, width-18))
	}
	title := paneTitle(titleText, true)
	if len(m.tasks) == 0 {
		return paneBox(width, height, title+"\n\nSelected plan has no task list.")
	}
	innerWidth := max(20, width-6)
	lines := make([]string, 0, len(m.tasks)*2)
	for i, task := range m.tasks {
		marker := rowMarker(i == m.selectedTask && m.leftMode == kitchenTUILeftTasks)
		primary := truncate(fmt.Sprintf("%s %s %s", marker, padRight(compactState(task.State), 6), task.Title), innerWidth)
		secondary := truncate("    "+task.ID+" · "+task.Kind, innerWidth)
		lines = append(lines, renderSelectableRow(i == m.selectedTask && m.leftMode == kitchenTUILeftTasks, primary, secondary)...)
	}
	return paneBox(width, height, title+"\n"+fitListLines(lines, height-1))
}

func (m kitchenTUIModel) renderQuestionsPane(width, height int) string {
	titleText := "Questions"
	if plan := m.selectedPlanItem(); plan != nil {
		titleText = "Questions · " + truncate(plan.Record.Title, max(10, width-20))
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

	innerWidth := max(20, width-6)
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
	return paneBox(width, height, title+"\n"+fitListLines(lines, height-1))
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
	} else {
		lines = m.renderPlanDetailLines(innerWidth)
	}
	return paneBox(width, height, fitAndWrapLines(lines, innerWidth, height))
}

func (m kitchenTUIModel) renderQuestionDetailPane(width, height int) string {
	q := m.selectedQuestionItem()
	if q == nil {
		return paneBox(width, height, "Question Detail\n\nNo selected question.")
	}
	innerWidth := max(24, width-6)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	boldStyle := lipgloss.NewStyle().Bold(true)
	highlightStyle := lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230")).Bold(true)

	var lines []string

	// Header
	lines = append(lines, dimStyle.Render(fmt.Sprintf("Question %s · %s", q.ID, firstNonEmpty(q.Category, "general"))))
	lines = append(lines, "")

	// Full question text
	lines = append(lines, boldStyle.Render("Question:"))
	lines = append(lines, wrapText(q.Question, innerWidth)...)
	lines = append(lines, "")

	// Context block
	if strings.TrimSpace(q.Context) != "" {
		lines = append(lines, dimStyle.Render("Context:"))
		lines = append(lines, wrapText(q.Context, innerWidth)...)
		lines = append(lines, "")
	}

	// Provenance
	lines = append(lines, dimStyle.Render(fmt.Sprintf("Worker: %s  Task: %s", firstNonEmpty(q.WorkerID, "-"), firstNonEmpty(q.TaskID, "-"))))
	if !q.AskedAt.IsZero() {
		lines = append(lines, dimStyle.Render("Asked: "+q.AskedAt.UTC().Format("2006-01-02 15:04:05")))
	}
	lines = append(lines, "")

	// Already answered guard
	if q.Answered {
		lines = append(lines, boldStyle.Render("Answer: ")+q.Answer)
		return paneBox(width, height, fitAndWrapLines(lines, innerWidth, height))
	}

	// Options or free-text hint
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
	} else {
		if m.inputMode != kitchenTUIInputAnswer {
			lines = append(lines, dimStyle.Render("Press 'a' to answer"))
		}
	}

	return paneBox(width, height, fitAndWrapLines(lines, innerWidth, height))
}

func (m kitchenTUIModel) renderPlanDetailLines(innerWidth int) []string {
	detail := *m.detail
	state := firstNonEmpty(planDisplayState(kitchenTUIPlanItem{Record: detail.Plan, Progress: &detail.Progress}), detail.Execution.State, detail.Plan.State)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(detail.Plan.Title),
		fmt.Sprintf("Plan ID: %s", detail.Plan.PlanID),
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
	if detail.Progress.ImplReviewRequested {
		lines = append(lines, fmt.Sprintf("Implementation review: %s", firstNonEmpty(detail.Progress.ImplReviewStatus, "pending")))
		if len(detail.Progress.ImplReviewFindings) > 0 {
			lines = append(lines, "Implementation review findings:")
			for _, finding := range detail.Progress.ImplReviewFindings {
				lines = append(lines, wrapText("- "+finding, innerWidth)...)
			}
		}
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
			for _, turn := range detail.Execution.CouncilTurns {
				if turn.Artifact == nil {
					lines = append(lines, fmt.Sprintf("Turn %d [%s]", turn.Turn, firstNonEmpty(turn.Seat, "-")))
					continue
				}
				lines = append(lines, fmt.Sprintf("Turn %d [%s] %s", turn.Turn, firstNonEmpty(turn.Artifact.Seat, turn.Seat, "-"), firstNonEmpty(turn.Artifact.Stance, "-")))
				lines = appendWrappedPrefixed(lines, "  ", firstNonEmpty(turn.Artifact.Summary, turn.Artifact.SeatMemo), max(1, innerWidth))
				lines = append(lines, fmt.Sprintf("  disagreements: %d  blocking questions: %d", len(turn.Artifact.Disagreements), countBlockingCouncilQuestions(turn.Artifact.QuestionsForUser)))
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
	if task.Summary != "" {
		lines = append(lines, "", "Latest summary:")
		lines = append(lines, wrapText(task.Summary, innerWidth)...)
	}
	if task.Prompt != "" {
		lines = append(lines, "", "Prompt:")
		lines = append(lines, wrapText(task.Prompt, innerWidth)...)
	}
	return lines
}

func (m kitchenTUIModel) renderTaskLogLines(innerWidth int) []string {
	task := m.selectedTaskItem()
	if task == nil {
		return []string{"No selected task."}
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(task.Title + " · activity log"),
		fmt.Sprintf("Task ID: %s", task.ID),
		fmt.Sprintf("Runtime ID: %s", task.RuntimeID),
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
		if label == "" {
			label = "activity"
		}
		line := fmt.Sprintf("%s  %s", ts, label)
		if strings.TrimSpace(record.Activity.Summary) != "" {
			line += "  " + strings.TrimSpace(record.Activity.Summary)
		}
		lines = append(lines, line)
	}
	return lines
}

func (m kitchenTUIModel) renderInputBar() string {
	// Multiple-choice answer mode doesn't use the input bar
	if m.inputMode == kitchenTUIInputAnswer && m.selectedQuestionItem() != nil && len(m.selectedQuestionItem().Options) > 0 {
		return ""
	}
	label := "Input"
	switch m.inputMode {
	case kitchenTUIInputSubmit:
		label = "Submit"
		if m.submitImplReview {
			label += " · impl review on"
		}
	case kitchenTUIInputReplan:
		label = "Replan"
	case kitchenTUIInputAnswer:
		label = "Answer"
	}
	return paneBox(m.width, 3, label+"\n"+m.input.View())
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

	var items []kitchenTUITaskItem
	for _, cycle := range detail.Progress.Cycles {
		if cycle.PlannerTaskID != "" {
			items = append(items, buildCycleTaskItem("planner", cycle.PlannerTaskID, "Planner cycle "+fmt.Sprint(cycle.Index), cycle.PlannerTaskState, taskSummaryByID[cycle.PlannerTaskID]))
		}
		if cycle.ReviewTaskID != "" {
			items = append(items, buildCycleTaskItem("reviewer", cycle.ReviewTaskID, "Review cycle "+fmt.Sprint(cycle.Index), cycle.ReviewTaskState, taskSummaryByID[cycle.ReviewTaskID]))
		}
		if cycle.ImplReviewTaskID != "" {
			items = append(items, buildCycleTaskItem("implementation-review", cycle.ImplReviewTaskID, "Implementation review "+fmt.Sprint(max(1, cycle.Index)), cycle.ImplReviewTaskState, taskSummaryByID[cycle.ImplReviewTaskID]))
		}
	}
	for _, task := range detail.Plan.Tasks {
		runtimeID := planTaskRuntimeID(detail.Plan.PlanID, task.ID)
		state := planTaskRuntimeState(*detail, task.ID)
		summary := taskSummaryByID[runtimeID]
		if strings.TrimSpace(summary.Status) != "" {
			state = summary.Status
		}
		items = append(items, kitchenTUITaskItem{
			ID:          task.ID,
			RuntimeID:   runtimeID,
			Kind:        "implementation",
			Title:       firstNonEmpty(task.Title, task.ID),
			State:       state,
			WorkerID:    summary.WorkerID,
			Complexity:  string(task.Complexity),
			Prompt:      task.Prompt,
			Summary:     summarizeTaskHistory(detail.History, runtimeID),
			HasConflict: summary.HasConflict,
		})
	}
	return items
}

func buildCycleTaskItem(kind, runtimeID, title, state string, summary pool.TaskSummary) kitchenTUITaskItem {
	if strings.TrimSpace(summary.Status) != "" {
		state = summary.Status
	}
	return kitchenTUITaskItem{
		ID:        runtimeID,
		RuntimeID: runtimeID,
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
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		return ""
	}
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		for _, wrapped := range wrapText(line, width) {
			out = append(out, truncate(wrapped, width))
		}
	}
	if len(out) > height {
		out = out[:height]
	}
	return strings.Join(out, "\n")
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
