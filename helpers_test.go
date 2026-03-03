package main

import (
	"os"
	"path/filepath"
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
