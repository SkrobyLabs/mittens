package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// ComputeImageTag
// ---------------------------------------------------------------------------

func TestComputeImageTag(t *testing.T) {
	tests := []struct {
		name       string
		extensions []*registry.Extension
		want       string
	}{
		{
			name:       "no extensions returns latest",
			extensions: nil,
			want:       "latest",
		},
		{
			name: "one extension with tag",
			extensions: []*registry.Extension{
				{
					Enabled: true,
					RawArg:  "8",
					Build:   &registry.BuildConfig{ImageTag: "dotnet{{.Arg}}"},
				},
			},
			want: "dotnet8",
		},
		{
			name: "multiple sorted",
			extensions: []*registry.Extension{
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "kubectl"},
				},
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
			},
			want: "aws-kubectl",
		},
		{
			name: "disabled skipped",
			extensions: []*registry.Extension{
				{
					Enabled: false,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "gcp"},
				},
			},
			want: "gcp",
		},
		{
			name: "no build config returns latest",
			extensions: []*registry.Extension{
				{Enabled: true},
			},
			want: "latest",
		},
		{
			name: "all disabled returns latest",
			extensions: []*registry.Extension{
				{
					Enabled: false,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
			},
			want: "latest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeImageTag(tc.extensions)
			if got != tc.want {
				t.Errorf("ComputeImageTag() = %q, want %q", got, tc.want)
			}
		})
	}
}
