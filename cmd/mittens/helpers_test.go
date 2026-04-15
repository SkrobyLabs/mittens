package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestRotateBrokerLog(t *testing.T) {
	t.Run("rotates file over threshold", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "broker.log")
		data := make([]byte, maxBrokerLogSize+1)
		os.WriteFile(logPath, data, 0644)

		rotateBrokerLog(logPath)

		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatal("original file should be removed after rotation")
		}
		rotated := logPath + ".1"
		fi, err := os.Stat(rotated)
		if err != nil {
			t.Fatalf("rotated file should exist: %v", err)
		}
		if fi.Size() != int64(maxBrokerLogSize+1) {
			t.Fatalf("rotated file size mismatch: got %d", fi.Size())
		}
	})

	t.Run("does not rotate file under threshold", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "broker.log")
		os.WriteFile(logPath, []byte("small"), 0644)

		rotateBrokerLog(logPath)

		if _, err := os.Stat(logPath); err != nil {
			t.Fatal("original file should still exist")
		}
		if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
			t.Fatal("rotated file should not exist")
		}
	})

	t.Run("no error when file does not exist", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "nonexistent.log")
		rotateBrokerLog(logPath) // should not panic
	})

	t.Run("overwrites existing .1 file", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "broker.log")
		rotated := logPath + ".1"
		os.WriteFile(rotated, []byte("old"), 0644)

		data := make([]byte, maxBrokerLogSize+1)
		os.WriteFile(logPath, data, 0644)

		rotateBrokerLog(logPath)

		fi, err := os.Stat(rotated)
		if err != nil {
			t.Fatalf("rotated file should exist: %v", err)
		}
		if fi.Size() != int64(maxBrokerLogSize+1) {
			t.Fatalf("rotated file should be overwritten: got size %d", fi.Size())
		}
	})
}

func TestIsSensitiveEnvKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"AWS_SECRET_ACCESS_KEY", true},
		{"ANTHROPIC_API_KEY", true},
		{"DATABASE_PASSWORD", true},
		{"MY_TOKEN_VALUE", true},
		{"HOME", false},
		{"PATH", false},
		{"VERBOSE", false},
		{"AWS_PROFILE", false},
		{"my_secret", true},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := isSensitiveEnvKey(tc.key)
			if got != tc.want {
				t.Errorf("isSensitiveEnvKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestRandomHex(t *testing.T) {
	t.Run("length and format", func(t *testing.T) {
		s, err := randomHex(16)
		if err != nil {
			t.Fatalf("randomHex(16) returned error: %v", err)
		}
		if len(s) != 32 {
			t.Fatalf("expected length 32, got %d", len(s))
		}
		if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(s) {
			t.Errorf("unexpected characters in hex string: %q", s)
		}
	})

	t.Run("uniqueness", func(t *testing.T) {
		a, err := randomHex(16)
		if err != nil {
			t.Fatalf("first call returned error: %v", err)
		}
		b, err := randomHex(16)
		if err != nil {
			t.Fatalf("second call returned error: %v", err)
		}
		if a == b {
			t.Errorf("two calls produced identical values: %q", a)
		}
	})
}

func TestHomeDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/test-home-dir")
	got := homeDir()
	if got != "/tmp/test-home-dir" {
		t.Fatalf("homeDir() = %q, want %q", got, "/tmp/test-home-dir")
	}
}

func TestMittensLogPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/test-home-dir")

	tests := []struct {
		category string
		want     string
		wantErr  bool
	}{
		{category: "", want: "/tmp/test-home-dir/.mittens/logs/broker.log"},
		{category: "broker", want: "/tmp/test-home-dir/.mittens/logs/broker.log"},
		{category: "credsync", want: "/tmp/test-home-dir/.mittens/logs/credsync.log"},
		{category: "broker-debug", want: "/tmp/test-home-dir/.mittens/logs/broker-debug.log"},
		{category: "unknown", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.category, func(t *testing.T) {
			got, err := mittensLogPath(tc.category)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mittensLogPath(%q) error = nil, want error", tc.category)
				}
				return
			}
			if err != nil {
				t.Fatalf("mittensLogPath(%q) error = %v", tc.category, err)
			}
			if got != tc.want {
				t.Fatalf("mittensLogPath(%q) = %q, want %q", tc.category, got, tc.want)
			}
		})
	}
}
