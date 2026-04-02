package main

import (
	"testing"
)

func TestRunClean_ParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantDry bool
		wantImg bool
	}{
		{"no flags", nil, false, false},
		{"dry-run only", []string{"--dry-run"}, true, false},
		{"images only", []string{"--images"}, false, true},
		{"both flags", []string{"--dry-run", "--images"}, true, true},
		{"reversed order", []string{"--images", "--dry-run"}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dryRun, images, err := parseCleanFlags(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if dryRun != tc.wantDry {
				t.Errorf("dryRun = %v, want %v", dryRun, tc.wantDry)
			}
			if images != tc.wantImg {
				t.Errorf("images = %v, want %v", images, tc.wantImg)
			}
		})
	}
}

func TestRunClean_ParseFlags_UnknownFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"--force"}},
		{"unknown mixed with valid", []string{"--dry-run", "--nope"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseCleanFlags(tc.args)
			if err == nil {
				t.Fatal("expected error for unknown flag, got nil")
			}
		})
	}
}

func TestListStoppedContainers_FiltersMittensPrefix(t *testing.T) {
	// filterMittensNames should only keep names starting with "mittens-"
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"all mittens", []string{"mittens-123", "mittens-456"}, []string{"mittens-123", "mittens-456"}},
		{"mixed names", []string{"mittens-123", "other-container", "mittens-789"}, []string{"mittens-123", "mittens-789"}},
		{"none match", []string{"redis", "postgres"}, nil},
		{"empty", nil, nil},
		{"prefix but not dash", []string{"mittensdata"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterMittensNames(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
