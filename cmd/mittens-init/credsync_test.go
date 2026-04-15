package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldAcceptPull_NormalMode(t *testing.T) {
	tests := []struct {
		name     string
		remote   int64
		local    int64
		want     bool
	}{
		{"remote fresher", 2000, 1000, true},
		{"remote equal", 1000, 1000, false},
		{"remote stale", 500, 1000, false},
		{"remote zero", 0, 1000, false},
		{"local zero remote positive", 1000, 0, true},
		{"both zero", 0, 0, false},
		{"local negative remote positive", 1000, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAcceptPull(tt.remote, tt.local, 0, false)
			if got != tt.want {
				t.Errorf("shouldAcceptPull(%d, %d, 0, false) = %v, want %v",
					tt.remote, tt.local, got, tt.want)
			}
		})
	}
}

func TestShouldAcceptPull_RefreshPending(t *testing.T) {
	const origExp int64 = 1700000000

	tests := []struct {
		name   string
		remote int64
		want   bool
	}{
		{"broker has original creds", origExp, false},
		{"broker has stale creds", origExp - 1000, false},
		{"broker has genuinely new creds", origExp + 3600000, true},
		{"broker has slightly newer creds", origExp + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// localExp is 1 (faked by triggerTokenRefresh)
			got := shouldAcceptPull(tt.remote, 1, origExp, true)
			if got != tt.want {
				t.Errorf("shouldAcceptPull(%d, 1, %d, true) = %v, want %v",
					tt.remote, origExp, got, tt.want)
			}
		})
	}
}

func TestCredSyncSource(t *testing.T) {
	cfg := &config{
		ProviderName:  "anthropic",
		ContainerName: "mittens-kitchen-w-101",
		InstanceName:  "review-seat-a",
	}
	got := credSyncSource(cfg)
	want := "provider=anthropic container=mittens-kitchen-w-101 instance=review-seat-a"
	if got != want {
		t.Fatalf("credSyncSource() = %q, want %q", got, want)
	}
}

func TestCredLoggerWriteIncludesSource(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "credsync.log")
	logger := newCredLogger(nil, logPath, "provider=anthropic container=mittens-kitchen-w-101")
	logger.write("refresh-trigger — expires in %dms", 120000)
	_ = logger.file.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[cred-sync provider=anthropic container=mittens-kitchen-w-101] refresh-trigger — expires in 120000ms") {
		t.Fatalf("expected source-aware credsync log entry, got %q", string(data))
	}
}
