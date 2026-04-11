package adapter

import "testing"

func TestValidateCouncilTurnArtifact_NilCandidateMatrix(t *testing.T) {
	validPlan := &PlanArtifact{
		Title: "Parser plan",
		Tasks: []PlanArtifactTask{{
			ID:         "t1",
			Title:      "Normalize parser errors",
			Prompt:     "Do the work.",
			Complexity: "medium",
		}},
	}

	tests := []struct {
		name             string
		turn             int
		adoptedPriorPlan bool
		stance           string
		candidatePlan    *PlanArtifact
		wantErr          string
	}{
		{name: "turn one valid candidate accepted", turn: 1, stance: "propose", candidatePlan: validPlan},
		{name: "turn one adopted nil rejected", turn: 1, adoptedPriorPlan: true, stance: "converged", wantErr: "candidate plan must not be nil"},
		{name: "turn one nil rejected", turn: 1, stance: "propose", wantErr: "candidate plan must not be nil"},
		{name: "turn two adopted converged nil accepted", turn: 2, adoptedPriorPlan: true, stance: "converged"},
		{name: "turn two adopted converged with plan accepted", turn: 2, adoptedPriorPlan: true, stance: "converged", candidatePlan: validPlan},
		{name: "turn two adopted revise nil rejected", turn: 2, adoptedPriorPlan: true, stance: "revise", wantErr: "candidate plan must not be nil"},
		{name: "turn two not adopted revise nil rejected", turn: 2, stance: "revise", wantErr: "candidate plan must not be nil"},
		{name: "turn two not adopted converged nil rejected", turn: 2, stance: "converged", wantErr: "candidate plan must not be nil"},
		{name: "turn two adopted propose nil rejected", turn: 2, adoptedPriorPlan: true, stance: "propose", wantErr: "candidate plan must not be nil"},
		{name: "turn three adopted converged nil accepted", turn: 3, adoptedPriorPlan: true, stance: "converged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifact := &CouncilTurnArtifact{
				Seat:             "A",
				Turn:             tt.turn,
				Stance:           tt.stance,
				CandidatePlan:    tt.candidatePlan,
				AdoptedPriorPlan: tt.adoptedPriorPlan,
			}
			err := validateCouncilTurnArtifact(artifact)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateCouncilTurnArtifact() error = %v, want nil", err)
			}
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("validateCouncilTurnArtifact() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("validateCouncilTurnArtifact() error = %q, want %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}
