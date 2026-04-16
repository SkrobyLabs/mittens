package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

// Set by -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Kitchen is the top-level orchestrator that will own the scheduler,
// persistence, and API surfaces as the implementation plan lands.
type Kitchen struct {
	pm              *pool.PoolManager
	wal             *pool.WAL
	hostAPI         pool.RuntimeAPI
	router          *ComplexityRouter
	health          *ProviderHealth
	planStore       *PlanStore
	lineageMgr      *LineageManager
	workerBkr       *WorkerBroker
	scheduler       *Scheduler
	apiServer       *http.Server
	notifyMu        sync.RWMutex
	notifySubs      map[int]chan pool.Notification
	notifySeq       int
	repoMutex       sync.Mutex
	councilResumeMu sync.Mutex
	councilExtendMu sync.Mutex
	cfg             KitchenConfig
	repoPath        string
	paths           KitchenPaths
	project         ProjectPaths
	// keepDeadWorkers retains finished worker containers via docker
	// instead of removing them when their task ends, for post-mortem
	// debugging. When set, the scheduler evicts the oldest retained
	// container before spawning if the total would exceed MaxWorkersTotal.
	keepDeadWorkers bool
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "kitchen: %v\n", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var submitLineage string
	var submitAuto bool
	var submitImplReview bool
	var submitFile string
	var submitAnchorRef string
	var submitDependsOn []string
	var plansCompleted bool
	var historyCycle int
	var historyJSON bool
	var evidenceCompact bool
	var statusHistoryLimit int
	var configPathsOnly bool
	var capabilitiesCLIOnly bool
	var replanReason string
	var remediateIncludeNits bool
	var steerFile string
	var steerImplementationFile string
	var retrySameWorker bool
	var mergeNoCommit bool
	var researchFile string
	var promoteLineage string
	var promoteAuto bool
	var promoteImplReview bool
	var promoteNoImplReview bool
	var serveAddr string
	var serveToken string
	var serveProvider string
	var brokerAddr string
	var brokerToken string
	var advertiseAddr string
	var keepDeadWorkers bool

	rootCmd := &cobra.Command{
		Use:           "kitchen",
		Short:         "Kitchen orchestration control plane",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKitchenTUI(".")
		},
	}

	tuiCmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive Kitchen terminal UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKitchenTUI(".")
		},
	}

	submitCmd := &cobra.Command{
		Use:   "submit [--lineage LINEAGE] [--anchor-ref REF] [--auto] [--depends-on PLAN_ID] [--file PATH|-] [IDEA]",
		Short: "Submit an idea for planning",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idea, err := resolveSubmitIdea(cmd, args, submitFile)
			if err != nil {
				return err
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.SubmitIdeaAt(idea, submitLineage, submitAuto, submitImplReview, submitAnchorRef, nil, submitDependsOn...)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			bundle, err := k.SubmitIdeaAt(idea, submitLineage, submitAuto, submitImplReview, nil, submitAnchorRef, submitDependsOn...)
			if err != nil {
				return err
			}
			resp := map[string]any{
				"planId":          bundle.Plan.PlanID,
				"state":           bundle.Execution.State,
				"lineage":         bundle.Plan.Lineage,
				"councilMaxTurns": bundle.Execution.CouncilMaxTurns,
			}
			return writeJSON(cmd.OutOrStdout(), resp)
		},
	}
	submitCmd.Flags().StringVar(&submitLineage, "lineage", "", "lineage to submit the idea into")
	submitCmd.Flags().StringVar(&submitAnchorRef, "anchor-ref", "", "git branch or commit to anchor the plan to (defaults to current)")
	submitCmd.Flags().BoolVar(&submitAuto, "auto", false, "auto-approve the generated plan")
	submitCmd.Flags().StringVar(&submitFile, "file", "", "read the idea body from a file path or '-' for stdin")
	submitCmd.Flags().BoolVar(&submitImplReview, "impl-review", false, "request a post-implementation adversarial review")
	submitCmd.Flags().StringSliceVar(&submitDependsOn, "depends-on", nil, "plan IDs this plan depends on (repeatable)")

	plansCmd := &cobra.Command{
		Use:   "plans",
		Short: "List plans",
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				plans, err := client.ListPlans(plansCompleted)
				if err != nil {
					return err
				}
				for _, plan := range plans {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", plan.PlanID, plan.State, plan.Lineage, plan.Title)
				}
				return nil
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			plans, err := k.ListPlans(plansCompleted)
			if err != nil {
				return err
			}
			for _, plan := range plans {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", plan.PlanID, plan.State, plan.Lineage, plan.Title)
			}
			return nil
		},
	}
	plansCmd.Flags().BoolVar(&plansCompleted, "completed", false, "include completed plans")

	planCmd := &cobra.Command{
		Use:   "plan PLAN_ID",
		Short: "Show one plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				detail, err := client.PlanDetail(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), detail)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			detail, err := k.PlanDetail(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}

	evidenceCmd := &cobra.Command{
		Use:   "evidence PLAN_ID",
		Short: "Show execution evidence for one plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tier := evidenceTierRich
			if evidenceCompact {
				tier = evidenceTierCompact
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				evidence, err := client.Evidence(args[0], tier)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), evidence)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			evidence, err := k.EvidenceWithTier(args[0], tier)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), evidence)
		},
	}
	evidenceCmd.Flags().BoolVar(&evidenceCompact, "compact", false, "emit the compact evidence tier instead of the default rich payload")

	historyCmd := &cobra.Command{
		Use:   "history PLAN_ID",
		Short: "Show condensed planning history for one plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				payload, history, err := client.PlanHistory(args[0], historyCycle)
				if err != nil {
					return err
				}
				if historyJSON {
					return writeJSON(cmd.OutOrStdout(), payload)
				}
				return writePlanHistory(cmd.OutOrStdout(), history)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			history, err := k.PlanHistory(args[0], historyCycle)
			if err != nil {
				return err
			}
			if historyJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"planId":  args[0],
					"cycle":   historyCycle,
					"history": history,
				})
			}
			return writePlanHistory(cmd.OutOrStdout(), history)
		},
	}
	historyCmd.Flags().IntVar(&historyCycle, "cycle", 0, "show only one planning/review cycle")
	historyCmd.Flags().BoolVar(&historyJSON, "json", false, "emit history as JSON")

	approveCmd := &cobra.Command{
		Use:   "approve PLAN_ID",
		Short: "Approve a plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.ApprovePlan(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.ApprovePlan(args[0]); err != nil {
				return err
			}
			bundle, err := k.GetPlan(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": bundle.Execution.State})
		},
	}

	rejectCmd := &cobra.Command{
		Use:   "reject PLAN_ID",
		Short: "Reject a plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.RejectPlan(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.RejectPlan(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": planStateRejected})
		},
	}

	replanCmd := &cobra.Command{
		Use:   "replan PLAN_ID",
		Short: "Start a fresh planning pass for a plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.ReplanPlan(args[0], replanReason)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			newPlanID, err := k.Replan(args[0], replanReason)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"newPlanId": newPlanID})
		},
	}
	replanCmd.Flags().StringVar(&replanReason, "reason", "", "optional reason to append to the replanned summary")

	reviewCmd := &cobra.Command{
		Use:   "review PLAN_ID",
		Short: "Trigger a manual implementation review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.RequestReview(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.RequestReview(args[0]); err != nil {
				return err
			}
			detail, err := k.PlanDetail(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}

	steerCmd := &cobra.Command{
		Use:   "steer PLAN_ID [--file PATH|-] [NOTE]",
		Short: "Append directional guidance to an in-progress planning council",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			planID := ""
			if len(args) > 0 {
				planID = strings.TrimSpace(args[0])
			}
			if planID == "" {
				return fmt.Errorf("plan ID must not be empty")
			}
			noteArgs := args[1:]
			note, err := resolveSubmitIdea(cmd, noteArgs, steerFile)
			if err != nil {
				return err
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				detail, err := client.SteerPlan(planID, note)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), detail)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.SteerPlan(planID, note); err != nil {
				return err
			}
			detail, err := k.PlanDetail(planID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}
	steerCmd.Flags().StringVar(&steerFile, "file", "", "read the steering note from a file path or '-' for stdin")

	steerImplementationCmd := &cobra.Command{
		Use:   "steer-implementation PLAN_ID [--file PATH|-] [NOTE]",
		Short: "Queue implementation guidance on the existing lineage without a full replan",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			planID := ""
			if len(args) > 0 {
				planID = strings.TrimSpace(args[0])
			}
			if planID == "" {
				return fmt.Errorf("plan ID must not be empty")
			}
			noteArgs := args[1:]
			note, err := resolveSubmitIdea(cmd, noteArgs, steerImplementationFile)
			if err != nil {
				return err
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				detail, err := client.SteerImplementation(planID, note)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), detail)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.SteerImplementation(planID, note); err != nil {
				return err
			}
			detail, err := k.PlanDetail(planID)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}
	steerImplementationCmd.Flags().StringVar(&steerImplementationFile, "file", "", "read the implementation steering note from a file path or '-' for stdin")

	remediateReviewCmd := &cobra.Command{
		Use:   "remediate-review PLAN_ID",
		Short: "Queue a manual remediation task from a passed implementation review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.RemediateReview(args[0], remediateIncludeNits)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.RemediateReview(args[0], remediateIncludeNits); err != nil {
				return err
			}
			detail, err := k.PlanDetail(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), detail)
		},
	}
	remediateReviewCmd.Flags().BoolVar(&remediateIncludeNits, "include-nits", false, "include nit findings in the remediation task")

	cancelCmd := &cobra.Command{
		Use:   "cancel PLAN_ID",
		Short: "Cancel a plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.CancelPlan(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.CancelPlan(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": "cancelled"})
		},
	}

	deleteCmd := &cobra.Command{
		Use:   "delete PLAN_ID",
		Short: "Delete a plan and its tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.DeletePlan(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.DeletePlan(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": "deleted"})
		},
	}

	retryCmd := &cobra.Command{
		Use:   "retry TASK_ID",
		Short: "Retry a failed task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requireFreshWorker := !retrySameWorker
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.RetryTask(args[0], requireFreshWorker)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.RetryTask(args[0], requireFreshWorker); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"status":             "retried",
				"taskId":             args[0],
				"requireFreshWorker": requireFreshWorker,
			})
		},
	}
	retryCmd.Flags().BoolVar(&retrySameWorker, "same-worker", false, "allow retrying on any eligible idle worker instead of requiring a fresh worker")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show queue and worker status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				snapshot, err := client.Status(statusHistoryLimit)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), snapshot)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			snapshot, err := k.StatusSnapshotWithLimit(statusHistoryLimit)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), snapshot)
		},
	}
	statusCmd.Flags().IntVar(&statusHistoryLimit, "history-limit", -1, "override embedded plan-history entries in the status snapshot; 0 disables history")

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Show effective Kitchen config and paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			payload := map[string]any{
				"config": k.cfg,
				"paths": map[string]any{
					"home":      k.paths.HomeDir,
					"config":    k.paths.ConfigPath,
					"state":     k.paths.StateDir,
					"projects":  k.paths.ProjectsDir,
					"worktrees": k.paths.WorktreesDir,
					"repo":      k.repoPath,
				},
			}
			if configPathsOnly {
				payload = map[string]any{
					"paths": payload["paths"],
				}
			}
			return writeJSON(cmd.OutOrStdout(), payload)
		},
	}
	configCmd.Flags().BoolVar(&configPathsOnly, "paths", false, "show only resolved Kitchen paths")

	capabilitiesCmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Show machine-readable Kitchen capability metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := kitchenCapabilities()
			if capabilitiesCLIOnly {
				payload = map[string]any{
					"meta": payload["meta"],
					"cli":  payload["cli"],
				}
			}
			return writeJSON(cmd.OutOrStdout(), payload)
		},
	}
	capabilitiesCmd.Flags().BoolVar(&capabilitiesCLIOnly, "cli", false, "show only CLI capability metadata")

	questionsCmd := &cobra.Command{
		Use:   "questions",
		Short: "List pending operator questions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				questions, err := client.ListQuestions()
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"questions": questions})
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			return writeJSON(cmd.OutOrStdout(), map[string]any{"questions": k.ListQuestions()})
		},
	}

	answerCmd := &cobra.Command{
		Use:   "answer QUESTION_ID ANSWER",
		Short: "Answer a pending operator question",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.AnswerQuestion(args[0], args[1])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.AnswerQuestion(args[0], args[1]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": "answered"})
		},
	}

	unhelpfulCmd := &cobra.Command{
		Use:   "unhelpful QUESTION_ID",
		Short: "Mark a question answering path as unhelpful",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.MarkUnhelpful(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.MarkUnhelpful(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": "recorded"})
		},
	}

	mergeCmd := &cobra.Command{
		Use:   "merge LINEAGE",
		Short: "Squash-merge a lineage branch into its base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.MergeLineage(args[0], mergeNoCommit, false)
				if err != nil {
					return err
				}
				if !mergeNoCommit {
					confirmed, err := maybeConfirmFallbackMerge(cmd, resp, func() (map[string]any, error) {
						return client.MergeLineage(args[0], false, true)
					})
					if err != nil {
						return err
					}
					resp = confirmed
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			var resp map[string]any
			if mergeNoCommit {
				resp, err = k.PreviewMergeLineage(args[0])
			} else {
				resp, err = k.MergeLineageWithOptions(args[0], false)
				if err == nil {
					resp, err = maybeConfirmFallbackMerge(cmd, resp, func() (map[string]any, error) {
						return k.MergeLineageWithOptions(args[0], true)
					})
				}
			}
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), resp)
		},
	}
	mergeCmd.Flags().BoolVar(&mergeNoCommit, "no-commit", false, "preview the merge result without updating the base branch")

	mergeCheckCmd := &cobra.Command{
		Use:   "merge-check LINEAGE",
		Short: "Check whether a lineage can merge cleanly",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.MergeCheck(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			lineage := args[0]
			gitMgr, err := k.gitManager()
			if err != nil {
				return err
			}
			baseBranch := k.baseBranchForLineage(lineage)
			clean, conflicts, err := gitMgr.MergeCheck(lineage, baseBranch)
			if err != nil {
				return err
			}
			currentHead, err := runGit(k.repoPath, "rev-parse", lineageBranchName(lineage))
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"clean":       clean,
				"conflicts":   conflicts,
				"baseBranch":  baseBranch,
				"currentHead": strings.TrimSpace(currentHead),
			})
		},
	}

	reapplyCmd := &cobra.Command{
		Use:   "reapply LINEAGE",
		Short: "Merge base branch into lineage to absorb upstream changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.ReapplyLineage(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			resp, err := k.ReapplyLineage(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), resp)
		},
	}

	fixMergeCmd := &cobra.Command{
		Use:   "fix-merge LINEAGE",
		Short: "Queue a worker to resolve lineage→base merge conflicts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.FixLineageConflicts(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			newTaskID, err := k.FixLineageConflicts(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"newTaskId": newTaskID})
		},
	}

	lineagesCmd := &cobra.Command{
		Use:   "lineages",
		Short: "List lineage state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				lineages, err := client.ListLineages()
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), map[string]any{"lineages": lineages})
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			lineages, err := k.lineageMgr.List()
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{"lineages": lineages})
		},
	}

	providerResetCmd := &cobra.Command{
		Use:   "reset PROVIDER/MODEL",
		Short: "Reset provider health for one provider/model key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.ResetProviderKey(args[0])
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			if err := k.ResetProviderKey(args[0]); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]string{"status": "reset"})
		},
	}

	providerCmd := &cobra.Command{
		Use:   "provider",
		Short: "Provider health management",
	}
	providerCmd.AddCommand(providerResetCmd)

	cleanCmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove completed lineage worktrees",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			removed, err := k.CleanWorktrees()
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"status":  "cleaned",
				"count":   len(removed),
				"removed": removed,
			})
		},
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Kitchen HTTP API",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			var (
				hostAPI  pool.RuntimeAPI
				hostPool []PoolKey
				err      error
			)
			var supervised []*supervisedDaemon
			stopSupervised := func() {
				for i := len(supervised) - 1; i >= 0; i-- {
					_ = supervised[i].Stop()
				}
			}
			defer stopSupervised()

			useExternalRuntime := strings.TrimSpace(os.Getenv("MITTENS_RUNTIME_SOCKET")) != "" && strings.TrimSpace(os.Getenv("MITTENS_POOL_TOKEN")) != ""
			if strings.TrimSpace(serveProvider) != "" || !useExternalRuntime {
				paths, err := DefaultKitchenPaths()
				if err != nil {
					return err
				}
				mittensPath, err := resolveMittensBinary()
				if err != nil {
					return err
				}
				var providers []string
				if strings.TrimSpace(serveProvider) != "" {
					providers = []string{serveProvider}
				} else {
					cfg, err := LoadKitchenConfig(paths.ConfigPath)
					if err != nil {
						return err
					}
					providers, err = configuredServeProviders(cfg)
					if err != nil {
						return err
					}
				}
				clients := make(map[string]pool.RuntimeAPI, len(providers))
				for _, provider := range providers {
					project, err := paths.Project(".")
					if err != nil {
						stopSupervised()
						return err
					}
					daemon, err := startSupervisedDaemon(paths, project, provider, mittensPath, cmd.ErrOrStderr())
					if err != nil {
						stopSupervised()
						return err
					}
					supervised = append(supervised, daemon)
					client := daemon.RuntimeClient()
					hostPool = append(hostPool, daemon.HostPool()...)
					clients[daemon.provider] = client
				}
				switch len(clients) {
				case 0:
					hostAPI = nil
				case 1:
					for _, client := range clients {
						hostAPI = client
					}
				default:
					hostAPI = newRuntimeMux(clients)
				}
			}

			// If the user left the default --addr / --broker-addr flags,
			// probe a small range for free ports so a second `kitchen
			// serve` (for a different repo) doesn't collide with the
			// first. Explicit flag values are honored as-is and will
			// fail hard on bind conflict below.
			if !cmd.Flags().Changed("addr") {
				if picked, err := pickAvailableAddr(serveAddr, 20); err == nil {
					serveAddr = picked
				}
			}
			if !cmd.Flags().Changed("broker-addr") {
				if picked, err := pickAvailableAddr(brokerAddr, 20, serveAddr); err == nil {
					brokerAddr = picked
				}
			}

			k, closeFn, err := openKitchenWithOptions(".", kitchenOpenOptions{
				hostAPI:         hostAPI,
				hostPool:        hostPool,
				keepDeadWorkers: keepDeadWorkers,
			})
			if err != nil {
				return err
			}
			defer closeFn()

			resolvedBrokerToken := strings.TrimSpace(brokerToken)
			if resolvedBrokerToken == "" {
				resolvedBrokerToken = strings.TrimSpace(serveToken)
			}
			workerAddr, err := k.StartRuntime(ctx, brokerAddr, resolvedBrokerToken, advertiseAddr)
			if err != nil {
				return err
			}

			server := &http.Server{
				Handler: k.NewAPIHandler(serveToken),
			}
			listener, err := net.Listen("tcp", serveAddr)
			if err != nil {
				return err
			}
			serverURL := "http://" + listener.Addr().String()
			if err := writeServeMetadata(k.project, k.repoPath, serverURL, serveToken); err != nil {
				_ = listener.Close()
				return err
			}
			defer removeServeMetadata(k.project)
			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
			}()
			fmt.Fprintf(cmd.OutOrStdout(), "Kitchen API listening on %s\n", listener.Addr().String())
			fmt.Fprintf(cmd.OutOrStdout(), "Kitchen worker broker listening on %s\n", workerAddr)
			err = server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	serveCmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:7681", "address for the Kitchen API server")
	serveCmd.Flags().StringVar(&serveToken, "token", os.Getenv("KITCHEN_API_TOKEN"), "optional shared API token")
	serveCmd.Flags().StringVar(&serveProvider, "provider", "", "supervise one child mittens daemon for this provider (claude, codex, gemini)")
	serveCmd.Flags().StringVar(&brokerAddr, "broker-addr", "127.0.0.1:7682", "listen address for the Kitchen worker broker")
	serveCmd.Flags().StringVar(&brokerToken, "broker-token", os.Getenv("KITCHEN_BROKER_TOKEN"), "shared token for the Kitchen worker broker")
	serveCmd.Flags().StringVar(&advertiseAddr, "advertise-addr", "", "worker broker address advertised to spawned workers")
	serveCmd.Flags().BoolVar(&keepDeadWorkers, "keep-dead-workers", false, "retain finished worker containers for debugging; oldest is evicted when the container count reaches maxWorkersTotal")

	researchCmd := &cobra.Command{
		Use:   "research [--file PATH|-] [TOPIC]",
		Short: "Submit a research topic for read-only investigation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic, err := resolveSubmitIdea(cmd, args, researchFile)
			if err != nil {
				return err
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.SubmitResearch(topic)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			bundle, err := k.SubmitResearch(topic)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"planId": bundle.Plan.PlanID,
				"state":  bundle.Execution.State,
				"mode":   bundle.Plan.Mode,
			})
		},
	}
	researchCmd.Flags().StringVar(&researchFile, "file", "", "read the research topic from a file path or '-' for stdin")

	promoteCmd := &cobra.Command{
		Use:   "promote PLAN_ID [--lineage LINEAGE] [--auto] [--impl-review]",
		Short: "Promote completed research into an implementation plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			implReview := true
			if cmd.Flags().Changed("impl-review") {
				implReview = promoteImplReview
			}
			if cmd.Flags().Changed("no-impl-review") && promoteNoImplReview {
				implReview = false
			}
			if client, ok, err := openKitchenAPIClient("."); err != nil {
				return err
			} else if ok {
				resp, err := client.PromoteResearch(args[0], promoteLineage, promoteAuto, implReview)
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), resp)
			}

			k, closeFn, err := openKitchen(".")
			if err != nil {
				return err
			}
			defer closeFn()

			bundle, err := k.PromoteResearch(args[0], promoteLineage, promoteAuto, implReview)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"planId":          bundle.Plan.PlanID,
				"state":           bundle.Execution.State,
				"lineage":         bundle.Plan.Lineage,
				"researchPlanId":  bundle.Plan.ResearchPlanID,
				"councilMaxTurns": bundle.Execution.CouncilMaxTurns,
			})
		},
	}
	promoteCmd.Flags().StringVar(&promoteLineage, "lineage", "", "lineage for the implementation plan")
	promoteCmd.Flags().BoolVar(&promoteAuto, "auto", false, "auto-approve the generated plan")
	promoteCmd.Flags().BoolVar(&promoteImplReview, "impl-review", true, "request a post-implementation adversarial review (default true)")
	promoteCmd.Flags().BoolVar(&promoteNoImplReview, "no-impl-review", false, "skip post-implementation adversarial review")

	configureCmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure complexity models and provider policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigure()
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "kitchen %s (%s, %s)\n", version, commit, date)
			return nil
		},
	}

	mittensCmd := &cobra.Command{
		Use:                "mittens [mittens args...]",
		Short:              "Launch mittens with the kitchen home directory mounted",
		Long:               "Resolves the mittens binary next to kitchen, then execs it with --dir $KITCHEN_HOME (default ~/.kitchen) so the running AI has read-write access to kitchen's plans, lineages, and config. Any additional flags/args are forwarded to mittens.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
				return cmd.Help()
			}
			mittensPath, err := resolveMittensBinary()
			if err != nil {
				return fmt.Errorf("locate mittens binary: %w", err)
			}
			kitchenHome, err := DefaultKitchenHome()
			if err != nil {
				return err
			}
			if info, err := os.Stat(kitchenHome); err != nil {
				return fmt.Errorf("kitchen home %q: %w", kitchenHome, err)
			} else if !info.IsDir() {
				return fmt.Errorf("kitchen home %q is not a directory", kitchenHome)
			}
			mittensArgs := append([]string{"mittens", "--dir", kitchenHome}, args...)
			return syscall.Exec(mittensPath, mittensArgs, os.Environ())
		},
	}

	rootCmd.AddCommand(
		submitCmd,
		researchCmd,
		promoteCmd,
		plansCmd,
		planCmd,
		evidenceCmd,
		historyCmd,
		approveCmd,
		rejectCmd,
		replanCmd,
		reviewCmd,
		steerCmd,
		steerImplementationCmd,
		remediateReviewCmd,
		cancelCmd,
		deleteCmd,
		retryCmd,
		statusCmd,
		configCmd,
		capabilitiesCmd,
		questionsCmd,
		answerCmd,
		unhelpfulCmd,
		lineagesCmd,
		mergeCmd,
		mergeCheckCmd,
		reapplyCmd,
		fixMergeCmd,
		providerCmd,
		cleanCmd,
		configureCmd,
		tuiCmd,
		serveCmd,
		mittensCmd,
		versionCmd,
	)

	return rootCmd
}

