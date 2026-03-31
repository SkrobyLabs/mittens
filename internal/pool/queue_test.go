package pool

import "testing"

func alwaysSatisfied(_ string) bool { return true }
func neverSatisfied(_ string) bool  { return false }

func satisfiedSet(ids ...string) func(string) bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return func(id string) bool { return m[id] }
}

// --- Push + priority ordering ---

func TestPush_PriorityOrdering(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("low", 100, nil)
	pq.Push("high", 1, nil)
	pq.Push("mid", 50, nil)

	want := []string{"high", "mid", "low"}
	for i, w := range want {
		id, ok := pq.Pop(alwaysSatisfied)
		if !ok || id != w {
			t.Fatalf("pop[%d] = %q/%v, want %q/true", i, id, ok, w)
		}
	}
}

// --- Pop returns highest-priority ready task ---

func TestPop_ReturnsHighestPriorityReady(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("p5", 5, nil)
	pq.Push("p1", 1, nil)
	pq.Push("p3", 3, nil)

	id, ok := pq.Pop(alwaysSatisfied)
	if !ok || id != "p1" {
		t.Fatalf("got %q/%v, want p1/true", id, ok)
	}
	if pq.Len() != 2 {
		t.Fatalf("len = %d, want 2", pq.Len())
	}
}

// --- Pop with unsatisfied dependencies ---

func TestPop_SkipsUnsatisfiedDeps(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("blocked", 1, []string{"dep-1"})
	pq.Push("ready", 10, nil)

	id, ok := pq.Pop(neverSatisfied)
	if !ok || id != "ready" {
		t.Fatalf("got %q/%v, want ready/true", id, ok)
	}
}

func TestPop_SkipsPartiallyUnsatisfiedDeps(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("partial", 1, []string{"dep-a", "dep-b"})
	pq.Push("free", 5, nil)

	// Only dep-a is satisfied, dep-b is not → partial is still blocked.
	id, ok := pq.Pop(satisfiedSet("dep-a"))
	if !ok || id != "free" {
		t.Fatalf("got %q/%v, want free/true", id, ok)
	}
}

// --- Dependency satisfaction unblocks tasks ---

func TestPop_DependencySatisfactionUnblocks(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("t-1", 1, nil)
	pq.Push("t-2", 2, []string{"t-1"})

	checker := satisfiedSet() // nothing satisfied yet

	id, ok := pq.Pop(checker)
	if !ok || id != "t-1" {
		t.Fatalf("got %q/%v, want t-1/true", id, ok)
	}

	// t-2 still blocked because checker hasn't been updated.
	_, ok = pq.Pop(checker)
	if ok {
		t.Fatal("t-2 should still be blocked")
	}

	// Mark t-1 satisfied → t-2 becomes available.
	id, ok = pq.Pop(satisfiedSet("t-1"))
	if !ok || id != "t-2" {
		t.Fatalf("got %q/%v, want t-2/true", id, ok)
	}
}

// --- Empty queue ---

func TestPop_EmptyQueue_ReturnsNil(t *testing.T) {
	pq := NewPriorityQueue()

	id, ok := pq.Pop(alwaysSatisfied)
	if ok {
		t.Fatalf("got %q/true, want empty", id)
	}
	if id != "" {
		t.Fatalf("got id %q, want empty string", id)
	}
}

func TestPop_AllBlocked_ReturnsNil(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, []string{"x"})
	pq.Push("b", 2, []string{"y"})

	id, ok := pq.Pop(neverSatisfied)
	if ok {
		t.Fatalf("got %q/true, want nothing poppable", id)
	}
}

// --- FIFO within same priority ---

func TestPop_FIFOWithinSamePriority(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("first", 1, nil)
	pq.Push("second", 1, nil)
	pq.Push("third", 1, nil)

	want := []string{"first", "second", "third"}
	for i, w := range want {
		id, ok := pq.Pop(alwaysSatisfied)
		if !ok || id != w {
			t.Fatalf("pop[%d] = %q, want %q", i, id, w)
		}
	}
}

func TestPop_FIFOPreservedAfterMixedInsert(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a-p1", 1, nil)
	pq.Push("b-p2", 2, nil)
	pq.Push("c-p1", 1, nil) // same priority as a, but inserted later
	pq.Push("d-p2", 2, nil) // same priority as b, but inserted later

	want := []string{"a-p1", "c-p1", "b-p2", "d-p2"}
	for i, w := range want {
		id, ok := pq.Pop(alwaysSatisfied)
		if !ok || id != w {
			t.Fatalf("pop[%d] = %q, want %q", i, id, w)
		}
	}
}

// --- Complex dependency chains ---

func TestPop_ComplexDependencyChain(t *testing.T) {
	// T3 depends on T1 and T2; T3 should only pop after both are satisfied.
	pq := NewPriorityQueue()
	pq.Push("t1", 1, nil)
	pq.Push("t2", 1, nil)
	pq.Push("t3", 1, []string{"t1", "t2"})

	satisfied := map[string]bool{}
	checker := func(id string) bool { return satisfied[id] }

	// Pop t1 (no deps).
	id, ok := pq.Pop(checker)
	if !ok || id != "t1" {
		t.Fatalf("got %q, want t1", id)
	}

	// Only t1 satisfied → t3 still blocked.
	satisfied["t1"] = true
	id, ok = pq.Pop(checker)
	if !ok || id != "t2" {
		t.Fatalf("got %q, want t2 (t3 still blocked)", id)
	}

	// Both satisfied → t3 unblocked.
	satisfied["t2"] = true
	id, ok = pq.Pop(checker)
	if !ok || id != "t3" {
		t.Fatalf("got %q, want t3", id)
	}

	// Queue empty.
	_, ok = pq.Pop(checker)
	if ok {
		t.Fatal("queue should be empty")
	}
}

