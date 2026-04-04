package pool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWALFileCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.wal")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("WAL file should not exist before OpenWAL")
	}

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("WAL file should exist after OpenWAL: %v", err)
	}

	if wal.Seq() != 0 {
		t.Errorf("initial seq = %d, want 0", wal.Seq())
	}
}

func TestWALAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	events := []Event{
		{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1", Data: marshalData(WorkerSpawnedData{Role: "impl"})},
		{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"},
		{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "do stuff", Priority: 1})},
	}

	for _, e := range events {
		if _, err := wal.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	if wal.Seq() != 3 {
		t.Errorf("seq = %d, want 3", wal.Seq())
	}

	wal.Close()

	// Reopen and replay.
	wal2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	if wal2.Seq() != 3 {
		t.Errorf("recovered seq = %d, want 3", wal2.Seq())
	}

	var replayed []Event
	err = wal2.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3", len(replayed))
	}
	if replayed[0].Type != EventWorkerSpawned {
		t.Errorf("event[0] type = %q, want %q", replayed[0].Type, EventWorkerSpawned)
	}
	if replayed[0].Sequence != 1 {
		t.Errorf("event[0] seq = %d, want 1", replayed[0].Sequence)
	}
	if replayed[2].Sequence != 3 {
		t.Errorf("event[2] seq = %d, want 3", replayed[2].Sequence)
	}
}

func TestWALReplayEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	count := 0
	err = wal.Replay(func(e Event) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if count != 0 {
		t.Errorf("replayed %d events from empty WAL", count)
	}
}

func TestWALSequenceContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seq.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	wal.Close()

	wal2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	e, err := wal2.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if e.Sequence != 3 {
		t.Errorf("seq = %d, want 3", e.Sequence)
	}
}

func TestWALMultipleAppendCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	// First batch.
	wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	wal.Append(Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})

	// Second batch.
	wal.Append(Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	wal.Append(Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "x"})})

	// Third batch.
	wal.Append(Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})

	var replayed []Event
	err = wal.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 5 {
		t.Fatalf("replayed %d events, want 5", len(replayed))
	}

	expectedTypes := []string{
		EventPoolCreated,
		EventWorkerSpawned,
		EventWorkerReady,
		EventTaskCreated,
		EventTaskDispatched,
	}
	for i, want := range expectedTypes {
		if replayed[i].Type != want {
			t.Errorf("event[%d] type = %q, want %q", i, replayed[i].Type, want)
		}
		if replayed[i].Sequence != uint64(i+1) {
			t.Errorf("event[%d] seq = %d, want %d", i, replayed[i].Sequence, i+1)
		}
	}
}

func TestWALFileFormatIsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jsonl.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	wal.Append(Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1",
		Data: marshalData(WorkerSpawnedData{ContainerID: "abc123", Role: "impl"})})
	wal.Append(Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1",
		Data: marshalData(TaskCreatedData{Prompt: "hello", Priority: 3})})
	wal.Close()

	// Read raw file and verify each line is valid JSON.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			t.Errorf("line %d is not valid JSON: %s", lineNum, line)
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Errorf("line %d failed to unmarshal as Event: %v", lineNum, err)
		}
		if e.Type == "" {
			t.Errorf("line %d has empty type", lineNum)
		}
		if e.Sequence == 0 {
			t.Errorf("line %d has zero sequence", lineNum)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lineNum != 3 {
		t.Errorf("got %d lines, want 3", lineNum)
	}
}

func TestWALAppendAssignsSequence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seq-assign.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	// Input event has no sequence set.
	in := Event{Timestamp: time.Now(), Type: EventPoolCreated, Sequence: 0}
	out, err := wal.Append(in)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if out.Sequence != 1 {
		t.Errorf("returned seq = %d, want 1", out.Sequence)
	}

	out2, err := wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if out2.Sequence != 2 {
		t.Errorf("returned seq = %d, want 2", out2.Sequence)
	}
}

