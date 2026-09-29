package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// Long details and change lists need a viewport, not a Note that truncates at
// terminal height. Confirmation uses an explicit key so scrolling is harmless.
type wizardViewer struct {
	title, content, acceptLabel string
	viewport                    viewport.Model
	confirm, accepted           bool
}

func (m wizardViewer) Init() tea.Cmd { return nil }
func (m wizardViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = max(10, msg.Width-2)
		m.viewport.Height = max(1, msg.Height-5)
		m.wrap()
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if !m.confirm {
				return m, tea.Quit
			}
		case "s":
			if m.confirm {
				m.accepted = true
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
func (m *wizardViewer) wrap() {
	var lines []string
	for _, line := range strings.Split(m.content, "\n") {
		lines = append(lines, wrapWizardDetailLine("", line, m.viewport.Width)...)
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
}
func (m wizardViewer) View() string {
	help := "↑/↓ scroll · PgUp/PgDn · Enter/Esc back"
	if m.confirm {
		help = "↑/↓ scroll · PgUp/PgDn · s " + m.acceptLabel + " · Esc back"
	}
	return wizardBold.Render(m.title) + "\n\n" + m.viewport.View() + "\n" + fmt.Sprintf("%.0f%%  %s", m.viewport.ScrollPercent()*100, help) + "\n"
}
func viewWizardText(title, content, acceptLabel string) (bool, error) {
	m := wizardViewer{title: title, content: content, acceptLabel: acceptLabel, confirm: acceptLabel != "", viewport: viewport.New(78, 18)}
	m.wrap()
	result, err := tea.NewProgram(m, tea.WithInput(os.Stdin), tea.WithOutput(os.Stderr)).Run()
	if err != nil {
		return false, err
	}
	final, ok := result.(wizardViewer)
	if !ok {
		return false, huh.ErrUserAborted
	}
	return final.accepted, nil
}