func TestPop_DiamondDependency(t *testing.T) {
	// Diamond: T4 depends on T2, T3. T2 and T3 both depend on T1.
	pq := NewPriorityQueue()
	pq.Push("t1", 1, nil)
	pq.Push("t2", 2, []string{"t1"})
	pq.Push("t3", 2, []string{"t1"})
	pq.Push("t4", 3, []string{"t2", "t3"})

	satisfied := map[string]bool{}
	checker := func(id string) bool { return satisfied[id] }

	// Phase 1: t1 ready.
	id, _ := pq.Pop(checker)
	if id != "t1" {
		t.Fatalf("got %q, want t1", id)
	}
	satisfied["t1"] = true

	// Phase 2: t2 and t3 both unblocked (same priority, FIFO).
	id, _ = pq.Pop(checker)
	if id != "t2" {
		t.Fatalf("got %q, want t2", id)
	}
	satisfied["t2"] = true

	id, _ = pq.Pop(checker)
	if id != "t3" {
		t.Fatalf("got %q, want t3", id)
	}
	satisfied["t3"] = true

	// Phase 3: t4 unblocked.
	id, ok := pq.Pop(checker)
	if !ok || id != "t4" {
		t.Fatalf("got %q/%v, want t4/true", id, ok)
	}
}

// --- Len ---

func TestLen_EmptyQueue(t *testing.T) {
	pq := NewPriorityQueue()
	if pq.Len() != 0 {
		t.Fatalf("len = %d, want 0", pq.Len())
	}
}

func TestLen_AfterPushAndPop(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, nil)
	pq.Push("b", 2, nil)
	pq.Push("c", 3, nil)
	if pq.Len() != 3 {
		t.Fatalf("len = %d, want 3", pq.Len())
	}

	pq.Pop(alwaysSatisfied)
	if pq.Len() != 2 {
		t.Fatalf("len = %d, want 2", pq.Len())
	}

	pq.Pop(alwaysSatisfied)
	pq.Pop(alwaysSatisfied)
	if pq.Len() != 0 {
		t.Fatalf("len = %d, want 0", pq.Len())
	}
}

// --- Remove ---

func TestRemove_CleansUpDeps(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, nil)
	pq.Push("b", 2, []string{"a"})
	pq.Push("c", 3, nil)

	pq.Remove("b")
	if pq.Len() != 2 {
		t.Fatalf("len = %d, want 2", pq.Len())
	}
	if len(pq.deps["b"]) != 0 {
		t.Fatalf("deps for b not cleaned: %v", pq.deps["b"])
	}
	// Reverse index for "a" should no longer mention "b".
	for _, r := range pq.depRev["a"] {
		if r == "b" {
			t.Fatal("depRev[a] still contains b after remove")
		}
	}
}

func TestRemove_NonExistent(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, nil)
	pq.Remove("does-not-exist") // should not panic
	if pq.Len() != 1 {
		t.Fatalf("len = %d, want 1", pq.Len())
	}
}

// --- PendingDeps ---

func TestPendingDeps_PartialSatisfaction(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("t-1", 1, []string{"dep-a", "dep-b", "dep-c"})

	pending := pq.PendingDeps("t-1", satisfiedSet("dep-a"))
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 items", pending)
	}
	m := map[string]bool{}
	for _, p := range pending {
		m[p] = true
	}
	if !m["dep-b"] || !m["dep-c"] {
		t.Fatalf("pending = %v, want dep-b and dep-c", pending)
	}
}

func TestPendingDeps_AllSatisfied(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("t-1", 1, []string{"dep-a", "dep-b"})

	pending := pq.PendingDeps("t-1", satisfiedSet("dep-a", "dep-b"))
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty", pending)
	}
}

func TestPendingDeps_NoDeps(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("t-1", 1, nil)

	pending := pq.PendingDeps("t-1", neverSatisfied)
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty", pending)
	}
}

// --- HasCircularDeps ---

func TestHasCircularDeps_SelfCycle(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, nil)

	getDeps := func(id string) []string { return pq.deps[id] }
	if !pq.HasCircularDeps("a", []string{"a"}, getDeps) {
		t.Fatal("should detect self-cycle")
	}
}

func TestHasCircularDeps_MutualCycle(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, []string{"b"})
	pq.Push("b", 1, []string{"a"})

	getDeps := func(id string) []string { return pq.deps[id] }
	// b depends on a, a depends on b → b→a→b is a cycle.
	if !pq.HasCircularDeps("b", []string{"a"}, getDeps) {
		t.Fatal("should detect b -> a -> b cycle")
	}
}

func TestHasCircularDeps_NoCycle(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push("a", 1, nil)
	pq.Push("b", 2, []string{"a"})

	getDeps := func(id string) []string { return pq.deps[id] }
	// c depending on b: c->b->a, no path back to c.
	if pq.HasCircularDeps("c", []string{"b"}, getDeps) {
		t.Fatal("should not detect cycle for c->b->a")
	}
}