// TestWALRoundTripAllEventTypes verifies that every event type can be appended
// to the WAL and replayed with all fields preserved.
func TestWALRoundTripAllEventTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond) // truncate for JSON round-trip stability

	allEvents := []Event{
		{Timestamp: now, Type: EventPoolCreated},
		{Timestamp: now, Type: EventWorkerSpawned, WorkerID: "w-1",
			Data: marshalData(WorkerSpawnedData{ContainerID: "ctr-abc", Role: "planner"})},
		{Timestamp: now, Type: EventWorkerReady, WorkerID: "w-1"},
		{Timestamp: now, Type: EventWorkerBusy, WorkerID: "w-1", TaskID: "t-1"},
		{Timestamp: now, Type: EventWorkerIdle, WorkerID: "w-1"},
		{Timestamp: now, Type: EventWorkerBlocked, WorkerID: "w-1"},
		{Timestamp: now, Type: EventWorkerDead, WorkerID: "w-1"},
		{Timestamp: now, Type: EventTaskCreated, TaskID: "t-1",
			Data: marshalData(TaskCreatedData{
				Prompt: "implement feature X", Priority: 5, DependsOn: []string{"t-0"},
				Role: "implementer", MaxReviews: 2, PipelineID: "p-1", StageIndex: 1,
			})},
		{Timestamp: now, Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1",
			Data: marshalData(TaskDispatchedData{WorkerID: "w-1"})},
		{Timestamp: now, Type: EventTaskCompleted, TaskID: "t-1", WorkerID: "w-1",
			Data: marshalData(TaskCompletedData{
				Summary: "done", ContextSummary: "ctx", FilesChanged: []string{"a.go", "b.go"},
			})},
		{Timestamp: now, Type: EventTaskFailed, TaskID: "t-2", WorkerID: "w-1",
			Data: marshalData(TaskFailedData{Error: "timeout"})},
		{Timestamp: now, Type: EventTaskCanceled, TaskID: "t-3"},
		{Timestamp: now, Type: EventTaskRequeued, TaskID: "t-2"},
		{Timestamp: now, Type: EventReviewDispatched, TaskID: "t-1",
			Data: marshalData(ReviewDispatchedData{ReviewerID: "w-2"})},
		{Timestamp: now, Type: EventReviewCompleted, TaskID: "t-1",
			Data: marshalData(ReviewCompletedData{
				ReviewerID: "w-2", Verdict: ReviewPass, Feedback: "looks good", Severity: SeverityMinor,
			})},
		{Timestamp: now, Type: EventTaskAccepted, TaskID: "t-1"},
		{Timestamp: now, Type: EventTaskRejected, TaskID: "t-4"},
		{Timestamp: now, Type: EventTaskEscalated, TaskID: "t-5",
			Data: marshalData(EscalationData{Action: EscalationRetry})},
		{Timestamp: now, Type: EventPipelineCreated, TaskID: "p-1",
			Data: marshalData(PipelineCreatedData{Pipeline: Pipeline{
				ID: "p-1", Goal: "build feature", Status: PipelineRunning,
				Stages: []Stage{{Name: "plan", Role: "planner"}},
			}})},
		{Timestamp: now, Type: EventPipelineCompleted, TaskID: "p-1"},
		{Timestamp: now, Type: EventPipelineFailed, TaskID: "p-2",
			Data: marshalData(PipelineFailedData{Reason: "stage 2 failed"})},
		{Timestamp: now, Type: EventPipelineBlocked, TaskID: "p-1",
			Data: marshalData(PipelineBlockedData{PipelineID: "p-1", StageIndex: 1})},
		{Timestamp: now, Type: EventPipelineUnblocked, TaskID: "p-1",
			Data: marshalData(PipelineBlockedData{PipelineID: "p-1", StageIndex: 1})},
		{Timestamp: now, Type: EventStageAdvanced, TaskID: "t-1",
			Data: marshalData(StageAdvancedData{PipelineID: "p-1", StageIndex: 1, TriggerTaskID: "t-1"})},
		{Timestamp: now, Type: EventWorkerQuestion, WorkerID: "w-1", TaskID: "t-1",
			Data: marshalData(WorkerQuestionData{
				QuestionID: "q-1", Question: "which DB?", Category: "architecture",
				Options: []string{"postgres", "mysql"}, Blocking: true,
			})},
		{Timestamp: now, Type: EventQuestionAnswered,
			Data: marshalData(QuestionAnsweredData{
				QuestionID: "q-1", Answer: "postgres", AnsweredBy: "leader",
			})},
	}

	for i, e := range allEvents {
		if _, err := wal.Append(e); err != nil {
			t.Fatalf("append event %d (%s): %v", i, e.Type, err)
		}
	}

	wal.Close()

	// Reopen and replay.
	wal2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	var replayed []Event
	err = wal2.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if len(replayed) != len(allEvents) {
		t.Fatalf("replayed %d events, want %d", len(replayed), len(allEvents))
	}

	for i, got := range replayed {
		want := allEvents[i]
		if got.Type != want.Type {
			t.Errorf("event[%d] type = %q, want %q", i, got.Type, want.Type)
		}
		if got.Sequence != uint64(i+1) {
			t.Errorf("event[%d] seq = %d, want %d", i, got.Sequence, i+1)
		}
		if got.TaskID != want.TaskID {
			t.Errorf("event[%d] taskId = %q, want %q", i, got.TaskID, want.TaskID)
		}
		if got.WorkerID != want.WorkerID {
			t.Errorf("event[%d] workerId = %q, want %q", i, got.WorkerID, want.WorkerID)
		}
	}
}

