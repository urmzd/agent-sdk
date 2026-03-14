// Package tui provides a bubbletea-based progress UI for streaming agent
// deltas. It tracks sub-agent tool executions with spinners and status
// icons, accumulates the final coordinator text, and provides verbose-mode
// formatting helpers for non-TTY / debug output.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/urmzd/agent-sdk/core"
)

// ── Pipeline stage ──────────────────────────────────────────────────

type pipelineStage int

const (
	stageInitializing pipelineStage = iota
	stageDelegating
	stageSynthesizing
	stageDone
)

func (s pipelineStage) String() string {
	switch s {
	case stageInitializing:
		return "Initializing"
	case stageDelegating:
		return "Analyzing"
	case stageSynthesizing:
		return "Synthesizing"
	case stageDone:
		return "Done"
	default:
		return "Unknown"
	}
}

// ── Agent tracking ──────────────────────────────────────────────────

type agentStatus int

const (
	agentPending agentStatus = iota
	agentRunning
	agentDone
	agentError
)

type agentState struct {
	name   string
	status agentStatus
	errMsg string
}

// ── Bubbletea messages ──────────────────────────────────────────────

type deltaMsg struct {
	delta core.Delta
}

type streamDoneMsg struct{}

// ── StreamModel ─────────────────────────────────────────────────────

// StreamModel is a bubbletea model that consumes a delta channel from
// an agent-sdk EventStream and displays real-time progress for sub-agent
// tool executions.
type StreamModel struct {
	title       string
	deltaCh     <-chan core.Delta
	stage       pipelineStage
	agents      map[string]*agentState // toolCallID → state
	agentOrder  []string               // ordered toolCallIDs for display
	finalReport strings.Builder
	spinner     spinner.Model
	err         error
}

// NewStreamModel creates a StreamModel that reads deltas from ch and
// displays the given title.
func NewStreamModel(title string, ch <-chan core.Delta) StreamModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	return StreamModel{
		title:   title,
		deltaCh: ch,
		stage:   stageInitializing,
		agents:  make(map[string]*agentState),
		spinner: s,
	}
}

// FinalReport returns the accumulated coordinator output text.
func (m StreamModel) FinalReport() string {
	return m.finalReport.String()
}

// Err returns any error encountered during the stream.
func (m StreamModel) Err() error {
	return m.err
}

// ── tea.Model implementation ────────────────────────────────────────

// Init starts listening for deltas and spinning.
func (m StreamModel) Init() tea.Cmd {
	return tea.Batch(
		listenForDelta(m.deltaCh),
		m.spinner.Tick,
	)
}

// Update processes messages.
func (m StreamModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case streamDoneMsg:
		m.stage = stageDone
		return m, tea.Quit

	case deltaMsg:
		return m.handleDelta(msg.delta)
	}

	return m, nil
}

func (m StreamModel) handleDelta(d core.Delta) (tea.Model, tea.Cmd) {
	switch d := d.(type) {
	case core.ToolExecStartDelta:
		m.stage = stageDelegating
		m.agents[d.ToolCallID] = &agentState{
			name:   d.Name,
			status: agentRunning,
		}
		m.agentOrder = append(m.agentOrder, d.ToolCallID)

	case core.ToolExecDelta:
		// Sub-agent output — accumulate silently in TUI mode.

	case core.ToolExecEndDelta:
		if a, ok := m.agents[d.ToolCallID]; ok {
			if d.Error != "" {
				a.status = agentError
				a.errMsg = d.Error
			} else {
				a.status = agentDone
			}
		}

	case core.TextContentDelta:
		if m.allAgentsDone() && len(m.agents) > 0 {
			m.stage = stageSynthesizing
		}
		m.finalReport.WriteString(d.Content)

	case core.ErrorDelta:
		m.err = d.Error
		return m, tea.Quit

	case core.DoneDelta:
		m.stage = stageDone
		return m, tea.Quit
	}

	return m, listenForDelta(m.deltaCh)
}

func (m StreamModel) allAgentsDone() bool {
	for _, a := range m.agents {
		if a.status == agentRunning || a.status == agentPending {
			return false
		}
	}
	return true
}

// View renders the TUI.
func (m StreamModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(m.title))
	b.WriteString("\n\n")

	if len(m.agents) == 0 && m.stage == stageInitializing {
		fmt.Fprintf(&b, "  %s Initializing...\n", m.spinner.View())
		return b.String()
	}

	doneCount := 0
	total := len(m.agents)

	for _, id := range m.agentOrder {
		a := m.agents[id]
		var icon, label string
		switch a.status {
		case agentPending:
			icon = statusPending.Render(iconPending)
			label = statusPending.Render("Pending")
		case agentRunning:
			icon = m.spinner.View()
			label = statusRunning.Render("Analyzing...")
		case agentDone:
			icon = statusDone.Render(iconDone)
			label = statusDone.Render("Complete")
			doneCount++
		case agentError:
			icon = statusError.Render(iconError)
			label = statusError.Render("Error: " + a.errMsg)
			doneCount++
		}
		fmt.Fprintf(&b, "  %s %s %s\n", icon, agentNameStyle.Render(a.name), label)
	}

	if m.stage == stageSynthesizing {
		fmt.Fprintf(&b, "\n  %s Synthesizing final report...\n", m.spinner.View())
	} else if total > 0 {
		b.WriteString(stageStyle.Render(fmt.Sprintf("  Stage: %s (%d/%d complete)", m.stage, doneCount, total)))
		b.WriteString("\n")
	}

	return b.String()
}

// ── Delta bridge ────────────────────────────────────────────────────

func listenForDelta(ch <-chan core.Delta) tea.Cmd {
	return func() tea.Msg {
		delta, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		return deltaMsg{delta: delta}
	}
}

// ── Verbose-mode formatting helpers ─────────────────────────────────

// FormatDelegateStart formats a delegation start message for verbose mode.
func FormatDelegateStart(name string) string {
	return verboseDelegateMsg.Render(fmt.Sprintf(">> Delegating to %s...", name))
}

// FormatAgentOutput formats sub-agent streaming output for verbose mode.
func FormatAgentOutput(name, content string) string {
	return verboseAgentPrefix.Render(fmt.Sprintf("[%s] ", name)) + content
}

// FormatAgentDone formats a delegation completion message for verbose mode.
func FormatAgentDone(name string) string {
	return verboseDelegateMsg.Render(fmt.Sprintf(">> %s complete.", name))
}

// FormatAgentError formats an agent error message for verbose mode.
func FormatAgentError(name, errMsg string) string {
	return statusError.Render(fmt.Sprintf(">> %s error: %s", name, errMsg))
}
