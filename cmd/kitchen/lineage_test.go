package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLineageManagerActivateReadAndClear(t *testing.T) {
	root := t.TempDir()
	manager := NewLineageManager(filepath.Join(root, "lineages"), filepath.Join(root, "plans"))

	if err := manager.ActivatePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}

	active, err := manager.ActivePlan("parser-errors")
	if err != nil {
		t.Fatalf("ActivePlan: %v", err)
	}
	if active != "plan_1" {
		t.Fatalf("active plan = %q, want plan_1", active)
	}

	if err := manager.ClearActivePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ClearActivePlan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lineages", "parser-errors", activePlanLinkName)); !os.IsNotExist(err) {
		t.Fatalf("active plan marker still exists: %v", err)
	}
}

func TestLineageManagerClearActivePlanIgnoresForeignMarker(t *testing.T) {
	root := t.TempDir()
	manager := NewLineageManager(filepath.Join(root, "lineages"), filepath.Join(root, "plans"))

	if err := manager.ActivatePlan("shared", "plan_owner"); err != nil {
		t.Fatalf("ActivatePlan(plan_owner): %v", err)
	}

	// Another plan asks to clear the marker. It's not the owner, so
	// the marker must be preserved and no error returned — otherwise
	// DeletePlan / CancelPlan on superseded plans fails whenever the
	// lineage has since been claimed by a successor.
	if err := manager.ClearActivePlan("shared", "plan_other"); err != nil {
		t.Fatalf("ClearActivePlan(foreign plan) returned error: %v", err)
	}
	active, err := manager.ActivePlan("shared")
	if err != nil {
		t.Fatalf("ActivePlan after foreign clear: %v", err)
	}
	if active != "plan_owner" {
		t.Fatalf("active plan = %q, want marker preserved at plan_owner", active)
	}
}

func TestLineageManagerEnforcesOneActivePlan(t *testing.T) {
	root := t.TempDir()
	manager := NewLineageManager(filepath.Join(root, "lineages"), filepath.Join(root, "plans"))

	if err := manager.ActivatePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ActivatePlan(plan_1): %v", err)
	}
	if err := manager.ActivatePlan("parser-errors", "plan_2"); err == nil {
		t.Fatal("expected conflict when activating second plan")
	}
}