// TestEventMarshalRoundTrip verifies that each typed data payload serializes and
// deserializes correctly with all fields preserved.
func TestEventMarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		evType   string
		data     any
		validate func(t *testing.T, raw json.RawMessage)
	}{
		{
			name:   "WorkerSpawnedData",
			evType: EventWorkerSpawned,
			data:   WorkerSpawnedData{ContainerID: "ctr-99", Role: "reviewer"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d WorkerSpawnedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "ContainerID", d.ContainerID, "ctr-99")
				assertEqual(t, "Role", d.Role, "reviewer")
			},
		},
		{
			name:   "TaskCreatedData",
			evType: EventTaskCreated,
			data: TaskCreatedData{
				Prompt: "build a REST API", Priority: 10, DependsOn: []string{"t-0", "t-1"},
				Role: "implementer", MaxReviews: 3, PipelineID: "p-5", StageIndex: 2,
			},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d TaskCreatedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Prompt", d.Prompt, "build a REST API")
				assertEqualInt(t, "Priority", d.Priority, 10)
				if len(d.DependsOn) != 2 || d.DependsOn[0] != "t-0" || d.DependsOn[1] != "t-1" {
					t.Errorf("DependsOn = %v, want [t-0 t-1]", d.DependsOn)
				}
				assertEqual(t, "Role", d.Role, "implementer")
				assertEqualInt(t, "MaxReviews", d.MaxReviews, 3)
				assertEqual(t, "PipelineID", d.PipelineID, "p-5")
				assertEqualInt(t, "StageIndex", d.StageIndex, 2)
			},
		},
		{
			name:   "TaskDispatchedData",
			evType: EventTaskDispatched,
			data:   TaskDispatchedData{WorkerID: "w-42"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d TaskDispatchedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "WorkerID", d.WorkerID, "w-42")
			},
		},
		{
			name:   "TaskCompletedData",
			evType: EventTaskCompleted,
			data:   TaskCompletedData{Summary: "all done", ContextSummary: "built X", FilesChanged: []string{"main.go", "go.mod"}},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d TaskCompletedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Summary", d.Summary, "all done")
				assertEqual(t, "ContextSummary", d.ContextSummary, "built X")
				if len(d.FilesChanged) != 2 {
					t.Errorf("FilesChanged len = %d, want 2", len(d.FilesChanged))
				}
			},
		},
		{
			name:   "TaskFailedData",
			evType: EventTaskFailed,
			data:   TaskFailedData{Error: "container OOMKilled"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d TaskFailedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Error", d.Error, "container OOMKilled")
			},
		},
		{
			name:   "ReviewDispatchedData",
			evType: EventReviewDispatched,
			data:   ReviewDispatchedData{ReviewerID: "w-rev"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d ReviewDispatchedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "ReviewerID", d.ReviewerID, "w-rev")
			},
		},
		{
			name:   "ReviewCompletedData",
			evType: EventReviewCompleted,
			data:   ReviewCompletedData{ReviewerID: "w-rev", Verdict: ReviewFail, Feedback: "missing tests", Severity: SeverityMajor},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d ReviewCompletedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "ReviewerID", d.ReviewerID, "w-rev")
				assertEqual(t, "Verdict", d.Verdict, ReviewFail)
				assertEqual(t, "Feedback", d.Feedback, "missing tests")
				assertEqual(t, "Severity", d.Severity, SeverityMajor)
			},
		},
		{
			name:   "WorkerQuestionData",
			evType: EventWorkerQuestion,
			data: WorkerQuestionData{
				QuestionID: "q-7", Question: "which framework?", Category: "design",
				Options: []string{"gin", "echo", "chi"}, Blocking: true,
			},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d WorkerQuestionData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "QuestionID", d.QuestionID, "q-7")
				assertEqual(t, "Question", d.Question, "which framework?")
				assertEqual(t, "Category", d.Category, "design")
				if len(d.Options) != 3 {
					t.Errorf("Options len = %d, want 3", len(d.Options))
				}
				if !d.Blocking {
					t.Error("Blocking = false, want true")
				}
			},
		},
		{
			name:   "QuestionAnsweredData",
			evType: EventQuestionAnswered,
			data:   QuestionAnsweredData{QuestionID: "q-7", Answer: "chi", AnsweredBy: "leader"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d QuestionAnsweredData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "QuestionID", d.QuestionID, "q-7")
				assertEqual(t, "Answer", d.Answer, "chi")
				assertEqual(t, "AnsweredBy", d.AnsweredBy, "leader")
			},
		},
		{
			name:   "EscalationData",
			evType: EventTaskEscalated,
			data:   EscalationData{Action: EscalationAbort},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d EscalationData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Action", d.Action, EscalationAbort)
			},
		},
		{
			name:   "PipelineCreatedData",
			evType: EventPipelineCreated,
			data: PipelineCreatedData{Pipeline: Pipeline{
				ID: "p-1", Goal: "ship v2", Status: PipelineRunning,
				Stages: []Stage{
					{Name: "plan", Role: "planner", AutoAdvance: true},
					{Name: "implement", Role: "implementer", Fan: FanOut,
						Tasks:     []StageTask{{ID: "st-1", PromptTmpl: "do {{.thing}}"}},
						DependsOn: []string{"plan"}},
				},
			}},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d PipelineCreatedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Pipeline.ID", d.Pipeline.ID, "p-1")
				assertEqual(t, "Pipeline.Goal", d.Pipeline.Goal, "ship v2")
				if len(d.Pipeline.Stages) != 2 {
					t.Fatalf("Stages len = %d, want 2", len(d.Pipeline.Stages))
				}
				assertEqual(t, "Stage[0].Name", d.Pipeline.Stages[0].Name, "plan")
				if !d.Pipeline.Stages[0].AutoAdvance {
					t.Error("Stage[0].AutoAdvance = false, want true")
				}
				assertEqual(t, "Stage[1].Fan", string(d.Pipeline.Stages[1].Fan), string(FanOut))
				if len(d.Pipeline.Stages[1].Tasks) != 1 {
					t.Fatalf("Stage[1].Tasks len = %d, want 1", len(d.Pipeline.Stages[1].Tasks))
				}
				assertEqual(t, "StageTask.ID", d.Pipeline.Stages[1].Tasks[0].ID, "st-1")
			},
		},
		{
			name:   "PipelineFailedData",
			evType: EventPipelineFailed,
			data:   PipelineFailedData{Reason: "stage 2 failed"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d PipelineFailedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "Reason", d.Reason, "stage 2 failed")
			},
		},
		{
			name:   "PipelineBlockedData",
			evType: EventPipelineBlocked,
			data:   PipelineBlockedData{PipelineID: "p-1", StageIndex: 2},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d PipelineBlockedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "PipelineID", d.PipelineID, "p-1")
				assertEqualInt(t, "StageIndex", d.StageIndex, 2)
			},
		},
		{
			name:   "StageAdvancedData",
			evType: EventStageAdvanced,
			data:   StageAdvancedData{PipelineID: "p-1", StageIndex: 3, TriggerTaskID: "t-9"},
			validate: func(t *testing.T, raw json.RawMessage) {
				var d StageAdvancedData
				mustUnmarshal(t, raw, &d)
				assertEqual(t, "PipelineID", d.PipelineID, "p-1")
				assertEqualInt(t, "StageIndex", d.StageIndex, 3)
				assertEqual(t, "TriggerTaskID", d.TriggerTaskID, "t-9")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := marshalData(tc.data)
			if raw == nil {
				t.Fatal("marshalData returned nil")
			}
			if !json.Valid(raw) {
				t.Fatalf("marshalData produced invalid JSON: %s", raw)
			}

			// Round-trip through Event marshal/unmarshal.
			e := Event{
				Timestamp: time.Now(),
				Type:      tc.evType,
				Sequence:  42,
				TaskID:    "t-1",
				WorkerID:  "w-1",
				Data:      raw,
			}

			encoded, err := json.Marshal(e)
			if err != nil {
				t.Fatalf("json.Marshal(Event): %v", err)
			}

			var decoded Event
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("json.Unmarshal(Event): %v", err)
			}

			if decoded.Type != tc.evType {
				t.Errorf("type = %q, want %q", decoded.Type, tc.evType)
			}
			if decoded.Sequence != 42 {
				t.Errorf("seq = %d, want 42", decoded.Sequence)
			}
			if decoded.TaskID != "t-1" {
				t.Errorf("taskId = %q, want t-1", decoded.TaskID)
			}
			if decoded.WorkerID != "w-1" {
				t.Errorf("workerId = %q, want w-1", decoded.WorkerID)
			}

			tc.validate(t, decoded.Data)
		})
	}
}

