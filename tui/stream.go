// Package tui provides a bubbletea-based progress UI for streaming agent
// deltas. It tracks sub-agent tool executions with spinners and status
// icons, accumulates the final coordinator text, and provides verbose-mode
// formatting helpers for non-TTY / debug output.
package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
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

// ── Non-interactive streaming ───────────────────────────────────────

// VerboseResult holds the outcome of a StreamVerbose run.
type VerboseResult struct {
	Text string // accumulated coordinator text output
	Err  error  // first error encountered, if any
}

// StreamVerbose consumes deltas from ch and writes styled progress output
// to w. It does not require an interactive terminal. If w is nil, os.Stdout
// is used. All delta types are logged for a complete trace. Returns the
// accumulated coordinator text output and any error.
func StreamVerbose(title string, ch <-chan core.Delta, w io.Writer) VerboseResult {
	if w == nil {
		w = os.Stdout
	}

	fmt.Fprintln(w, titleStyle.Render(title))
	fmt.Fprintln(w)

	agentNames := map[string]string{}    // toolCallID → name
	agentNewLine := map[string]bool{}    // toolCallID → needs prefix on next chunk
	agentStarted := map[string]bool{}    // toolCallID → has received any text
	var text strings.Builder
	coordinatorStreaming := false // tracks whether coordinator text is mid-line

	// ensureNewline prints a newline if the previous output didn't end with one.
	ensureNewline := func() {
		if coordinatorStreaming {
			fmt.Fprintln(w)
			coordinatorStreaming = false
		}
	}

	for delta := range ch {
		switch d := delta.(type) {

		// ── LLM text streaming ──────────────────────────────────────
		case core.TextStartDelta:
			ensureNewline()

		case core.TextContentDelta:
			text.WriteString(d.Content)
			fmt.Fprint(w, d.Content)
			coordinatorStreaming = true

		case core.TextEndDelta:
			ensureNewline()

		// ── LLM tool call streaming ─────────────────────────────────
		case core.ToolCallStartDelta:
			ensureNewline()
			fmt.Fprintln(w, stageStyle.Render(
				fmt.Sprintf("[tool-call] %s (id=%s)", d.Name, d.ID)))

		case core.ToolCallArgumentDelta:
			// argument JSON fragments — skip in verbose mode

		case core.ToolCallEndDelta:
			// tool call fully parsed — logged at exec start

		// ── Tool execution ──────────────────────────────────────────
		case core.ToolExecStartDelta:
			ensureNewline()
			agentNames[d.ToolCallID] = d.Name
			agentNewLine[d.ToolCallID] = true // first chunk gets the prefix
			agentStarted[d.ToolCallID] = false
			fmt.Fprintln(w, FormatDelegateStart(d.Name))

		case core.ToolExecDelta:
			if inner, ok := d.Inner.(core.TextContentDelta); ok {
				name := agentNames[d.ToolCallID]
				agentStarted[d.ToolCallID] = true
				content := inner.Content

				// Print prefix at start of each new line of sub-agent output.
				if agentNewLine[d.ToolCallID] {
					fmt.Fprint(w, FormatAgentOutput(name, ""))
					agentNewLine[d.ToolCallID] = false
				}
				// If the content contains newlines, add prefix after each one.
				if strings.Contains(content, "\n") {
					prefix := FormatAgentOutput(name, "")
					lines := strings.Split(content, "\n")
					for i, line := range lines {
						if i > 0 {
							fmt.Fprint(w, prefix)
						}
						fmt.Fprint(w, line)
						if i < len(lines)-1 {
							fmt.Fprintln(w)
						}
					}
					// If content ended with \n, next chunk needs a prefix
					if strings.HasSuffix(content, "\n") {
						agentNewLine[d.ToolCallID] = true
					}
				} else {
					fmt.Fprint(w, content)
				}
			}

		case core.ToolExecEndDelta:
			name := agentNames[d.ToolCallID]
			// ensure sub-agent output ends on its own line
			if agentStarted[d.ToolCallID] && !agentNewLine[d.ToolCallID] {
				fmt.Fprintln(w)
			}
			if d.Error != "" {
				fmt.Fprintln(w, FormatAgentError(name, d.Error))
			} else {
				fmt.Fprintln(w, FormatAgentDone(name))
			}

		// ── Markers ─────────────────────────────────────────────────
		case core.MarkerDelta:
			ensureNewline()
			fmt.Fprintln(w, verboseDelegateMsg.Render(
				fmt.Sprintf("[approval required] tool=%s id=%s", d.ToolName, d.ToolCallID)))
			for _, m := range d.Markers {
				fmt.Fprintln(w, verboseDelegateMsg.Render(
					fmt.Sprintf("  marker: %s — %s", m.Kind, m.Message)))
			}

		// ── Metadata ────────────────────────────────────────────────
		case core.UsageDelta:
			ensureNewline()
			fmt.Fprintln(w, stageStyle.Render(
				fmt.Sprintf("[%d prompt + %d completion tokens, %s]",
					d.PromptTokens, d.CompletionTokens, d.Latency)))

		// ── Terminal ────────────────────────────────────────────────
		case core.ErrorDelta:
			ensureNewline()
			fmt.Fprintln(w, statusError.Render(fmt.Sprintf("Error: %v", d.Error)))
			return VerboseResult{Text: text.String(), Err: d.Error}

		case core.DoneDelta:
			ensureNewline()
			return VerboseResult{Text: text.String()}
		}
	}

	return VerboseResult{Text: text.String()}
}

// ── Markdown rendering ──────────────────────────────────────────────

// RenderMarkdown renders markdown text as styled terminal output using
// glamour. It auto-detects the terminal's color profile. If rendering
// fails, the raw text is returned unchanged.
func RenderMarkdown(md string) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return out
}

// RenderReport renders a titled section with the report body formatted
// as markdown. Suitable for displaying the final agent output.
func RenderReport(title, body string) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(reportTitleStyle.Render(title))
	b.WriteString("\n")
	b.WriteString(reportDividerStyle.Render(strings.Repeat("─", 60)))
	b.WriteString("\n")
	b.WriteString(RenderMarkdown(body))
	return b.String()
}
