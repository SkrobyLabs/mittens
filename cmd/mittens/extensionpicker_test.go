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

func TestExtensionPickerDownLeavesSearchAndFocusesFirstResult(t *testing.T) {
	model := newExtensionPickerModel("Extensions", testPickerExtensions(), nil)
	model.searching = true
	model.search = "install"
	model.applySearch()
	model.cursor = 1
	model.offset = 1

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(extensionPickerModel)

	if model.searching {
		t.Fatal("down should close search mode")
	}
	if model.cursor != 0 || model.offset != 0 {
		t.Fatalf("cursor/offset = %d/%d, want 0/0", model.cursor, model.offset)
	}
	if len(model.items) != 2 || model.items[0].name != "dotnet" {
		t.Fatalf("items = %#v, want dotnet first", model.items)
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
	if model.cursor != 0 || model.offset != 0 {
		t.Fatalf("cursor/offset = %d/%d, want 0/0", model.cursor, model.offset)
	}
	if len(model.items) != 0 {
		t.Fatalf("items = %#v, want none", model.items)
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
