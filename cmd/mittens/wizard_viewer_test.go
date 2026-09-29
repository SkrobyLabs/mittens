package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWizardViewerScrollAndConfirmation(t *testing.T) {
	m := wizardViewer{title: "Review", content: strings.Repeat("A long configuration value\n", 100), confirm: true, viewport: viewport.New(40, 8)}
	m.wrap()
	model, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = model.(wizardViewer)
	if m.viewport.Height != 7 {
		t.Fatalf("viewport height = %d", m.viewport.Height)
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = model.(wizardViewer)
	if m.viewport.YOffset == 0 || m.accepted {
		t.Fatal("page down must scroll without saving")
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.(wizardViewer).accepted || cmd != nil {
		t.Fatal("Enter must not accidentally confirm a review")
	}
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.(wizardViewer).accepted || cmd == nil {
		t.Fatal("Escape must return without saving")
	}
	model, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !model.(wizardViewer).accepted || cmd == nil {
		t.Fatal("explicit save key did not confirm")
	}
}
