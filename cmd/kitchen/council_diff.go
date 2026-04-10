package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
)

type CouncilTurnDiff struct {
	IsInitial              bool
	HasChanges             bool
	TitleChanged           bool
	PrevTitle              string
	CurrTitle              string
	SummaryChanged         bool
	PrevSummary            string
	CurrSummary            string
	LineageChanged         bool
	PrevLineage            string
	CurrLineage            string
	OwnershipChanged       bool
	PrevOwnership          string
	CurrOwnership          string
	TaskOrderChanged       bool
	PrevTaskOrder          string
	CurrTaskOrder          string
	AddedTasks             []string
	RemovedTasks           []string
	RenamedTasks           []TaskRename
	PromptChanges          []string
	ComplexityChanges      []string
	DependencyChanges      []string
	FileListChanges        []string
	ArtifactListChanges    []string
	SuccessCriteriaChanges []string
	TaskCountDelta         int
}

type TaskRename struct {
	ID        string
	PrevTitle string
	CurrTitle string
}

func councilTurnDiff(prev, curr *adapter.PlanArtifact) CouncilTurnDiff {
	if prev == nil || curr == nil {
		return CouncilTurnDiff{IsInitial: true}
	}
	if adapter.PlanArtifactsEqual(prev, curr) {
		return CouncilTurnDiff{}
	}

	diff := CouncilTurnDiff{
		TaskCountDelta: len(curr.Tasks) - len(prev.Tasks),
		HasChanges:     true,
	}
	prevTitle := strings.TrimSpace(prev.Title)
	currTitle := strings.TrimSpace(curr.Title)
	if prevTitle != currTitle {
		diff.TitleChanged = true
		diff.PrevTitle = prevTitle
		diff.CurrTitle = currTitle
	}
	prevSummary := strings.TrimSpace(prev.Summary)
	currSummary := strings.TrimSpace(curr.Summary)
	if prevSummary != currSummary {
		diff.SummaryChanged = true
		diff.PrevSummary = prevSummary
		diff.CurrSummary = currSummary
	}
	prevLineage := strings.TrimSpace(prev.Lineage)
	currLineage := strings.TrimSpace(curr.Lineage)
	if prevLineage != currLineage {
		diff.LineageChanged = true
		diff.PrevLineage = prevLineage
		diff.CurrLineage = currLineage
	}

	prevOwnership := formatPlanArtifactOwnership(prev.Ownership)
	currOwnership := formatPlanArtifactOwnership(curr.Ownership)
	if prevOwnership != currOwnership {
		diff.OwnershipChanged = true
		diff.PrevOwnership = prevOwnership
		diff.CurrOwnership = currOwnership
	}

	prevOrder := formatTaskOrder(prev.Tasks)
	currOrder := formatTaskOrder(curr.Tasks)
	if prevOrder != currOrder {
		diff.TaskOrderChanged = true
		diff.PrevTaskOrder = prevOrder
		diff.CurrTaskOrder = currOrder
	}

	prevByID := make(map[string]adapter.PlanArtifactTask, len(prev.Tasks))
	currByID := make(map[string]adapter.PlanArtifactTask, len(curr.Tasks))
	for _, task := range prev.Tasks {
		prevByID[strings.TrimSpace(task.ID)] = task
	}
	for _, task := range curr.Tasks {
		currByID[strings.TrimSpace(task.ID)] = task
	}

	for _, task := range curr.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			continue
		}
		name := firstNonEmpty(strings.TrimSpace(task.Title), id)
		prevTask, ok := prevByID[id]
		if !ok {
			diff.AddedTasks = append(diff.AddedTasks, name)
			continue
		}
		if strings.TrimSpace(prevTask.Title) != strings.TrimSpace(task.Title) {
			diff.RenamedTasks = append(diff.RenamedTasks, TaskRename{
				ID:        id,
				PrevTitle: strings.TrimSpace(prevTask.Title),
				CurrTitle: strings.TrimSpace(task.Title),
			})
		}
		if strings.TrimSpace(prevTask.Prompt) != strings.TrimSpace(task.Prompt) {
			diff.PromptChanges = append(diff.PromptChanges, fmt.Sprintf("%s prompt updated", name))
		}
		if strings.TrimSpace(prevTask.Complexity) != strings.TrimSpace(task.Complexity) ||
			strings.TrimSpace(prevTask.ReviewComplexity) != strings.TrimSpace(task.ReviewComplexity) {
			diff.ComplexityChanges = append(diff.ComplexityChanges, fmt.Sprintf("%s complexity: %s/%s -> %s/%s",
				name,
				firstNonEmpty(strings.TrimSpace(prevTask.Complexity), "-"),
				firstNonEmpty(strings.TrimSpace(prevTask.ReviewComplexity), "-"),
				firstNonEmpty(strings.TrimSpace(task.Complexity), "-"),
				firstNonEmpty(strings.TrimSpace(task.ReviewComplexity), "-"),
			))
		}
		if !adapterPlanStringSetEqual(prevTask.Dependencies, task.Dependencies) {
			diff.DependencyChanges = append(diff.DependencyChanges, fmt.Sprintf("%s deps: %s -> %s",
				name,
				formatTaskSet(prevTask.Dependencies),
				formatTaskSet(task.Dependencies),
			))
		}
		if fileChange := planArtifactListChange(name, "files", outputFiles(prevTask.Outputs), outputFiles(task.Outputs)); fileChange != "" {
			diff.FileListChanges = append(diff.FileListChanges, fileChange)
		}
		if artifactChange := planArtifactListChange(name, "artifacts", outputArtifacts(prevTask.Outputs), outputArtifacts(task.Outputs)); artifactChange != "" {
			diff.ArtifactListChanges = append(diff.ArtifactListChanges, artifactChange)
		}
		if !successCriteriaEqual(prevTask.SuccessCriteria, task.SuccessCriteria) {
			diff.SuccessCriteriaChanges = append(diff.SuccessCriteriaChanges, fmt.Sprintf("%s success criteria updated", name))
		}
	}
	for _, task := range prev.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			continue
		}
		if _, ok := currByID[id]; !ok {
			diff.RemovedTasks = append(diff.RemovedTasks, firstNonEmpty(strings.TrimSpace(task.Title), id))
		}
	}

	sort.Strings(diff.AddedTasks)
	sort.Strings(diff.RemovedTasks)
	sort.Slice(diff.RenamedTasks, func(i, j int) bool {
		return diff.RenamedTasks[i].ID < diff.RenamedTasks[j].ID
	})
	sort.Strings(diff.PromptChanges)
	sort.Strings(diff.ComplexityChanges)
	sort.Strings(diff.DependencyChanges)
	sort.Strings(diff.FileListChanges)
	sort.Strings(diff.ArtifactListChanges)
	sort.Strings(diff.SuccessCriteriaChanges)
	return diff
}

