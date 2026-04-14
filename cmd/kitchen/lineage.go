package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const activePlanLinkName = "active_plan"

type LineageManager struct {
	lineagesDir string
	plansDir    string
}

type LineageState struct {
	Name       string `json:"name"`
	ActivePlan string `json:"activePlan,omitempty"`
}

func NewLineageManager(lineagesDir, plansDir string) *LineageManager {
	if lineagesDir == "" {
		return nil
	}
	_ = os.MkdirAll(lineagesDir, 0755)
	return &LineageManager{lineagesDir: lineagesDir, plansDir: plansDir}
}

func (lm *LineageManager) ActivatePlan(lineage, planID string) error {
	if lm == nil {
		return fmt.Errorf("lineage manager not configured")
	}
	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return err
	}
	lineageDir := filepath.Join(lm.lineagesDir, lineage)
	if err := os.MkdirAll(lineageDir, 0755); err != nil {
		return fmt.Errorf("create lineage dir: %w", err)
	}

	currentPlan, err := lm.ActivePlan(lineage)
	if err == nil && currentPlan != "" && currentPlan != planID {
		return fmt.Errorf("lineage %s already has active plan %s", lineage, currentPlan)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	activePath := filepath.Join(lineageDir, activePlanLinkName)
	_ = os.Remove(activePath)
	target := filepath.Join("..", "..", "plans", planID)
	if err := os.Symlink(target, activePath); err == nil {
		return nil
	}
	return os.WriteFile(activePath, []byte(planID+"\n"), 0644)
}

func (lm *LineageManager) ActivePlan(lineage string) (string, error) {
	if lm == nil {
		return "", fmt.Errorf("lineage manager not configured")
	}
	if err := validatePathComponent("lineage", lineage); err != nil {
		return "", err
	}
	activePath := filepath.Join(lm.lineagesDir, lineage, activePlanLinkName)
	info, err := os.Lstat(activePath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(activePath)
		if err != nil {
			return "", fmt.Errorf("read active plan link: %w", err)
		}
		return filepath.Base(target), nil
	}

	data, err := os.ReadFile(activePath)
	if err != nil {
		return "", fmt.Errorf("read active plan file: %w", err)
	}
	return string(bytesTrimSpace(data)), nil
}

// ClearActivePlan removes the active-plan marker for `lineage` when it
// currently points at `planID`. If the marker is missing or claims a
// different plan (for example because another plan took over the
// lineage after a rename, or this plan was already cleared), it
// returns nil — callers deleting/cancelling/merging a plan should
// never be blocked by someone else's active marker, they simply
// don't own it.
func (lm *LineageManager) ClearActivePlan(lineage, planID string) error {
	if lm == nil {
		return fmt.Errorf("lineage manager not configured")
	}
	if strings.TrimSpace(lineage) == "" {
		return nil
	}
	currentPlan, err := lm.ActivePlan(lineage)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if planID != "" && currentPlan != planID {
		return nil
	}
	return os.Remove(filepath.Join(lm.lineagesDir, lineage, activePlanLinkName))
}

func (lm *LineageManager) List() ([]LineageState, error) {
	if lm == nil {
		return nil, fmt.Errorf("lineage manager not configured")
	}
	entries, err := os.ReadDir(lm.lineagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read lineages dir: %w", err)
	}

	var out []LineageState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state := LineageState{Name: entry.Name()}
		activePlan, err := lm.ActivePlan(entry.Name())
		if err == nil {
			state.ActivePlan = activePlan
		}
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func bytesTrimSpace(data []byte) []byte {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	end := len(data)
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}
