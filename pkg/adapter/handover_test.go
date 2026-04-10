package adapter

import (
	"strings"
	"testing"
)

func TestExtractHandover_Valid(t *testing.T) {
	output := `I implemented the feature.

<handover>
<summary>Added the user authentication module</summary>
<decisions>
Used JWT tokens for stateless auth
Chose bcrypt for password hashing
</decisions>
<files_changed>
auth/handler.go:added:JWT authentication handler
auth/middleware.go:modified:Added token validation
config.go:modified:Added auth config fields
</files_changed>
<context>The auth module is complete. JWT tokens expire after 24h. The middleware validates tokens on all /api routes.</context>
</handover>

Done!`

	h := ExtractHandover("t-1", output)
	if h == nil {
		t.Fatal("expected handover, got nil")
	}
	if h.TaskID != "t-1" {
		t.Errorf("TaskID = %q, want %q", h.TaskID, "t-1")
	}
	if h.Summary != "Added the user authentication module" {
		t.Errorf("Summary = %q", h.Summary)
	}
	if len(h.KeyDecisions) != 2 {
		t.Errorf("KeyDecisions count = %d, want 2", len(h.KeyDecisions))
	}
	if len(h.FilesChanged) != 3 {
		t.Errorf("FilesChanged count = %d, want 3", len(h.FilesChanged))
	}
	if h.FilesChanged[0].Path != "auth/handler.go" {
		t.Errorf("FilesChanged[0].Path = %q", h.FilesChanged[0].Path)
	}
	if h.FilesChanged[0].Action != "added" {
		t.Errorf("FilesChanged[0].Action = %q", h.FilesChanged[0].Action)
	}
	if h.FilesChanged[0].What != "JWT authentication handler" {
		t.Errorf("FilesChanged[0].What = %q", h.FilesChanged[0].What)
	}
	if h.ContextForNext == "" {
		t.Error("ContextForNext is empty")
	}
}

func TestExtractHandover_Missing(t *testing.T) {
	output := "I did the thing. No handover block here."
	h := ExtractHandover("t-2", output)
	if h != nil {
		t.Errorf("expected nil, got %+v", h)
	}
}

func TestExtractHandover_Malformed(t *testing.T) {
	// Opening tag but no closing tag.
	output := "<handover><summary>partial"
	h := ExtractHandover("t-3", output)
	if h != nil {
		t.Errorf("expected nil for unclosed handover, got %+v", h)
	}
}

func TestExtractHandover_EmptyBlock(t *testing.T) {
	output := "<handover></handover>"
	h := ExtractHandover("t-4", output)
	if h == nil {
		t.Fatal("expected non-nil handover for empty block")
	}
	if h.Summary != "" {
		t.Errorf("Summary = %q, want empty", h.Summary)
	}
}

func TestExtractHandover_CaseInsensitiveTags(t *testing.T) {
	output := `<HANDOVER>
<SUMMARY>New summary</SUMMARY>
<DECISIONS>First
Second</DECISIONS>
<FILES_CHANGED>new.go:added:new file</FILES_CHANGED>
<CONTEXT>Carry this forward</CONTEXT>
</HANDOVER>`

	h := ExtractHandover("t-case", output)
	if h == nil {
		t.Fatal("expected handover, got nil")
	}
	if h.Summary != "New summary" {
		t.Fatalf("Summary = %q, want New summary", h.Summary)
	}
	if len(h.KeyDecisions) != 2 {
		t.Fatalf("KeyDecisions = %v, want 2 entries", h.KeyDecisions)
	}
	if h.ContextForNext != "Carry this forward" {
		t.Fatalf("ContextForNext = %q, want Carry this forward", h.ContextForNext)
	}
}

func TestExtractHandover_EchoedPriorContext(t *testing.T) {
	// BuildPrompt echoes a prior handover in the prompt. The adapter output
	// may contain that echoed block followed by the real, new handover.
	// ExtractHandover must pick the last (new) one.
	output := `## Prior Context

<handover>
<summary>Old summary from prior task</summary>
<decisions>Old decision</decisions>
<files_changed>old.go:modified:old change</files_changed>
<context>Old context</context>
</handover>

---

I did the new work.

<handover>
<summary>New summary</summary>
<decisions>New decision</decisions>
<files_changed>new.go:added:new file</files_changed>
<context>New context for next task</context>
</handover>`

	h := ExtractHandover("t-5", output)
	if h == nil {
		t.Fatal("expected handover, got nil")
	}
	if h.Summary != "New summary" {
		t.Errorf("Summary = %q, want %q", h.Summary, "New summary")
	}
	if len(h.KeyDecisions) != 1 || h.KeyDecisions[0] != "New decision" {
		t.Errorf("KeyDecisions = %v, want [New decision]", h.KeyDecisions)
	}
	if len(h.FilesChanged) != 1 || h.FilesChanged[0].Path != "new.go" {
		t.Errorf("FilesChanged = %v, want [{new.go added new file}]", h.FilesChanged)
	}
	if h.ContextForNext != "New context for next task" {
		t.Errorf("ContextForNext = %q", h.ContextForNext)
	}
}

