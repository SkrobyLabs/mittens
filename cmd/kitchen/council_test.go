package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
)

func TestDecideCouncilNext(t *testing.T) {
	prev := &adapter.PlanArtifact{
		Title: "Parser plan",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Normalize parser errors",
			Prompt:     "Do the work.",
			Complexity: "medium",
		}},
	}
	equal := &adapter.PlanArtifact{
		Title: "Parser plan",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Normalize parser errors",
			Prompt:     "Do the work.",
			Complexity: "medium",
		}},
	}
	different := &adapter.PlanArtifact{
		Title: "Parser plan",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Normalize parser errors",
			Prompt:     "Do different work.",
			Complexity: "medium",
		}},
	}

	tests := []struct {
		name     string
		bundle   StoredPlan
		artifact *adapter.CouncilTurnArtifact
		want     string
		warnings int
	}{
		{
			name: "explicit adoption converges",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 2, AdoptedPriorPlan: true, Stance: "converged", CandidatePlan: equal},
			want:     councilConverged,
		},
		{
			name: "structural equality auto converges without disagreements",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal},
			want:     councilAutoConverged,
		},
		{
			name: "structural equality auto converges with non critical warnings",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{
				Turn:          2,
				CandidatePlan: equal,
				Disagreements: []adapter.CouncilDisagreement{{ID: "d1", Severity: "major", Title: "Minor concern", Category: "scope", Impact: "note"}},
			},
			want:     councilAutoConverged,
			warnings: 1,
		},
		{
			name: "non equal candidate continues",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: different}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: different},
			want:     councilContinue,
		},
		{
			name: "turn one gate prevents auto converge",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 1,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: equal},
			want:     councilContinue,
		},
		{
			name: "empty prior turns gate prevents auto converge",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal},
			want:     councilContinue,
		},
		{
			name: "blocking question takes precedence",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{
				Turn:             2,
				CandidatePlan:    equal,
				QuestionsForUser: []adapter.CouncilUserQuestion{{ID: "q1", Question: "Need operator input", Blocking: true}},
			},
			want: councilAskUser,
		},
		{
			name: "critical disagreement blocks auto converge",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{
				Turn:          2,
				CandidatePlan: equal,
				Disagreements: []adapter.CouncilDisagreement{{ID: "d1", Severity: "critical", Title: "Still blocked", Category: "scope", Impact: "bad"}},
			},
			want: councilContinue,
		},
		{
			name: "blocked turn never auto converges even with equal candidate",
			bundle: StoredPlan{Execution: ExecutionRecord{
				CouncilMaxTurns:       4,
				CouncilTurnsCompleted: 2,
				CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Turn: 1, CandidatePlan: prev}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Turn: 2, CandidatePlan: equal}}},
			}},
			artifact: &adapter.CouncilTurnArtifact{Turn: 2, Stance: "blocked", CandidatePlan: equal},
			want:     councilContinue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := decideCouncilNext(tt.bundle, tt.artifact)
			if got != tt.want {
				t.Fatalf("decision = %q, want %q", got, tt.want)
			}
			if len(warnings) != tt.warnings {
				t.Fatalf("warnings = %+v, want %d entries", warnings, tt.warnings)
			}
		})
	}
}

func TestCouncilDecisionHelpers(t *testing.T) {
	if !canAutoApproveCouncil(ExecutionRecord{AutoApproved: true, CouncilFinalDecision: councilConverged}) {
		t.Fatal("expected explicit converged decision to auto-approve")
	}
	if !canAutoApproveCouncil(ExecutionRecord{AutoApproved: true, CouncilFinalDecision: councilConverged, CouncilWarnings: nil}) {
		t.Fatal("expected warning-free converged decision to auto-approve")
	}
	if canAutoApproveCouncil(ExecutionRecord{AutoApproved: true, CouncilFinalDecision: councilConverged, CouncilWarnings: []adapter.CouncilDisagreement{{ID: "d1"}}}) {
		t.Fatal("warnings should disable auto-approve")
	}
	if !canExtendCouncil(planStatePendingApproval, ExecutionRecord{
		CouncilTurnsCompleted: 4,
		CouncilMaxTurns:       4,
		CouncilFinalDecision:  councilConverged,
		CouncilWarnings:       []adapter.CouncilDisagreement{{ID: "d1"}},
	}) {
		t.Fatal("expected converged warning state to allow extension")
	}
}