// TestWALRoundTripDataPreserved verifies that typed data payloads survive
// a full WAL append → close → reopen → replay cycle.
func TestWALRoundTripDataPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	taskData := TaskCreatedData{
		Prompt: "build auth module", Priority: 7, DependsOn: []string{"t-0"},
		Role: "implementer", MaxReviews: 2, PipelineID: "p-3", StageIndex: 1,
	}
	wal.Append(Event{
		Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1",
		Data: marshalData(taskData),
	})

	completedData := TaskCompletedData{
		Summary: "built auth", ContextSummary: "added JWT middleware",
		FilesChanged: []string{"auth.go", "auth_test.go", "go.mod"},
	}
	wal.Append(Event{
		Timestamp: time.Now(), Type: EventTaskCompleted, TaskID: "t-1", WorkerID: "w-1",
		Data: marshalData(completedData),
	})

	questionData := WorkerQuestionData{
		QuestionID: "q-1", Question: "bcrypt or argon2?", Category: "security",
		Options: []string{"bcrypt", "argon2"}, Blocking: true,
	}
	wal.Append(Event{
		Timestamp: time.Now(), Type: EventWorkerQuestion, WorkerID: "w-1", TaskID: "t-1",
		Data: marshalData(questionData),
	})
	wal.Close()

	// Reopen and replay, verifying data payloads.
	wal2, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	var replayed []Event
	wal2.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})

	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3", len(replayed))
	}

	// Verify TaskCreatedData.
	var tc TaskCreatedData
	mustUnmarshal(t, replayed[0].Data, &tc)
	assertEqual(t, "Prompt", tc.Prompt, "build auth module")
	assertEqualInt(t, "Priority", tc.Priority, 7)
	assertEqual(t, "Role", tc.Role, "implementer")
	assertEqualInt(t, "MaxReviews", tc.MaxReviews, 2)
	assertEqual(t, "PipelineID", tc.PipelineID, "p-3")
	assertEqualInt(t, "StageIndex", tc.StageIndex, 1)
	if len(tc.DependsOn) != 1 || tc.DependsOn[0] != "t-0" {
		t.Errorf("DependsOn = %v, want [t-0]", tc.DependsOn)
	}

	// Verify TaskCompletedData.
	var cd TaskCompletedData
	mustUnmarshal(t, replayed[1].Data, &cd)
	assertEqual(t, "Summary", cd.Summary, "built auth")
	assertEqual(t, "ContextSummary", cd.ContextSummary, "added JWT middleware")
	if len(cd.FilesChanged) != 3 {
		t.Errorf("FilesChanged len = %d, want 3", len(cd.FilesChanged))
	}

	// Verify WorkerQuestionData.
	var qd WorkerQuestionData
	mustUnmarshal(t, replayed[2].Data, &qd)
	assertEqual(t, "QuestionID", qd.QuestionID, "q-1")
	assertEqual(t, "Question", qd.Question, "bcrypt or argon2?")
	assertEqual(t, "Category", qd.Category, "security")
	if len(qd.Options) != 2 {
		t.Errorf("Options len = %d, want 2", len(qd.Options))
	}
	if !qd.Blocking {
		t.Error("Blocking = false, want true")
	}
}

