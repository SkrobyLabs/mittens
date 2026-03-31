package pool

import "sort"

type queueEntry struct {
	taskID      string
	priority    int
	insertOrder int // for stable FIFO within same priority
}

// PriorityQueue manages task ordering with dependency tracking.
type PriorityQueue struct {
	entries []queueEntry
	deps    map[string][]string // taskID -> dependency task IDs
	depRev  map[string][]string // depID -> dependent task IDs (reverse index)
	nextOrd int
}

// NewPriorityQueue creates an empty priority queue.
func NewPriorityQueue() *PriorityQueue {
	return &PriorityQueue{
		deps:   make(map[string][]string),
		depRev: make(map[string][]string),
	}
}

// Push inserts a task into the queue with the given priority and dependencies.
// Lower priority number = higher priority (dispatched first).
func (pq *PriorityQueue) Push(taskID string, priority int, dependsOn []string) {
	pq.entries = append(pq.entries, queueEntry{
		taskID:      taskID,
		priority:    priority,
		insertOrder: pq.nextOrd,
	})
	pq.nextOrd++
	sort.Slice(pq.entries, func(i, j int) bool {
		if pq.entries[i].priority != pq.entries[j].priority {
			return pq.entries[i].priority < pq.entries[j].priority
		}
		return pq.entries[i].insertOrder < pq.entries[j].insertOrder
	})

	if len(dependsOn) > 0 {
		pq.deps[taskID] = dependsOn
		for _, dep := range dependsOn {
			pq.depRev[dep] = append(pq.depRev[dep], taskID)
		}
	}
}

// Pop returns the highest-priority task whose dependencies are all satisfied.
// isDepSatisfied is a callback that checks whether a dependency task is in a
// terminal-success state (completed or accepted).
func (pq *PriorityQueue) Pop(isDepSatisfied func(depID string) bool) (string, bool) {
	for i, e := range pq.entries {
		deps := pq.deps[e.taskID]
		allSatisfied := true
		for _, dep := range deps {
			if !isDepSatisfied(dep) {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			pq.entries = append(pq.entries[:i], pq.entries[i+1:]...)
			pq.cleanupDeps(e.taskID)
			return e.taskID, true
		}
	}
	return "", false
}

// Remove removes a task from the queue and cleans up dependency tracking.
func (pq *PriorityQueue) Remove(taskID string) {
	for i, e := range pq.entries {
		if e.taskID == taskID {
			pq.entries = append(pq.entries[:i], pq.entries[i+1:]...)
			break
		}
	}
	pq.cleanupDeps(taskID)
}

// HasCircularDeps returns true if adding the given dependencies for taskID
// would create a cycle. getDeps returns the current dependencies for any task
// (checks both queued and in-flight tasks).
func (pq *PriorityQueue) HasCircularDeps(taskID string, dependsOn []string, getDeps func(string) []string) bool {
	// DFS: starting from each dep, can we reach taskID?
	visited := make(map[string]bool)

	var dfs func(current string) bool
	dfs = func(current string) bool {
		if current == taskID {
			return true
		}
		if visited[current] {
			return false
		}
		visited[current] = true
		for _, dep := range getDeps(current) {
			if dfs(dep) {
				return true
			}
		}
		return false
	}

	for _, dep := range dependsOn {
		visited = make(map[string]bool) // fresh visited per root
		if dfs(dep) {
			return true
		}
	}
	return false
}

// PendingDeps returns the dependency task IDs that are not yet satisfied for taskID.
func (pq *PriorityQueue) PendingDeps(taskID string, isDepSatisfied func(string) bool) []string {
	deps := pq.deps[taskID]
	var pending []string
	for _, dep := range deps {
		if !isDepSatisfied(dep) {
			pending = append(pending, dep)
		}
	}
	return pending
}

// Len returns the number of tasks in the queue.
func (pq *PriorityQueue) Len() int {
	return len(pq.entries)
}

func (pq *PriorityQueue) cleanupDeps(taskID string) {
	// Remove forward deps.
	if deps, ok := pq.deps[taskID]; ok {
		for _, dep := range deps {
			revs := pq.depRev[dep]
			for i, r := range revs {
				if r == taskID {
					pq.depRev[dep] = append(revs[:i], revs[i+1:]...)
					break
				}
			}
			if len(pq.depRev[dep]) == 0 {
				delete(pq.depRev, dep)
			}
		}
		delete(pq.deps, taskID)
	}
}
