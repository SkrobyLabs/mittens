package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

type extensionPickerItem struct {
	extension   *registry.Extension
	name        string
	flag        string
	description string
	configured  bool
}

type extensionPickerModel struct {
	title      string
	allItems   []extensionPickerItem
	items      []extensionPickerItem
	cursor     int
	chosen     *registry.Extension
	cancelled  bool
	searching  bool
	search     string
	height     int
	termHeight int
	termWidth  int
	offset     int
}

func newExtensionPickerModel(title string, extensions []*registry.Extension, selected map[string]bool) extensionPickerModel {
	m := extensionPickerModel{
		title:  title,
		height: 12,
	}
	for _, ext := range extensions {
		item := extensionPickerItem{
			extension:   ext,
			name:        ext.Name,
			flag:        extPrimaryFlag(ext),
			description: ext.Description,
		}
		if selected != nil && selected[ext.Name] {
			item.configured = true
		}
		m.allItems = append(m.allItems, item)
	}
	m.applySearch()
	return m
}

func (m *extensionPickerModel) applySearch() {
	query := strings.ToLower(strings.TrimSpace(m.search))
	if query == "" {
		m.items = append([]extensionPickerItem(nil), m.allItems...)
	} else {
		m.items = nil
		for _, item := range m.allItems {
			haystack := strings.ToLower(strings.Join([]string{item.name, item.flag, item.description}, " "))
			if strings.Contains(haystack, query) {
				m.items = append(m.items, item)
			}
		}
	}
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.height {
		m.offset = m.cursor - m.height + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m extensionPickerModel) Init() tea.Cmd {
	return nil
}

func (m extensionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
		m.termWidth = msg.Width
		m.height = msg.Height - 6
		if m.height < 3 {
			m.height = 3
		}

	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(msg)
		}
		switch msg.String() {
		case "q", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if len(m.items) > 0 {
				m.chosen = m.items[m.cursor].extension
				return m, tea.Quit
			}
		case "up", "k":
			m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.items), m.height, -1)
		case "down", "j":
			m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.items), m.height, 1)
		case "f":
			m.searching = true
			m.search = ""
			m.applySearch()
		}
	}
	return m, nil
}

func (m extensionPickerModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.searching = false
		return m, nil
	case "up":
		m.searching = false
		m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.items), m.height, -1)
		return m, nil
	case "down":
		m.searching = false
		m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.items), m.height, 1)
		return m, nil
	case "backspace", "ctrl+h":
		if len(m.search) > 0 {
			runes := []rune(m.search)
			m.search = string(runes[:len(runes)-1])
			m.applySearch()
		}
	case "ctrl+u":
		m.search = ""
		m.applySearch()
	default:
		if msg.Type == tea.KeyRunes {
			m.search += string(msg.Runes)
			m.applySearch()
		}
	}
	return m, nil
}

func (m extensionPickerModel) View() string {
	var b strings.Builder

	b.WriteString(dpStylePath.Render(m.title))
	b.WriteString("\n\n")
	if m.searching || m.search != "" {
		prompt := "Search: " + m.search
		if m.searching {
			prompt += "|"
		}
		b.WriteString(dpStyleHelp.Render("  " + prompt))
		b.WriteString("\n")
	}

	if len(m.items) == 0 {
		b.WriteString(dpStyleHelp.Render("  (no matching extensions)"))
		b.WriteString("\n")
	} else {
		end := m.offset + m.height
		if end > len(m.items) {
			end = len(m.items)
		}
		for i := m.offset; i < end; i++ {
			item := m.items[i]
			marker := "[ ]"
			if item.configured {
				marker = dpStyleSelected.Render("[x]")
			}
			cursor := "  "
			name := item.name
			if i == m.cursor {
				cursor = dpStyleCursor.Render("> ")
				name = dpStyleCursor.Render(name)
			}
			label := item.description
			if item.flag != "" {
				label = item.flag + "  " + item.description
			}
			b.WriteString(fmt.Sprintf("%s%s %s %s\n", cursor, marker, name, label))
		}
		if len(m.items) > m.height {
			b.WriteString(dpStyleHelp.Render(fmt.Sprintf("  (%d/%d)", m.cursor+1, len(m.items))))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	if m.searching {
		b.WriteString(dpStyleHelp.Render("  type to search  up/down results  enter/esc close search  backspace delete"))
	} else {
		b.WriteString(dpStyleHelp.Render("  enter select  f search  esc cancel"))
	}

	if m.termWidth > 0 && m.termHeight > 0 {
		return fixedTerminalBlock(b.String(), m.termWidth, m.termHeight)
	}
	return b.String()
}

func runExtensionPicker(title string, extensions []*registry.Extension, selected map[string]bool) (*registry.Extension, error) {
	model := newExtensionPickerModel(title, extensions, selected)
	p := tea.NewProgram(model, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return nil, err
	}

	m := result.(extensionPickerModel)
	if m.cancelled {
		return nil, errPickerCancelled
	}
	if m.chosen == nil {
		return nil, errPickerCancelled
	}
	return m.chosen, nil
}
