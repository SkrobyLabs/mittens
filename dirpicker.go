package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
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

// ---------------------------------------------------------------------------
// Bubbletea model
// ---------------------------------------------------------------------------

type dirPickerModel struct {
	currentDir string
	entries    []dirEntry
	cursor     int
	selected   map[string]bool
	done       bool
	cancelled  bool
	height     int // visible entry rows (terminal height - 4)
	termHeight int // full terminal height
	offset     int // scroll offset
	err        error
}

func newDirPickerModel(startDir string, preSelected map[string]bool) dirPickerModel {
	sel := make(map[string]bool, len(preSelected))
	for k, v := range preSelected {
		sel[k] = v
	}
	m := dirPickerModel{
		currentDir: startDir,
		selected:   sel,
		height:     15,
	}
	m.loadDir(startDir)
	return m
}

func (m *dirPickerModel) loadDir(path string) {
	m.currentDir = path
	m.cursor = 0
	m.offset = 0
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
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		isGit := false
		if info, err := os.Stat(filepath.Join(full, ".git")); err == nil && info.IsDir() {
			isGit = true
		}
		m.entries = append(m.entries, dirEntry{name: name, path: full, isGit: isGit})
	}
	sort.Slice(m.entries, func(i, j int) bool {
		return m.entries[i].name < m.entries[j].name
	})
}

func (m dirPickerModel) Init() tea.Cmd {
	return nil
}

func (m dirPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
		// Reserve 4 lines for header + footer
		m.height = msg.Height - 4
		if m.height < 3 {
			m.height = 3
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			m.cancelled = true
			return m, tea.Quit

		case "d", "enter":
			// "enter" on an entry navigates into it, but "d" is always done.
			if msg.String() == "d" {
				m.done = true
				return m, tea.Quit
			}
			// enter navigates into the directory
			if len(m.entries) > 0 {
				target := m.entries[m.cursor].path
				m.loadDir(target)
				return m, tea.ClearScreen
			}

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}

		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
				if m.cursor >= m.offset+m.height {
					m.offset = m.cursor - m.height + 1
				}
			}

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
				if m.selected[p] {
					delete(m.selected, p)
				} else {
					m.selected[p] = true
				}
			}
		}
	}
	return m, nil
}

func (m dirPickerModel) View() string {
	var b strings.Builder

	// Header: current path
	b.WriteString(dpStylePath.Render("Browse: " + m.currentDir))
	b.WriteString("\n\n")

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
			if m.selected[e.path] {
				check = dpStyleSelected.Render("[x]")
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
		b.WriteString(dpStyleSelected.Render(fmt.Sprintf("\n  %d selected", len(m.selected))))
		b.WriteString("\n")
	}

	// Footer
	b.WriteString("\n")
	b.WriteString(dpStyleHelp.Render("  ←/→ navigate  space select  d done  esc cancel"))

	// Pad to full terminal height to prevent stale line artifacts.
	rendered := b.String()
	lines := strings.Count(rendered, "\n")
	for i := lines; i < m.termHeight; i++ {
		b.WriteString("\n")
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// runDirPicker launches the interactive directory browser starting at startDir.
// preSelected contains absolute paths that should be pre-checked.
// Returns the list of selected paths, or nil if cancelled.
func runDirPicker(startDir string, preSelected map[string]bool) ([]string, error) {
	model := newDirPickerModel(startDir, preSelected)
	p := tea.NewProgram(model, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return nil, err
	}

	m := result.(dirPickerModel)
	if m.cancelled {
		return nil, huh.ErrUserAborted
	}

	var paths []string
	for p := range m.selected {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}
