package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

func testWizardMenu(count int) wizardMenu {
	var options []huh.Option[string]
	for i := 0; i < count; i++ {
		options = append(options, huh.NewOption(fmt.Sprintf("Item %d", i), fmt.Sprint(i)))
	}
	m := newWizardMenu("Setup", "", options, "0")
	m.height = 16 // Ten list rows after header and footer.
	m.keepCursorVisible()
	return m
}

func menuKey(m wizardMenu, key tea.KeyType) wizardMenu {
	result, _ := m.Update(tea.KeyMsg{Type: key})
	return result.(wizardMenu)
}

func TestWizardMenuMovesCursorBeforeScrolling(t *testing.T) {
	m := testWizardMenu(25)
	if m.visibleRows() != 10 {
		t.Fatalf("visible rows = %d", m.visibleRows())
	}
	for cursor := 1; cursor <= 6; cursor++ {
		m = menuKey(m, tea.KeyDown)
		if m.cursor != cursor || m.offset != 0 {
			t.Fatalf("list moved prematurely: cursor=%d offset=%d", m.cursor, m.offset)
		}
	}
	for cursor := 7; cursor <= 12; cursor++ {
		m = menuKey(m, tea.KeyDown)
		if m.offset != cursor-6 || m.offset+m.visibleRows()-1-m.cursor != 3 {
			t.Fatalf("bottom margin lost: %+v", m)
		}
	}
	// Reversing direction moves the cursor through the visible rows first.
	for cursor := 11; cursor >= 9; cursor-- {
		m = menuKey(m, tea.KeyUp)
		if m.cursor != cursor || m.offset != 6 {
			t.Fatalf("list moved prematurely on up: cursor=%d offset=%d", m.cursor, m.offset)
		}
	}
	m = menuKey(m, tea.KeyUp)
	if m.offset != 5 || m.cursor-m.offset != 3 {
		t.Fatalf("top margin lost: cursor=%d offset=%d", m.cursor, m.offset)
	}
}

func TestWizardMenuDoesNotScrollWhenAllItemsFit(t *testing.T) {
	m := testWizardMenu(8)
	for i := 0; i < 20; i++ {
		m = menuKey(m, tea.KeyDown)
		if m.offset != 0 {
			t.Fatalf("fitting list scrolled at cursor %d", m.cursor)
		}
	}
	m = menuKey(m, tea.KeyHome)
	m = menuKey(m, tea.KeyUp)
	if m.cursor != 7 || m.offset != 0 {
		t.Fatalf("wrap = cursor %d offset %d", m.cursor, m.offset)
	}
}

func TestWizardMenuResizeAndPagingKeepSelectionVisible(t *testing.T) {
	m := testWizardMenu(25)
	m = menuKey(m, tea.KeyEnd)
	if m.cursor != 24 || m.offset != 15 {
		t.Fatalf("end = cursor %d offset %d", m.cursor, m.offset)
	}
	result, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = result.(wizardMenu)
	for _, key := range []tea.KeyType{tea.KeyPgUp, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyUp, tea.KeyDown} {
		m = menuKey(m, key)
		if m.cursor < m.offset || m.cursor >= m.offset+m.visibleRows() {
			t.Fatalf("selection offscreen: %+v", m)
		}
	}
	result, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = result.(wizardMenu)
	if m.offset != 0 {
		t.Fatal("expanded terminal should show the full list")
	}
}

func TestWizardMenuRendersWithinTerminalAndConfirmsValue(t *testing.T) {
	m := newWizardMenu("Setup", strings.Repeat("Long workspace path/", 40), []huh.Option[string]{
		huh.NewOption(strings.Repeat("Long option ", 10), "provider"), huh.NewOption("Save and exit", "save"),
	}, "save")
	result, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = result.(wizardMenu)
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 39 {
			t.Fatalf("line exceeds terminal width: %q", line)
		}
	}
	if lipgloss.Height(m.View()) > 12 {
		t.Fatalf("view exceeds terminal height: %d", lipgloss.Height(m.View()))
	}
	cancelled, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.(wizardMenu).accepted || cmd == nil {
		t.Fatal("Escape should exit without selection")
	}
	accepted, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := accepted.(wizardMenu)
	if !final.accepted || final.options[final.cursor].Value != "save" || cmd == nil {
		t.Fatal("Enter did not confirm the selected action")
	}
}
