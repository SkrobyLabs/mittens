package main

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
