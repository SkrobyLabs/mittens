package pool

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestPlanStore(t *testing.T) *PlanStore {
	t.Helper()
	return NewPlanStore(filepath.Join(t.TempDir(), "plans"))
}

// --- NewPlanStore ---

func TestNewPlanStore_Empty(t *testing.T) {
	ps := NewPlanStore("")
	if ps != nil {
		t.Error("expected nil PlanStore for empty dir")
	}
}

func TestNewPlanStore_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plans")
	ps := NewPlanStore(dir)
	if ps == nil {
		t.Fatal("expected non-nil PlanStore")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("plans dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

// --- CreatePlan ---

func TestCreatePlan(t *testing.T) {
	ps := newTestPlanStore(t)

	id, err := ps.CreatePlan("Test Plan", "# Plan content\nStep 1\nStep 2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(id) != 8 {
		t.Errorf("expected 8-char ID, got %q (len %d)", id, len(id))
	}

	// Verify files on disk.
	planDir := filepath.Join(ps.plansDir, id)
	if _, err := os.Stat(filepath.Join(planDir, "plan.md")); err != nil {
		t.Errorf("plan.md not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(planDir, "status.json")); err != nil {
		t.Errorf("status.json not created: %v", err)
	}
}

func TestCreatePlan_UniqueIDs(t *testing.T) {
	ps := newTestPlanStore(t)

	ids := map[string]bool{}
	for i := 0; i < 10; i++ {
		id, err := ps.CreatePlan("Plan", "content")
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if ids[id] {
			t.Errorf("duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

// --- ReadPlan ---

func TestReadPlan(t *testing.T) {
	ps := newTestPlanStore(t)
	content := "# My Plan\n\nDetails here."
	id, _ := ps.CreatePlan("Read Test", content)

	got, err := ps.ReadPlan(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestReadPlan_NotFound(t *testing.T) {
	ps := newTestPlanStore(t)
	_, err := ps.ReadPlan("deadbeef")
	if err == nil {
		t.Error("expected error for missing plan")
	}
}

func TestReadPlan_InvalidID(t *testing.T) {
	ps := newTestPlanStore(t)
	_, err := ps.ReadPlan("../escape")
	if err == nil {
		t.Error("expected error for invalid plan ID")
	}
}

// --- ClaimPlan ---

func TestClaimPlan(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Claim Test", "content")

	if err := ps.ClaimPlan(id, "session-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	plan, err := ps.readStatus(id)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if plan.Status != PlanActive {
		t.Errorf("status = %q, want active", plan.Status)
	}
	if plan.Owner != "session-1" {
		t.Errorf("owner = %q, want session-1", plan.Owner)
	}
	if plan.ClaimedAt.IsZero() {
		t.Error("claimedAt should be set")
	}
}

func TestClaimPlan_AlreadyActive(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Double Claim", "content")
	ps.ClaimPlan(id, "session-1")

	err := ps.ClaimPlan(id, "session-2")
	if err == nil {
		t.Error("expected error when claiming already-active plan")
	}
}

func TestClaimPlan_OrphanedPlanCanBeClaimed(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Orphan Claim", "content")
	ps.ClaimPlan(id, "session-1")
	ps.OrphanPlan(id)

	if err := ps.ClaimPlan(id, "session-2"); err != nil {
		t.Fatalf("claim orphaned plan: %v", err)
	}

	plan, _ := ps.readStatus(id)
	if plan.Status != PlanActive {
		t.Errorf("status = %q, want active", plan.Status)
	}
	if plan.Owner != "session-2" {
		t.Errorf("owner = %q, want session-2", plan.Owner)
	}
}

// --- CompletePlan ---

func TestCompletePlan(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Complete Test", "content")

	if err := ps.CompletePlan(id); err != nil {
		t.Fatalf("complete: %v", err)
	}

	plan, _ := ps.readStatus(id)
	if plan.Status != PlanCompleted {
		t.Errorf("status = %q, want completed", plan.Status)
	}
	if plan.CompletedAt.IsZero() {
		t.Error("completedAt should be set")
	}
}

// --- OrphanPlan ---

func TestOrphanPlan(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Orphan Test", "content")
	ps.ClaimPlan(id, "session-1")

	if err := ps.OrphanPlan(id); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	plan, _ := ps.readStatus(id)
	if plan.Status != PlanOrphaned {
		t.Errorf("status = %q, want orphaned", plan.Status)
	}
	if plan.Owner != "" {
		t.Errorf("owner = %q, want empty", plan.Owner)
	}
}

// --- UpdateProgress ---

func TestUpdateProgress(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Progress Test", "content")

	if err := ps.UpdateProgress(id, "step 1 done"); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	if err := ps.UpdateProgress(id, "step 2 done"); err != nil {
		t.Fatalf("update 2: %v", err)
	}

	logPath := filepath.Join(ps.plansDir, id, "log.md")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)
	if len(log) == 0 {
		t.Error("log.md should not be empty")
	}
}

func TestUpdateProgress_InvalidID(t *testing.T) {
	ps := newTestPlanStore(t)
	err := ps.UpdateProgress("bad!id", "entry")
	if err == nil {
		t.Error("expected error for invalid plan ID")
	}
}

// --- ListPlans ---

func TestListPlans(t *testing.T) {
	ps := newTestPlanStore(t)

	ps.CreatePlan("Plan A", "a")
	ps.CreatePlan("Plan B", "b")
	ps.CreatePlan("Plan C", "c")

	plans, err := ps.ListPlans()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans = %d, want 3", len(plans))
	}

	// Newest first.
	if plans[0].Title != "Plan C" {
		t.Errorf("first plan = %q, want Plan C", plans[0].Title)
	}
}

func TestListPlans_Empty(t *testing.T) {
	ps := newTestPlanStore(t)
	plans, err := ps.ListPlans()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(plans) != 0 {
		t.Errorf("expected 0 plans, got %d", len(plans))
	}
}

func TestListPlans_SkipsMalformed(t *testing.T) {
	ps := newTestPlanStore(t)
	ps.CreatePlan("Good", "content")

	// Create a malformed plan directory.
	bad := filepath.Join(ps.plansDir, "badplan1")
	os.MkdirAll(bad, 0755)
	os.WriteFile(filepath.Join(bad, "status.json"), []byte("not json"), 0644)

	plans, err := ps.ListPlans()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("plans = %d, want 1 (should skip malformed)", len(plans))
	}
}

// --- File locking ---

func TestFileLocking_ConcurrentClaim(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Concurrent Claim", "content")

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var successCount int
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			err := ps.ClaimPlan(id, "session-"+string(rune('a'+n)))
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	// Only one should succeed (first gets the lock, rest either fail on lock or on non-pending status).
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful claim, got %d", successCount)
	}
}

func TestFileLocking_ConcurrentComplete(t *testing.T) {
	ps := newTestPlanStore(t)
	id, _ := ps.CreatePlan("Concurrent Complete", "content")

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)
	var successCount int
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			err := ps.CompletePlan(id)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	// At least one should succeed.
	if successCount < 1 {
		t.Error("expected at least 1 successful completion")
	}
}

// --- Full lifecycle ---

func TestPlanFullLifecycle(t *testing.T) {
	ps := newTestPlanStore(t)

	// Create.
	id, err := ps.CreatePlan("Full Lifecycle", "## Steps\n1. Do X\n2. Do Y")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Read.
	content, err := ps.ReadPlan(id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content != "## Steps\n1. Do X\n2. Do Y" {
		t.Errorf("content mismatch: %q", content)
	}

	// Claim.
	if err := ps.ClaimPlan(id, "sess-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Update progress.
	if err := ps.UpdateProgress(id, "started X"); err != nil {
		t.Fatalf("progress: %v", err)
	}

	// Complete.
	if err := ps.CompletePlan(id); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Verify final state.
	plan, _ := ps.readStatus(id)
	if plan.Status != PlanCompleted {
		t.Errorf("final status = %q, want completed", plan.Status)
	}

	// Verify it appears in list.
	plans, _ := ps.ListPlans()
	if len(plans) != 1 {
		t.Fatalf("list = %d, want 1", len(plans))
	}
	if plans[0].ID != id {
		t.Errorf("listed ID = %q, want %q", plans[0].ID, id)
	}
}
