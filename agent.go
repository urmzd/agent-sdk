package agentsdk

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AgentConfig holds configuration for an Agent.
type AgentConfig struct {
	Name         string
	SystemPrompt string
	Provider     Provider
	Tools        *ToolRegistry
	Compactor    Compactor
	MaxIter      int
	SubAgents    []SubAgentDef
}

// Agent runs an LLM agent loop with tool execution.
type Agent struct {
	cfg   AgentConfig
	tools *ToolRegistry
}

// NewAgent creates a new Agent.
func NewAgent(cfg AgentConfig) *Agent {
	if cfg.MaxIter <= 0 {
		cfg.MaxIter = 10
	}
	tools := cfg.Tools
	if tools == nil {
		tools = NewToolRegistry()
	}

	// Register sub-agents as delegate tools
	if len(cfg.SubAgents) > 0 {
		for _, sa := range cfg.SubAgents {
			sa := sa
			delegateTool := &ToolFunc{
				Def: ToolDef{
					Name:        "delegate_to_" + sa.Name,
					Description: sa.Description,
					Parameters: ParameterSchema{
						Type:     "object",
						Required: []string{"task"},
						Properties: map[string]PropertyDef{
							"task": {Type: "string", Description: "The task to delegate"},
						},
					},
				},
				Fn: func(ctx context.Context, args map[string]any) (string, error) {
					task, _ := args["task"].(string)
					child := NewAgent(AgentConfig{
						Name:         sa.Name,
						SystemPrompt: sa.SystemPrompt,
						Provider:     sa.Provider,
						Tools:        sa.Tools,
						MaxIter:      sa.MaxIter,
					})
					stream := child.Invoke(ctx, []Message{NewUserMessage(task)})
					// Collect the final text
					var result string
					for d := range stream.Deltas() {
						if tc, ok := d.(TextContentDelta); ok {
							result += tc.Content
						}
					}
					return result, stream.err
				},
			}
			tools.tools[delegateTool.Def.Name] = delegateTool
		}
	}

	return &Agent{cfg: cfg, tools: tools}
}

// Invoke starts the agent loop and returns a stream of deltas.
func (a *Agent) Invoke(ctx context.Context, inputMessages []Message) *EventStream {
	ctx, cancel := context.WithCancel(ctx)
	stream := newEventStream(cancel)

	go a.runLoop(ctx, stream, inputMessages)

	return stream
}

func (a *Agent) runLoop(ctx context.Context, stream *EventStream, inputMessages []Message) {
	defer func() {
		stream.send(DoneDelta{})
		stream.close(nil)
	}()

	// Build messages with system prompt
	messages := make([]Message, 0, len(inputMessages)+1)
	messages = append(messages, NewSystemMessage(a.cfg.SystemPrompt))
	messages = append(messages, inputMessages...)

	toolDefs := a.tools.Definitions()

	for range a.cfg.MaxIter {
		select {
		case <-ctx.Done():
			stream.send(ErrorDelta{Error: ErrStreamCancelled})
			return
		default:
		}

		// Compact if configured
		if a.cfg.Compactor != nil {
			compacted, err := a.cfg.Compactor.Compact(ctx, messages, a.cfg.Provider)
			if err == nil {
				messages = compacted
			}
		}

		// Call LLM
		rx, err := a.cfg.Provider.ChatStream(ctx, messages, toolDefs)
		if err != nil {
			stream.send(ErrorDelta{Error: fmt.Errorf("%w: %v", ErrProviderFailed, err)})
			return
		}

		// Accumulate response
		agg := NewDefaultAggregator()
		for delta := range rx {
			stream.send(delta)
			agg.Push(delta)
		}

		// Build assistant message
		msg := agg.Message()
		if msg == nil {
			break
		}
		messages = append(messages, msg)

		// Check for tool calls
		assistantMsg, ok := msg.(AssistantMessage)
		if !ok {
			break
		}

		var hasTools bool
		for _, block := range assistantMsg.Content {
			tc, isTool := block.(ToolUseContent)
			if !isTool {
				continue
			}
			hasTools = true

			// Emit tool call start
			stream.send(ToolCallStartDelta{ID: tc.ID, Name: tc.Name})

			// Execute tool
			result, execErr := a.tools.Execute(ctx, tc.Name, tc.Arguments)
			if execErr != nil {
				result = "Error: " + execErr.Error()
			}

			// Emit tool call result as a ToolCallEndDelta with result info
			stream.send(ToolCallEndDelta{Arguments: map[string]any{
				"id":     tc.ID,
				"name":   tc.Name,
				"result": result,
			}})

			// Append tool result message
			messages = append(messages, NewToolResultMessage(tc.ID, result))
		}

		if !hasTools {
			break
		}
	}
}

func generateID() string {
	return uuid.New().String()
}

// NewID generates a new unique ID. Exported for use by adapters.
func NewID() string {
	return uuid.New().String()
}
