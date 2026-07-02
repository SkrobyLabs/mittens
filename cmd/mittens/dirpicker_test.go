package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestDirPickerShowsHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".ssh"))
	mkdir(t, filepath.Join(root, "visible"))

	m := newDirPickerModel(root, nil, "")

	if !dirPickerHasEntry(m, ".ssh") {
		t.Fatalf("hidden directory .ssh not shown: %#v", m.entries)
	}
	if !dirPickerHasEntry(m, "visible") {
		t.Fatalf("visible directory not shown: %#v", m.entries)
	}
}

func TestDirPickerSearchFiltersAndCloses(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".config"))
	mkdir(t, filepath.Join(root, "workspace"))

	model := newDirPickerModel(root, nil, "")

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	model = updated.(dirPickerModel)
	if !model.searching {
		t.Fatal("expected search mode after f")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("conf")})
	model = updated.(dirPickerModel)
	if len(model.entries) != 1 || model.entries[0].name != ".config" {
		t.Fatalf("search entries = %#v, want .config only", model.entries)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(dirPickerModel)
	if model.searching {
		t.Fatal("enter should close search mode")
	}
	if !dirPickerHasEntry(model, ".config") || dirPickerHasEntry(model, "workspace") {
		t.Fatalf("search should remain applied after closing: %#v", model.entries)
	}
}

func TestDirPickerArrowsLeaveSearchAndContinueFromCursor(t *testing.T) {
	// Empty query keeps the full sorted list (.config, deployments, workspace).
	// dirpicker search matches on the full path, which embeds the temp-dir name,
	// so an empty query is used to keep the result set deterministic.
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".config"))
	mkdir(t, filepath.Join(root, "deployments"))
	mkdir(t, filepath.Join(root, "workspace"))

	newModel := func(cursor int) dirPickerModel {
		m := newDirPickerModel(root, nil, "")
		m.searching = true
		m.search = ""
		m.applySearch()
		m.cursor = cursor
		return m
	}

	// Down continues from the cursor (1 -> 2), leaving search.
	updated, _ := newModel(1).Update(tea.KeyMsg{Type: tea.KeyDown})
	model := updated.(dirPickerModel)
	if model.searching {
		t.Fatal("down should close search mode")
	}
	if model.cursor != 2 {
		t.Fatalf("down cursor = %d, want 2", model.cursor)
	}

	// Up continues from the cursor (1 -> 0), leaving search.
	updated, _ = newModel(1).Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(dirPickerModel)
	if model.searching {
		t.Fatal("up should close search mode")
	}
	if model.cursor != 0 {
		t.Fatalf("up cursor = %d, want 0", model.cursor)
	}

	// Search-exit wraps: Down from the last entry -> first.
	updated, _ = newModel(2).Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(dirPickerModel)
	if model.cursor != 0 {
		t.Fatalf("down-wrap cursor = %d, want 0", model.cursor)
	}

	// Search-exit wraps: Up from the first entry -> last.
	updated, _ = newModel(0).Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(dirPickerModel)
	if model.cursor != 2 {
		t.Fatalf("up-wrap cursor = %d, want 2", model.cursor)
	}
}

func TestDirPickerMainListWrapsAround(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "alpha"))
	mkdir(t, filepath.Join(root, "beta"))
	mkdir(t, filepath.Join(root, "gamma"))

	// Down from the last entry wraps to the first.
	model := newDirPickerModel(root, nil, "")
	model.cursor = 2
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(dirPickerModel)
	if model.cursor != 0 {
		t.Fatalf("down-wrap cursor = %d, want 0", model.cursor)
	}

	// Up from the first entry wraps to the last, keeping it visible.
	model = newDirPickerModel(root, nil, "")
	model.height = 2
	model.cursor = 0
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(dirPickerModel)
	if model.cursor != 2 {
		t.Fatalf("up-wrap cursor = %d, want 2", model.cursor)
	}
	if model.cursor < model.offset || model.cursor >= model.offset+model.height {
		t.Fatalf("cursor %d outside viewport [%d,%d)", model.cursor, model.offset, model.offset+model.height)
	}
}

func TestDirPickerDownLeavesSearchWithNoResults(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "workspace"))

	model := newDirPickerModel(root, nil, "")
	model.searching = true
	model.search = "missing"
	model.applySearch()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(dirPickerModel)

	if model.searching {
		t.Fatal("down should close search mode")
	}
	// Exit now goes through stepCursor's empty-list no-op, leaving cursor/offset 0.
	if model.cursor != 0 || model.offset != 0 {
		t.Fatalf("cursor/offset = %d/%d, want 0/0", model.cursor, model.offset)
	}
	if len(model.entries) != 0 {
		t.Fatalf("entries = %#v, want none", model.entries)
	}
}

func TestDirPickerEnterDoneAndCtrlEnterNavigates(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	mkdir(t, child)
	mkdir(t, filepath.Join(child, "grandchild"))

	model := newDirPickerModel(root, nil, "")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(dirPickerModel)
	if model.currentDir != child {
		t.Fatalf("ctrl+j currentDir = %q, want %q", model.currentDir, child)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(dirPickerModel)
	if !model.done {
		t.Fatal("enter should mark picker done")
	}
	if cmd == nil {
		t.Fatal("enter should quit picker")
	}
}

func TestDirPickerViewPadsEveryTerminalLine(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "deployments"))

	model := newDirPickerModel(root, nil, "")
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	model = updated.(dirPickerModel)
	model.searching = true
	model.search = "de"
	model.applySearch()

	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 8 {
		t.Fatalf("line count = %d, want 8\n%s", len(lines), view)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 32 {
			t.Fatalf("line %d width = %d, want 32: %q", i, got, line)
		}
	}
}

func TestFixedTerminalBlockTruncatesHeightAndPadsWidth(t *testing.T) {
	block := fixedTerminalBlock("short\nlonger\nthis-line-is-too-long", 8, 3)
	lines := strings.Split(block, "\n")
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 8 {
			t.Fatalf("line %d width = %d, want 8: %q", i, got, line)
		}
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func dirPickerHasEntry(m dirPickerModel, name string) bool {
	for _, entry := range m.entries {
		if entry.name == name {
			return true
		}
	}
	return false
}
