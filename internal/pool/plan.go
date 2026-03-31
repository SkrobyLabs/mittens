package pool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PlanStore manages persistent plans in a directory with one subdirectory per plan.
type PlanStore struct {
	plansDir string
}

// NewPlanStore creates a PlanStore rooted at plansDir.
// Returns nil if plansDir is empty.
func NewPlanStore(plansDir string) *PlanStore {
	if plansDir == "" {
		return nil
	}
	os.MkdirAll(plansDir, 0755)
	return &PlanStore{plansDir: plansDir}
}

// CreatePlan creates a new plan and returns its ID.
func (ps *PlanStore) CreatePlan(title, content string) (string, error) {
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte(title + now.Format(time.RFC3339Nano)))
	id := hex.EncodeToString(hash[:])[:8]

	planDir := filepath.Join(ps.plansDir, id)
	if err := os.MkdirAll(planDir, 0755); err != nil {
		return "", fmt.Errorf("create plan dir: %w", err)
	}

	// Write plan.md.
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write plan.md: %w", err)
	}

	// Write status.json atomically.
	plan := Plan{
		ID:        id,
		Title:     title,
		Status:    PlanPending,
		CreatedAt: now,
	}
	if err := ps.writeStatusAtomic(planDir, plan); err != nil {
		return "", fmt.Errorf("write status.json: %w", err)
	}

	return id, nil
}

// ClaimPlan marks a plan as active for the given session.
func (ps *PlanStore) ClaimPlan(planID, sessionID string) error {
	if err := ValidatePlanID(planID); err != nil {
		return err
	}
	return ps.withLock(planID, func() error {
		plan, err := ps.readStatus(planID)
		if err != nil {
			return err
		}
		if plan.Status != PlanPending && plan.Status != PlanOrphaned {
			return fmt.Errorf("plan %s is %q, expected pending or orphaned", planID, plan.Status)
		}
		plan.Status = PlanActive
		plan.Owner = sessionID
		plan.ClaimedAt = time.Now().UTC()
		return ps.writeStatusAtomic(filepath.Join(ps.plansDir, planID), plan)
	})
}

// UpdateProgress appends a timestamped entry to the plan's log.
func (ps *PlanStore) UpdateProgress(planID, entry string) error {
	if err := ValidatePlanID(planID); err != nil {
		return err
	}
	logPath := filepath.Join(ps.plansDir, planID, "log.md")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log.md: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s \u2014 %s\n", time.Now().UTC().Format(time.RFC3339), entry)
	return err
}

// CompletePlan marks a plan as completed.
func (ps *PlanStore) CompletePlan(planID string) error {
	if err := ValidatePlanID(planID); err != nil {
		return err
	}
	return ps.withLock(planID, func() error {
		plan, err := ps.readStatus(planID)
		if err != nil {
			return err
		}
		plan.Status = PlanCompleted
		plan.CompletedAt = time.Now().UTC()
		return ps.writeStatusAtomic(filepath.Join(ps.plansDir, planID), plan)
	})
}

// OrphanPlan marks a plan as orphaned and clears its owner.
func (ps *PlanStore) OrphanPlan(planID string) error {
	if err := ValidatePlanID(planID); err != nil {
		return err
	}
	return ps.withLock(planID, func() error {
		plan, err := ps.readStatus(planID)
		if err != nil {
			return err
		}
		plan.Status = PlanOrphaned
		plan.Owner = ""
		return ps.writeStatusAtomic(filepath.Join(ps.plansDir, planID), plan)
	})
}

// ListPlans returns all plans sorted by creation time (newest first).
func (ps *PlanStore) ListPlans() ([]Plan, error) {
	entries, err := os.ReadDir(ps.plansDir)
	if err != nil {
		return nil, fmt.Errorf("read plans dir: %w", err)
	}

	var plans []Plan
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		plan, err := ps.readStatus(e.Name())
		if err != nil {
			continue // skip malformed plans
		}
		plans = append(plans, plan)
	}

	sort.Slice(plans, func(i, j int) bool {
		return plans[i].CreatedAt.After(plans[j].CreatedAt)
	})
	return plans, nil
}

// ReadPlan returns the plan.md content for a plan.
func (ps *PlanStore) ReadPlan(planID string) (string, error) {
	if err := ValidatePlanID(planID); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(ps.plansDir, planID, "plan.md"))
	if err != nil {
		return "", fmt.Errorf("read plan %s: %w", planID, err)
	}
	return string(data), nil
}

// --- helpers ---

func (ps *PlanStore) readStatus(planID string) (Plan, error) {
	if err := ValidatePlanID(planID); err != nil {
		return Plan{}, err
	}
	data, err := os.ReadFile(filepath.Join(ps.plansDir, planID, "status.json"))
	if err != nil {
		return Plan{}, fmt.Errorf("read status for plan %s: %w", planID, err)
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Plan{}, fmt.Errorf("parse status for plan %s: %w", planID, err)
	}
	return plan, nil
}

func (ps *PlanStore) writeStatusAtomic(planDir string, plan Plan) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(planDir, "status.json.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(planDir, "status.json"))
}

func (ps *PlanStore) withLock(planID string, fn func() error) error {
	if err := ValidatePlanID(planID); err != nil {
		return err
	}
	lockPath := filepath.Join(ps.plansDir, planID, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("plan %s is locked: %w", planID, err)
	}
	f.Close()
	defer os.Remove(lockPath)
	return fn()
}
