package pool

import "testing"

func TestStatusConstantsAreDistinct(t *testing.T) {
	workerStatuses := []string{WorkerSpawning, WorkerIdle, WorkerWorking, WorkerBlocked, WorkerDead}
	seen := map[string]bool{}
	for _, s := range workerStatuses {
		if s == "" {
			t.Error("empty worker status constant")
		}
		if seen[s] {
			t.Errorf("duplicate worker status: %q", s)
		}
		seen[s] = true
	}

	taskStatuses := []string{TaskQueued, TaskDispatched, TaskCompleted, TaskFailed, TaskCanceled}
	seen = map[string]bool{}
	for _, s := range taskStatuses {
		if s == "" {
			t.Error("empty task status constant")
		}
		if seen[s] {
			t.Errorf("duplicate task status: %q", s)
		}
		seen[s] = true
	}
}

func TestReviewVerdictConstants(t *testing.T) {
	if ReviewPass == ReviewFail {
		t.Error("ReviewPass == ReviewFail")
	}
	if ReviewPass == "" || ReviewFail == "" {
		t.Error("empty review verdict constant")
	}
}

func TestSeverityConstants(t *testing.T) {
	sevs := []string{SeverityNit, SeverityMinor, SeverityMajor, SeverityCritical}
	seen := map[string]bool{}
	for _, s := range sevs {
		if seen[s] {
			t.Errorf("duplicate severity: %q", s)
		}
		seen[s] = true
	}
}

func TestReviewSeverityHelpers(t *testing.T) {
	if !IsKnownReviewSeverity(" NIT ") {
		t.Fatal("expected nit to be recognized case-insensitively")
	}
	if IsKnownReviewSeverity("warning") {
		t.Fatal("unexpected unknown severity to validate")
	}
	if !ReviewSeverityAtLeast(SeverityCritical, SeverityMajor) {
		t.Fatal("expected critical to rank above major")
	}
	if ReviewSeverityAtLeast(SeverityNit, SeverityMinor) {
		t.Fatal("did not expect nit to rank above minor")
	}
}