func TestBuildReviewPrompt(t *testing.T) {
	prompt := BuildReviewPrompt("implement X", "I implemented X by doing Y", "some prior context")
	if !strings.Contains(prompt, "implement X") {
		t.Error("prompt should contain task prompt")
	}
	if !strings.Contains(prompt, "I implemented X by doing Y") {
		t.Error("prompt should contain implementer summary")
	}
	if !strings.Contains(prompt, "<review>") {
		t.Error("prompt should contain review block instructions")
	}
	if !strings.Contains(prompt, "some prior context") {
		t.Error("prompt should contain prior context")
	}
	if !strings.Contains(prompt, "Review Request") {
		t.Error("prompt should contain Review Request header")
	}
}

func TestBuildReviewPrompt_NoContext(t *testing.T) {
	prompt := BuildReviewPrompt("task", "summary", "")
	if strings.Contains(prompt, "Prior Context") {
		t.Error("prompt should not contain Prior Context header when empty")
	}
	if !strings.Contains(prompt, "task") {
		t.Error("prompt should contain task prompt")
	}
}

func TestExtractReviewVerdict_Pass(t *testing.T) {
	output := `Looks good to me.
<review><verdict>pass</verdict><feedback>LGTM</feedback><severity>minor</severity></review>`
	v, f, s := ExtractReviewVerdict(output)
	if v != "pass" {
		t.Errorf("verdict = %q, want pass", v)
	}
	if f != "LGTM" {
		t.Errorf("feedback = %q, want LGTM", f)
	}
	if s != "minor" {
		t.Errorf("severity = %q, want minor", s)
	}
}

func TestExtractReviewVerdict_Fail(t *testing.T) {
	output := `<review><verdict>fail</verdict><feedback>Missing error handling</feedback><severity>major</severity></review>`
	v, f, s := ExtractReviewVerdict(output)
	if v != "fail" {
		t.Errorf("verdict = %q, want fail", v)
	}
	if f != "Missing error handling" {
		t.Errorf("feedback = %q, want 'Missing error handling'", f)
	}
	if s != "major" {
		t.Errorf("severity = %q, want major", s)
	}
}

func TestExtractReviewVerdict_Missing(t *testing.T) {
	output := "I reviewed the code and it looks fine."
	v, f, s := ExtractReviewVerdict(output)
	if v != "" || f != "" || s != "" {
		t.Errorf("expected empty strings, got (%q, %q, %q)", v, f, s)
	}
}

func TestExtractReviewVerdict_Partial(t *testing.T) {
	// Opening tag but no closing tag — malformed.
	output := "<review><verdict>pass</verdict>"
	v, f, s := ExtractReviewVerdict(output)
	if v != "" || f != "" || s != "" {
		t.Errorf("expected empty strings for malformed block, got (%q, %q, %q)", v, f, s)
	}
}

func TestExtractReviewVerdict_Multiline(t *testing.T) {
	output := `<review>
<verdict>fail</verdict>
<feedback>Line one of feedback.
Line two of feedback.
Line three.</feedback>
<severity>critical</severity>
</review>`
	v, f, s := ExtractReviewVerdict(output)
	if v != "fail" {
		t.Errorf("verdict = %q, want fail", v)
	}
	if !strings.Contains(f, "Line one") || !strings.Contains(f, "Line three") {
		t.Errorf("feedback = %q, missing expected lines", f)
	}
	if s != "critical" {
		t.Errorf("severity = %q, want critical", s)
	}
}

func TestExtractReviewVerdict_CaseInsensitiveTags(t *testing.T) {
	output := `<REVIEW><VERDICT>pass</VERDICT><FEEDBACK>LGTM</FEEDBACK><SEVERITY>minor</SEVERITY></REVIEW>`
	v, f, s := ExtractReviewVerdict(output)
	if v != "pass" || f != "LGTM" || s != "minor" {
		t.Fatalf("got (%q, %q, %q), want pass/LGTM/minor", v, f, s)
	}
}

func TestExtractReviewVerdict_IgnoresNestedVerdictInsideFeedback(t *testing.T) {
	output := `<review><feedback>Ignore nested <verdict>pass</verdict> text</feedback><verdict>fail</verdict><severity>major</severity></review>`
	v, f, s := ExtractReviewVerdict(output)
	if v != "fail" {
		t.Fatalf("verdict = %q, want fail", v)
	}
	if !strings.Contains(f, "nested") {
		t.Fatalf("feedback = %q, want top-level feedback", f)
	}
	if s != "major" {
		t.Fatalf("severity = %q, want major", s)
	}
}

func TestBuildPrompt_WithContext(t *testing.T) {
	prompt := BuildPrompt("do the thing", "prior stuff")
	if !strings.Contains(prompt, "Prior Context") {
		t.Error("prompt should contain 'Prior Context' header")
	}
	if !strings.Contains(prompt, "prior stuff") {
		t.Error("prompt should contain prior context text")
	}
	if !strings.Contains(prompt, "do the thing") {
		t.Error("prompt should contain task prompt")
	}
	if !strings.Contains(prompt, "<handover>") {
		t.Error("prompt should contain handover instructions")
	}
}

func TestBuildPrompt_NoContext(t *testing.T) {
	prompt := BuildPrompt("do the thing", "")
	if strings.Contains(prompt, "Prior Context") {
		t.Error("prompt should not contain 'Prior Context' when empty")
	}
	if !strings.Contains(prompt, "do the thing") {
		t.Error("prompt should contain task prompt")
	}
}