// TestWALEventsWithoutData verifies events that carry no data payload.
func TestWALEventsWithoutData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodata.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	noDataEvents := []Event{
		{Timestamp: time.Now(), Type: EventPoolCreated},
		{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"},
		{Timestamp: time.Now(), Type: EventWorkerBusy, WorkerID: "w-1", TaskID: "t-1"},
		{Timestamp: time.Now(), Type: EventWorkerIdle, WorkerID: "w-1"},
		{Timestamp: time.Now(), Type: EventWorkerBlocked, WorkerID: "w-1"},
		{Timestamp: time.Now(), Type: EventWorkerDead, WorkerID: "w-1"},
		{Timestamp: time.Now(), Type: EventTaskCanceled, TaskID: "t-1"},
		{Timestamp: time.Now(), Type: EventTaskRequeued, TaskID: "t-1"},
		{Timestamp: time.Now(), Type: EventTaskAccepted, TaskID: "t-1"},
		{Timestamp: time.Now(), Type: EventTaskRejected, TaskID: "t-1"},
		{Timestamp: time.Now(), Type: EventPipelineCompleted, TaskID: "p-1"},
	}

	for _, e := range noDataEvents {
		if _, err := wal.Append(e); err != nil {
			t.Fatalf("append %s: %v", e.Type, err)
		}
	}

	var replayed []Event
	wal.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})

	if len(replayed) != len(noDataEvents) {
		t.Fatalf("replayed %d events, want %d", len(replayed), len(noDataEvents))
	}

	for i, got := range replayed {
		if got.Type != noDataEvents[i].Type {
			t.Errorf("event[%d] type = %q, want %q", i, got.Type, noDataEvents[i].Type)
		}
		if got.Data != nil && string(got.Data) != "null" {
			t.Errorf("event[%d] data = %s, want nil/null", i, got.Data)
		}
	}
}

