// Command mittens-clipboard-helper reads a clipboard image from the Windows
// clipboard and writes it as PNG to stdout.
//
// Usage: mittens-clipboard-helper.exe
//
// Exit codes:
//
//	0 — image written to stdout
//	1 — no image on clipboard or error
package main

import (
	"fmt"
	"os"
)

func main() {
	if !hasClipboardImage() {
		os.Exit(1)
	}

	pngData, err := readClipboardImage()
	if err != nil {
		fmt.Fprintf(os.Stderr, "clipboard read: %v\n", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(pngData); err != nil {
		fmt.Fprintf(os.Stderr, "stdout write: %v\n", err)
		os.Exit(1)
	}
}
