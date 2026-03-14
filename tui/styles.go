package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12")) // bright blue

	agentNameStyle = lipgloss.NewStyle().
			Width(20)

	statusRunning = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")) // yellow

	statusDone = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")) // green

	statusError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")) // red

	statusPending = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")) // gray

	stageStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")).
			MarginTop(1)

	verboseAgentPrefix = lipgloss.NewStyle().
				Foreground(lipgloss.Color("14")). // cyan
				Bold(true)

	verboseDelegateMsg = lipgloss.NewStyle().
				Foreground(lipgloss.Color("11")). // yellow
				Bold(true)

	reportTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15")). // white
				Background(lipgloss.Color("4")).  // blue bg
				Padding(0, 1)

	reportDividerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")) // gray
)

const (
	iconPending = "○"
	iconDone    = "✓"
	iconError   = "✗"
)