// TestMarshalDataNil verifies marshalData returns nil for nil input.
func TestMarshalDataNil(t *testing.T) {
	if got := marshalData(nil); got != nil {
		t.Errorf("marshalData(nil) = %s, want nil", got)
	}
}

// helpers

func mustUnmarshal(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", field, got, want)
	}
}

func assertEqualInt(t *testing.T, field string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", field, got, want)
	}
}

// --- Concurrent append tests ---

func TestWALConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	const goroutines = 20
	const eventsPerGoroutine = 50
	totalEvents := goroutines * eventsPerGoroutine

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, totalEvents)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				e := Event{
					Timestamp: time.Now(),
					Type:      EventWorkerSpawned,
					WorkerID:  fmt.Sprintf("w-%d-%d", gid, i),
					Data:      marshalData(WorkerSpawnedData{Role: "impl"}),
				}
				if _, err := wal.Append(e); err != nil {
					errCh <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("append error: %v", err)
	}

	// Verify final sequence.
	if wal.Seq() != uint64(totalEvents) {
		t.Errorf("seq = %d, want %d", wal.Seq(), totalEvents)
	}

	// Replay and verify all events present with unique, sequential sequence numbers.
	var replayed []Event
	err = wal.Replay(func(e Event) error {
		replayed = append(replayed, e)
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != totalEvents {
		t.Fatalf("replayed %d events, want %d", len(replayed), totalEvents)
	}

	// Check that sequence numbers form a contiguous 1..N range.
	seqSeen := make(map[uint64]bool, totalEvents)
	for _, e := range replayed {
		if seqSeen[e.Sequence] {
			t.Errorf("duplicate seq %d", e.Sequence)
		}
		seqSeen[e.Sequence] = true
	}
	for i := uint64(1); i <= uint64(totalEvents); i++ {
		if !seqSeen[i] {
			t.Errorf("missing seq %d", i)
		}
	}
}

func TestWALConcurrentAppend_NoDataRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.wal")

	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wal.Close()

	// Run append, Seq, and Poisoned concurrently to test for data races
	// (this test's value comes from running with -race).
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			wal.Append(Event{Timestamp: time.Now(), Type: EventPoolCreated})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = wal.Seq()
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = wal.Poisoned()
		}
	}()

	wg.Wait()
}
