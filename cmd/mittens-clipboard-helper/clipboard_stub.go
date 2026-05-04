//go:build !windows
// +build !windows

package main

import "fmt"

func hasClipboardImage() bool {
	return false
}

func readClipboardImage() ([]byte, error) {
	return nil, fmt.Errorf("clipboard helper is only supported on Windows")
}
