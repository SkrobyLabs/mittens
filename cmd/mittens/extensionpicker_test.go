package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestExtensionPickerSearchFiltersByNameFlagAndDescription(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	model = updated.(extensionPickerModel)
	if !model.searching {
		t.Fatal("expected search mode after f")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("cloud")})
	model = updated.(extensionPickerModel)
	if extensionPickerNames(model) != "aws" {
		t.Fatalf("description search = %s, want aws", extensionPickerNames(model))
	}

	model.search = ""
	model.applySearch()
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("dot")})
	model = updated.(extensionPickerModel)
	if extensionPickerNames(model) != "dotnet" {
		t.Fatalf("name search = %s, want dotnet", extensionPickerNames(model))
	}

	model.search = ""
	model.applySearch()
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("--go")})
	model = updated.(extensionPickerModel)
	if extensionPickerNames(model) != "go" {
		t.Fatalf("flag search = %s, want go", extensionPickerNames(model))
	}
}

func TestExtensionPickerEscapeClosesSearch(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
	model.searching = true
	model.search = "dot"
	model.applySearch()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(extensionPickerModel)
	if model.searching {
		t.Fatal("escape should close search mode")
	}
	if model.cancelled {
		t.Fatal("escape in search mode should not cancel picker")
	}
	if extensionPickerNames(model) != "dotnet" {
		t.Fatalf("search should remain applied after escape: %s", extensionPickerNames(model))
	}
}

func TestExtensionPickerArrowsLeaveSearchAndContinueFromCursor(t *testing.T) {
	// Empty query keeps the full 3-item list (aws, dotnet, go) so the result
	// set is deterministic and does not depend on which letters match.
	newModel := func(cursor int) extensionPickerModel {
		m := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
		m.searching = true
		m.search = ""
		m.applySearch()
		m.cursor = cursor
		return m
	}

	// Down continues from the cursor (1 -> 2), leaving search.
	updated, _ := newModel(1).Update(tea.KeyMsg{Type: tea.KeyDown})
	model := updated.(extensionPickerModel)
	if model.searching {
		t.Fatal("down should close search mode")
	}
	if model.cursor != 2 {
		t.Fatalf("down cursor = %d, want 2", model.cursor)
	}

	// Up continues from the cursor (1 -> 0), leaving search.
	updated, _ = newModel(1).Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(extensionPickerModel)
	if model.searching {
		t.Fatal("up should close search mode")
	}
	if model.cursor != 0 {
		t.Fatalf("up cursor = %d, want 0", model.cursor)
	}

	// Search-exit wraps: Down from the last item -> first.
	updated, _ = newModel(2).Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(extensionPickerModel)
	if model.cursor != 0 {
		t.Fatalf("down-wrap cursor = %d, want 0", model.cursor)
	}

	// Search-exit wraps: Up from the first item -> last.
	updated, _ = newModel(0).Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(extensionPickerModel)
	if model.cursor != 2 {
		t.Fatalf("up-wrap cursor = %d, want 2", model.cursor)
	}
}

func TestExtensionPickerDownLeavesSearchWithNoResults(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
	model.searching = true
	model.search = "missing"
	model.applySearch()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(extensionPickerModel)

	if model.searching {
		t.Fatal("down should close search mode")
	}
	// Exit now goes through stepCursor's empty-list no-op, leaving cursor/offset 0.
	if model.cursor != 0 || model.offset != 0 {
		t.Fatalf("cursor/offset = %d/%d, want 0/0", model.cursor, model.offset)
	}
	if len(model.items) != 0 {
		t.Fatalf("items = %#v, want none", model.items)
	}
}

func TestExtensionPickerMainListWrapsAround(t *testing.T) {
	// Down from the last item wraps to the first.
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
	model.cursor = 2
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(extensionPickerModel)
	if model.cursor != 0 {
		t.Fatalf("down-wrap cursor = %d, want 0", model.cursor)
	}

	// Up from the first item wraps to the last, keeping it visible.
	model = newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
	model.height = 2
	model.cursor = 0
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(extensionPickerModel)
	if model.cursor != 2 {
		t.Fatalf("up-wrap cursor = %d, want 2", model.cursor)
	}
	if model.cursor < model.offset || model.cursor >= model.offset+model.height {
		t.Fatalf("cursor %d outside viewport [%d,%d)", model.cursor, model.offset, model.offset+model.height)
	}
}

func TestExtensionPickerEnterSelectsHighlightedExtension(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(extensionPickerModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(extensionPickerModel)

	if model.chosen == nil || model.chosen.Name != "dotnet" {
		t.Fatalf("chosen = %#v, want dotnet", model.chosen)
	}
	if cmd == nil {
		t.Fatal("enter should quit after selecting")
	}
}

func TestExtensionPickerViewShowsConfiguredMarker(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), map[string]bool{"aws": true})

	view := model.View()
	if !strings.Contains(view, "[x] aws") {
		t.Fatalf("configured marker missing from view:\n%s", view)
	}
}

func testPickerExtensions() []*registry.Extension {
	return []*registry.Extension{
		{
			Name:        "aws",
			Description: "Mount cloud credentials",
			Flags:       []registry.ExtensionFlag{{Name: "--aws", Arg: "csv"}},
		},
		{
			Name:        "dotnet",
			Description: "Install .NET SDK",
			Flags:       []registry.ExtensionFlag{{Name: "--dotnet", Arg: "enum"}},
		},
		{
			Name:        "go",
			Description: "Install Go SDK",
			Flags:       []registry.ExtensionFlag{{Name: "--go", Arg: "enum"}},
		},
	}
}

func extensionPickerNames(model extensionPickerModel) string {
	var names []string
	for _, item := range model.items {
		names = append(names, item.name)
	}
	return strings.Join(names, ",")
}
