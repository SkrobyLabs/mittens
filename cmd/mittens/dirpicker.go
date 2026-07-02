package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	dpStylePath     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))  // cyan
	dpStyleSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))             // green
	dpStyleGit      = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("82")) // dim green
	dpStyleCursor   = lipgloss.NewStyle().Bold(true)
	dpStyleHelp     = lipgloss.NewStyle().Faint(true)
)

// ---------------------------------------------------------------------------
// Directory entry
// ---------------------------------------------------------------------------

type dirEntry struct {
	name  string
	path  string // absolute path
	isGit bool   // has .git subdirectory
}

type dirMountSelection struct {
	Path     string
	ReadOnly bool
}

// ---------------------------------------------------------------------------
// Bubbletea model
// ---------------------------------------------------------------------------

type dirPickerModel struct {
	currentDir string
	allEntries []dirEntry
	entries    []dirEntry
	cursor     int
	selected   map[string]dirMountSelection
	exclude    string // absolute path to hide (primary workspace)
	done       bool
	cancelled  bool
	searching  bool
	search     string
	height     int // visible entry rows (terminal height - 7)
	termHeight int // full terminal height
	termWidth  int // full terminal width
	offset     int // scroll offset
	err        error
}

func newDirPickerModel(startDir string, preSelected map[string]bool, exclude string) dirPickerModel {
	sel := make(map[string]dirMountSelection, len(preSelected))
	for path, readOnly := range preSelected {
		sel[path] = dirMountSelection{Path: path, ReadOnly: readOnly}
	}
	m := dirPickerModel{
		currentDir: startDir,
		selected:   sel,
		exclude:    exclude,
		height:     15,
	}
	m.loadDir(startDir)
	return m
}

func (m *dirPickerModel) loadDir(path string) {
	m.currentDir = path
	m.cursor = 0
	m.offset = 0
	m.allEntries = nil
	m.entries = nil

	entries, err := os.ReadDir(path)
	if err != nil {
		m.err = err
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(path, name)
		if m.exclude != "" && full == m.exclude {
			continue
		}
		isGit := false
		if info, err := os.Stat(filepath.Join(full, ".git")); err == nil && info.IsDir() {
			isGit = true
		}
		m.allEntries = append(m.allEntries, dirEntry{name: name, path: full, isGit: isGit})
	}
	sort.Slice(m.allEntries, func(i, j int) bool {
		return m.allEntries[i].name < m.allEntries[j].name
	})
	m.applySearch()
}

func (m *dirPickerModel) applySearch() {
	query := strings.ToLower(strings.TrimSpace(m.search))
	if query == "" {
		m.entries = append([]dirEntry(nil), m.allEntries...)
	} else {
		m.entries = nil
		for _, entry := range m.allEntries {
			if strings.Contains(strings.ToLower(entry.name), query) || strings.Contains(strings.ToLower(entry.path), query) {
				m.entries = append(m.entries, entry)
			}
		}
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
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

func (m dirPickerModel) Init() tea.Cmd {
	return nil
}

func (m dirPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
		m.termWidth = msg.Width
		// Reserve 7 lines for header(2) + scroll indicator(1) + selected count(2) + footer(2)
		m.height = msg.Height - 7
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
			m.done = true
			return m, tea.Quit

		case "ctrl+j", "ctrl+enter":
			if len(m.entries) > 0 {
				target := m.entries[m.cursor].path
				m.loadDir(target)
				return m, tea.ClearScreen
			}

		case "up", "k":
			m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.entries), m.height, -1)

		case "down", "j":
			m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.entries), m.height, 1)

		case "right", "l":
			if len(m.entries) > 0 {
				target := m.entries[m.cursor].path
				m.loadDir(target)
				return m, tea.ClearScreen
			}

		case "left", "h":
			parent := filepath.Dir(m.currentDir)
			if parent != m.currentDir {
				prev := filepath.Base(m.currentDir)
				m.loadDir(parent)
				// Try to place cursor on the directory we came from.
				for i, e := range m.entries {
					if e.name == prev {
						m.cursor = i
						if m.cursor >= m.offset+m.height {
							m.offset = m.cursor - m.height + 1
						}
						break
					}
				}
				return m, tea.ClearScreen
			}

		case " ", "x":
			if len(m.entries) > 0 {
				p := m.entries[m.cursor].path
				if _, ok := m.selected[p]; ok {
					delete(m.selected, p)
				} else {
					m.selected[p] = dirMountSelection{Path: p}
				}
			}

		case "r":
			if len(m.entries) > 0 {
				p := m.entries[m.cursor].path
				cur, ok := m.selected[p]
				if !ok {
					m.selected[p] = dirMountSelection{Path: p, ReadOnly: true}
				} else {
					cur.ReadOnly = !cur.ReadOnly
					m.selected[p] = cur
				}
			}

		case "f":
			m.searching = true
			m.search = ""
			m.applySearch()
		}
	}
	return m, nil
}

