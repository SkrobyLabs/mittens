package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (k *Kitchen) RouteQuestion(workerID, taskID, question string) (string, error) {
	if k == nil || k.pm == nil {
		return "", fmt.Errorf("kitchen pool manager not configured")
	}
	workerID = strings.TrimSpace(workerID)
	taskID = strings.TrimSpace(taskID)
	question = strings.TrimSpace(question)
	if workerID == "" {
		return "", fmt.Errorf("worker ID must not be empty")
	}
	if question == "" {
		return "", fmt.Errorf("question must not be empty")
	}

	questionID, err := k.pm.AskQuestion(workerID, pool.Question{
		TaskID:   taskID,
		Question: question,
		Blocking: true,
	})
	if err != nil {
		return "", err
	}
	_ = k.recordQuestionAffinity(questionID)
	return questionID, nil
}

func (k *Kitchen) AnswerQuestion(questionID, answer string) error {
	if k == nil || k.pm == nil {
		return fmt.Errorf("kitchen pool manager not configured")
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fmt.Errorf("answer must not be empty")
	}
	planID, _ := k.planIDForQuestion(questionID)
	if err := k.pm.AnswerQuestion(questionID, answer, "operator"); err != nil {
		return err
	}
	if err := k.recordQuestionAffinity(questionID); err != nil {
		return err
	}
	return k.autoApproveReadyPlan(planID)
}

func (k *Kitchen) MarkUnhelpful(questionID string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}
	planID, err := k.planIDForQuestion(questionID)
	if err != nil {
		return err
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	bundle.Affinity.LastQuestionID = questionID
	bundle.Affinity.PlannerWorkerID = ""
	bundle.Affinity.PreferredProviders = nil
	bundle.Affinity.Invalidated = true
	bundle.Affinity.InvalidationReason = "operator_marked_unhelpful"
	bundle.Affinity.InvalidatedAt = &now
	return k.planStore.UpdateAffinity(planID, bundle.Affinity)
}

func (k *Kitchen) recordQuestionAffinity(questionID string) error {
	if k == nil || k.planStore == nil {
		return nil
	}
	planID, err := k.planIDForQuestion(questionID)
	if err != nil {
		return nil
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	bundle.Affinity.LastQuestionID = questionID
	return k.planStore.UpdateAffinity(planID, bundle.Affinity)
}

func (k *Kitchen) planIDForQuestion(questionID string) (string, error) {
	if k == nil || k.pm == nil {
		return "", fmt.Errorf("kitchen pool manager not configured")
	}
	q := k.pm.GetQuestion(questionID)
	if q == nil {
		return "", fmt.Errorf("question %s not found", questionID)
	}
	if q.TaskID == "" {
		return "", fmt.Errorf("question %s is not attached to a planned task", questionID)
	}
	task, ok := k.pm.Task(q.TaskID)
	if !ok || strings.TrimSpace(task.PlanID) == "" {
		return "", fmt.Errorf("question %s is not attached to a planned task", questionID)
	}
	return task.PlanID, nil
}

func (k *Kitchen) autoApproveReadyPlan(planID string) error {
	if k == nil || k.planStore == nil || k.pm == nil || strings.TrimSpace(planID) == "" {
		return nil
	}
	if len(pendingQuestionsForPlan(k.pm, planID)) != 0 {
		return nil
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	if !bundle.Execution.AutoApproved || bundle.Execution.State != planStatePendingApproval {
		return nil
	}
	return k.ApprovePlan(planID)
}
