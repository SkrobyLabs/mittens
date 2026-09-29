package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"
)

// The overview keeps its scroll position while the cursor moves through the
// visible rows. A three-row margin provides context near either edge.
type wizardMenu struct {
	title, description            string
	options                       []huh.Option[string]
	cursor, offset, width, height int
	accepted                      bool
}

func newWizardMenu(title, description string, options []huh.Option[string], selected string) wizardMenu {
	m := wizardMenu{title: title, description: description, options: options, width: 79, height: 24}
	for i, option := range options {
		if option.Value == selected {
			m.cursor = i
			break
		}
	}
	m.keepCursorVisible()
	return m
}

func (m wizardMenu) Init() tea.Cmd { return nil }

func (m wizardMenu) headerLines() []string {
	lines := []string{ansi.Truncate(m.title, m.width, "…")}
	var description []string
	for _, line := range strings.Split(m.description, "\n") {
		description = append(description, wrapWizardDetailLine("", line, m.width)...)
	}
	limit := max(1, m.height/4)
	if len(description) > limit {
		description = description[:limit]
		description[limit-1] = ansi.Truncate(description[limit-1], max(0, m.width-1), "") + "…"
	}
	lines = append(lines, description...)
	return append(lines, "")
}

func (m wizardMenu) visibleRows() int {
	// Reserve a blank row, a range indicator, and keyboard help below the list.
	return max(1, m.height-len(m.headerLines())-3)
}

func (m *wizardMenu) keepCursorVisible() {
	rows := m.visibleRows()
	if len(m.options) <= rows {
		m.offset = 0
		return
	}
	margin := min(3, (rows-1)/2)
	if m.cursor < m.offset+margin {
		m.offset = m.cursor - margin
	}
	if m.cursor >= m.offset+rows-margin {
		m.offset = m.cursor - rows + margin + 1
	}
	m.offset = max(0, min(m.offset, len(m.options)-rows))
}

func (m wizardMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width-1), max(1, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			if len(m.options) > 0 {
				m.accepted = true
				return m, tea.Quit
			}
		case "up", "k", "shift+tab":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = max(0, len(m.options)-1)
			}
		case "down", "j", "tab":
			m.cursor++
			if m.cursor >= len(m.options) {
				m.cursor = 0
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(0, len(m.options)-1)
		case "pgup":
			m.cursor = max(0, m.cursor-m.visibleRows())
		case "pgdown":
			m.cursor = min(max(0, len(m.options)-1), m.cursor+m.visibleRows())
		default:
			return m, nil
		}
	default:
		return m, nil
	}
	m.keepCursorVisible()
	return m, nil
}

func (m wizardMenu) View() string {
	var b strings.Builder
	for i, line := range m.headerLines() {
		if i == 0 {
			line = wizardTitle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	end := min(len(m.options), m.offset+m.visibleRows())
	for i := m.offset; i < end; i++ {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		line := ansi.Truncate(prefix+m.options[i].Key, m.width, "…")
		if i == m.cursor {
			line = dpStyleSelected.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteByte('\n')
	if len(m.options) > m.visibleRows() {
		b.WriteString(fmt.Sprintf("%d–%d of %d", m.offset+1, end, len(m.options)))
	}
	b.WriteByte('\n')
	b.WriteString(dpStyleHelp.Render(ansi.Truncate("↑/↓ move · Enter select · Esc discard · PgUp/PgDn", m.width, "…")))
	return b.String()
}

func runWizardMenu(title, description string, options []huh.Option[string], selected string) (string, error) {
	m := newWizardMenu(title, description, options, selected)
	result, err := tea.NewProgram(m, tea.WithInput(os.Stdin), tea.WithOutput(os.Stderr)).Run()
	if err != nil {
		return "", err
	}
	final, ok := result.(wizardMenu)
	if !ok || !final.accepted {
		return "", huh.ErrUserAborted
	}
	return final.options[final.cursor].Value, nil
}