func notImplemented(feature string) error {
	return fmt.Errorf("%s not implemented yet", feature)
}

// pickAvailableAddr probes ports starting at the port in base and walks
// upward by 1 until it finds one it can bind, up to maxTries candidates.
// Ports in exclude are skipped even if free.
// Used for default --addr / --broker-addr only, so that a second
// `kitchen serve` on the same host doesn't fail with EADDRINUSE. There
// is an inherent TOCTOU window between probe-close and actual-bind; if
// another process races us, the caller's net.Listen will surface the
// collision.
func pickAvailableAddr(base string, maxTries int, exclude ...string) (string, error) {
	host, portStr, err := net.SplitHostPort(base)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", base, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", fmt.Errorf("parse port %q: %w", portStr, err)
	}
	if maxTries <= 0 {
		maxTries = 1
	}
	excluded := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		excluded[e] = struct{}{}
	}
	for i := 0; i < maxTries; i++ {
		candidate := net.JoinHostPort(host, strconv.Itoa(port+i))
		if _, skip := excluded[candidate]; skip {
			continue
		}
		ln, err := net.Listen("tcp", candidate)
		if err == nil {
			_ = ln.Close()
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free port in range %d-%d on %s", port, port+maxTries-1, host)
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func resolveSubmitIdea(cmd *cobra.Command, args []string, filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if len(args) > 0 && filePath != "" {
		return "", fmt.Errorf("submit accepts either IDEA or --file, not both")
	}
	if filePath != "" {
		return readSubmitIdeaFromFile(filePath, cmd.InOrStdin())
	}
	if len(args) > 0 {
		idea := strings.TrimSpace(args[0])
		if idea == "" {
			return "", fmt.Errorf("idea must not be empty")
		}
		return idea, nil
	}
	if idea, present, err := readSubmitIdeaFromReader(cmd.InOrStdin()); err != nil {
		return "", err
	} else if present {
		if idea == "" {
			return "", fmt.Errorf("stdin is empty")
		}
		return idea, nil
	}
	return readSubmitIdeaFromEditor(cmd)
}

func readSubmitIdeaFromFile(filePath string, stdin io.Reader) (string, error) {
	if filePath == "-" {
		idea, present, err := readSubmitIdeaFromReader(stdin)
		if err != nil {
			return "", err
		}
		if !present || idea == "" {
			return "", fmt.Errorf("stdin is empty")
		}
		return idea, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	idea := strings.TrimSpace(string(data))
	if idea == "" {
		return "", fmt.Errorf("submit file %s is empty", filePath)
	}
	return idea, nil
}

func readSubmitIdeaFromReader(r io.Reader) (string, bool, error) {
	if r == nil {
		return "", false, nil
	}
	if f, ok := r.(*os.File); ok {
		info, err := f.Stat()
		if err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return "", false, nil
		}
	}
	if sized, ok := r.(interface{ Len() int }); ok && sized.Len() == 0 {
		return "", false, nil
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", true, err
	}
	return strings.TrimSpace(string(data)), true, nil
}

func readSubmitIdeaFromEditor(cmd *cobra.Command) (string, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return "", fmt.Errorf("submit requires IDEA, --file, piped stdin, or $EDITOR/$VISUAL")
	}

	tmp, err := os.CreateTemp("", "kitchen-submit-*.md")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	defer os.Remove(path)

	editorCmd := exec.Command("sh", "-lc", editor+" "+shellQuote(path))
	editorCmd.Stdin = cmd.InOrStdin()
	editorCmd.Stdout = cmd.ErrOrStderr()
	editorCmd.Stderr = cmd.ErrOrStderr()
	if err := editorCmd.Run(); err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	idea := strings.TrimSpace(string(data))
	if idea == "" {
		return "", fmt.Errorf("editor exited without writing an idea")
	}
	return idea, nil
}

func maybeConfirmFallbackMerge(cmd *cobra.Command, resp map[string]any, confirm func() (map[string]any, error)) (map[string]any, error) {
	if cmd == nil {
		return resp, nil
	}
	status, _ := resp["status"].(string)
	if status != "needs_fallback_confirmation" {
		return resp, nil
	}
	reason, _ := resp["error"].(string)
	fallback, _ := resp["fallbackCommitMessage"].(string)
	fmt.Fprintf(cmd.ErrOrStderr(), "LLM squash commit message generation failed: %s\n", strings.TrimSpace(reason))
	if strings.TrimSpace(fallback) != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Fallback commit message: %s\n", strings.TrimSpace(fallback))
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Continue with fallback commit message? [y/N]: ")
	reader := bufio.NewReader(cmd.InOrStdin())
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return map[string]any{
			"status":     "not_merged",
			"baseBranch": resp["baseBranch"],
			"mode":       resp["mode"],
			"reason":     "operator declined fallback commit message",
		}, nil
	}
	return confirm()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
