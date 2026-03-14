// Package main demonstrates both interactive and non-interactive TUI modes
// with a coordinator agent delegating to a researcher sub-agent.
//
// Usage:
//
//	go run ./examples/tui/              # non-interactive (verbose) mode
//	go run ./examples/tui/ -interactive # interactive bubbletea mode (requires TTY)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	tea "github.com/charmbracelet/bubbletea"
	agentsdk "github.com/urmzd/agent-sdk"
	"github.com/urmzd/agent-sdk/core"
	"github.com/urmzd/agent-sdk/provider/ollama"
	"github.com/urmzd/agent-sdk/tui"
)

func main() {
	interactive := flag.Bool("interactive", false, "use interactive bubbletea TUI (requires TTY)")
	flag.Parse()

	client := ollama.NewClient("http://localhost:11434", "qwen3.5:4b", "")
	adapter := ollama.NewAdapter(client)

	searchTool := &core.ToolFunc{
		Def: core.ToolDef{
			Name:        "search",
			Description: "Search the web for information on a topic",
			Parameters: core.ParameterSchema{
				Type:     "object",
				Required: []string{"query"},
				Properties: map[string]core.PropertyDef{
					"query": {Type: "string", Description: "Search query"},
				},
			},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			return fmt.Sprintf("Results for %q: Go 1.24 adds generic type aliases, "+
				"improved range-over-func iterators, and Swiss Tables map implementation.", query), nil
		},
	}

	agent := agentsdk.NewAgent(agentsdk.AgentConfig{
		Name:         "coordinator",
		SystemPrompt: "You coordinate research tasks. Delegate research to the researcher.",
		Provider:     adapter,
		SubAgents: []agentsdk.SubAgentDef{
			{
				Name:         "researcher",
				Description:  "A research specialist that can search for information",
				SystemPrompt: "You are a research assistant. Use the search tool to find information.",
				Provider:     adapter,
				Tools:        core.NewToolRegistry(searchTool),
			},
		},
	})

	stream := agent.Invoke(context.Background(), []core.Message{
		core.NewUserMessage("Research the latest Go features"),
	})

	if *interactive {
		runInteractive(stream)
	} else {
		runVerbose(stream)
	}
}

func runInteractive(stream *agentsdk.EventStream) {
	model := tui.NewStreamModel("Research Agent", stream.Deltas())
	p := tea.NewProgram(model)

	finalModel, err := p.Run()
	if err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	m := finalModel.(tui.StreamModel)
	if m.Err() != nil {
		log.Fatalf("Stream error: %v", m.Err())
	}

	fmt.Println("\n--- Final Report ---")
	fmt.Println(m.FinalReport())
}

func runVerbose(stream *agentsdk.EventStream) {
	result := tui.StreamVerbose("Research Agent", stream.Deltas(), nil)
	if result.Err != nil {
		log.Fatalf("Stream error: %v", result.Err)
	}
}