func (m dirPickerModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.searching = false
		return m, nil
	case "up":
		m.searching = false
		m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.entries), m.height, -1)
		return m, nil
	case "down":
		m.searching = false
		m.cursor, m.offset = stepCursor(m.cursor, m.offset, len(m.entries), m.height, 1)
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

func (m dirPickerModel) View() string {
	var b strings.Builder

	// Header: current path
	b.WriteString(dpStylePath.Render("Browse: " + m.currentDir))
	b.WriteString("\n\n")
	if m.searching || m.search != "" {
		prompt := "Search: " + m.search
		if m.searching {
			prompt += "█"
		}
		b.WriteString(dpStyleHelp.Render("  " + prompt))
		b.WriteString("\n")
	}

	if len(m.entries) == 0 {
		b.WriteString(dpStyleHelp.Render("  (no subdirectories)"))
		b.WriteString("\n")
	} else {
		end := m.offset + m.height
		if end > len(m.entries) {
			end = len(m.entries)
		}
		for i := m.offset; i < end; i++ {
			e := m.entries[i]

			// Checkbox
			check := "[ ]"
			if sel, ok := m.selected[e.path]; ok {
				if sel.ReadOnly {
					check = dpStyleSelected.Render("[r]")
				} else {
					check = dpStyleSelected.Render("[x]")
				}
			}

			// Name with trailing slash
			name := e.name + "/"

			// Git indicator
			git := ""
			if e.isGit {
				git = dpStyleGit.Render(" [git]")
			}

			// Cursor indicator
			cursor := "  "
			if i == m.cursor {
				cursor = dpStyleCursor.Render("> ")
				name = dpStyleCursor.Render(name)
			}

			b.WriteString(fmt.Sprintf("%s%s %s%s\n", cursor, check, name, git))
		}

		// Scroll indicator
		if len(m.entries) > m.height {
			b.WriteString(dpStyleHelp.Render(fmt.Sprintf("  (%d/%d)", m.cursor+1, len(m.entries))))
			b.WriteString("\n")
		}
	}

	// Selected count
	if len(m.selected) > 0 {
		roCount := 0
		for _, sel := range m.selected {
			if sel.ReadOnly {
				roCount++
			}
		}
		b.WriteString(dpStyleSelected.Render(fmt.Sprintf("\n  %d selected (%d read-only)", len(m.selected), roCount)))
		b.WriteString("\n")
	}

	// Footer
	b.WriteString("\n")
	if m.searching {
		b.WriteString(dpStyleHelp.Render("  type to search  up/down results  enter/esc close search  backspace delete"))
	} else {
		b.WriteString(dpStyleHelp.Render("  enter done  ←/→ navigate  ctrl+enter enter folder  f search  space toggle rw  r toggle read-only  esc cancel"))
	}

	if m.termWidth > 0 && m.termHeight > 0 {
		return fixedTerminalBlock(b.String(), m.termWidth, m.termHeight)
	}
	return b.String()
}

// stepCursor moves cursor by delta over a list of length n with wrap-around
// (past the last item -> first, before the first -> last) and returns the
// cursor together with a scroll offset that keeps it within a window of the
// given height. It is a no-op returning (0, 0) for an empty list.
func stepCursor(cursor, offset, length, height, delta int) (int, int) {
	if length == 0 {
		return 0, 0
	}
	if height <= 0 {
		height = 1
	}
	cursor += delta
	if cursor < 0 {
		cursor = length - 1
	} else if cursor >= length {
		cursor = 0
	}
	// Bidirectional viewport clamp — both branches are required because wrap
	// can move the cursor either direction in a single step. Mirrors the full
	// clamp used in applySearch, not the one-directional inline snippets in the
	// old up/down cases.
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+height {
		offset = cursor - height + 1
	}
	if offset < 0 {
		offset = 0
	}
	return cursor, offset
}

func fixedTerminalBlock(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	} else {
		for len(lines) < height {
			lines = append(lines, "")
		}
	}

	for i, line := range lines {
		lineWidth := lipgloss.Width(line)
		if lineWidth > width {
			line = lipgloss.NewStyle().MaxWidth(width).Render(line)
			line = strings.Split(line, "\n")[0]
			lineWidth = lipgloss.Width(line)
		}
		if lineWidth < width {
			lines[i] = line + strings.Repeat(" ", width-lineWidth)
		} else {
			lines[i] = line
		}
	}

	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// runDirPicker launches the interactive directory browser starting at startDir.
// preSelected contains absolute paths that should be pre-checked.
// PreSelected value indicates read-only mode.
// exclude is hidden from the listing (the primary workspace, already mounted).
// Returns selected paths with read-only mode, or nil if cancelled.
func runDirPicker(startDir string, preSelected map[string]bool, exclude string) ([]dirMountSelection, error) {
	model := newDirPickerModel(startDir, preSelected, exclude)
	p := tea.NewProgram(model, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return nil, err
	}

	m := result.(dirPickerModel)
	if m.cancelled {
		return nil, errPickerCancelled
	}

	var paths []string
	for path := range m.selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	out := make([]dirMountSelection, 0, len(paths))
	for _, path := range paths {
		out = append(out, m.selected[path])
	}
	return out, nil
}