func adapterPlanStringSetEqual(a, b []string) bool {
	left := cleanSortedStrings(a)
	right := cleanSortedStrings(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func cleanSortedStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func formatTaskSet(items []string) string {
	cleaned := cleanSortedStrings(items)
	if len(cleaned) == 0 {
		return "-"
	}
	return strings.Join(cleaned, ", ")
}

func formatTaskOrder(tasks []adapter.PlanArtifactTask) string {
	items := make([]string, 0, len(tasks))
	for _, task := range tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			continue
		}
		items = append(items, firstNonEmpty(strings.TrimSpace(task.Title), id))
	}
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, " -> ")
}

func formatPlanArtifactOwnership(item *adapter.PlanArtifactOwnership) string {
	if item == nil {
		return "-"
	}
	scope := "shared"
	if item.Exclusive {
		scope = "exclusive"
	}
	pkgs := cleanSortedStrings(item.Packages)
	if len(pkgs) == 0 {
		return scope + " [-]"
	}
	return fmt.Sprintf("%s [%s]", scope, strings.Join(pkgs, ", "))
}

func outputFiles(item *adapter.PlanArtifactOutputs) []string {
	if item == nil {
		return nil
	}
	return item.Files
}

func outputArtifacts(item *adapter.PlanArtifactOutputs) []string {
	if item == nil {
		return nil
	}
	return item.Artifacts
}

func successCriteriaEqual(a, b *adapter.PlanArtifactSuccessCriteria) bool {
	if successCriteriaEmpty(a) && successCriteriaEmpty(b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(a.Advisory) == strings.TrimSpace(b.Advisory) &&
		adapterPlanStringSetEqual(a.Verifiable, b.Verifiable)
}

func successCriteriaEmpty(item *adapter.PlanArtifactSuccessCriteria) bool {
	return item == nil || (strings.TrimSpace(item.Advisory) == "" && len(cleanSortedStrings(item.Verifiable)) == 0)
}

func planArtifactListChange(taskName, label string, prev, curr []string) string {
	prevItems := cleanSortedStrings(prev)
	currItems := cleanSortedStrings(curr)
	added := stringDifference(currItems, prevItems)
	removed := stringDifference(prevItems, currItems)
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(added) > 0 {
		parts = append(parts, "+"+strings.Join(added, " +"))
	}
	if len(removed) > 0 {
		parts = append(parts, "-"+strings.Join(removed, " -"))
	}
	return fmt.Sprintf("%s %s: %s", taskName, label, strings.Join(parts, " "))
}

func stringDifference(left, right []string) []string {
	seen := make(map[string]bool, len(right))
	for _, item := range right {
		seen[item] = true
	}
	out := make([]string, 0, len(left))
	for _, item := range left {
		if seen[item] {
			continue
		}
		out = append(out, item)
	}
	return out
}
