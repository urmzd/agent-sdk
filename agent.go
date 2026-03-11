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
	Tree         *Tree // optional; auto-created if nil
	// BranchID removed — use tree.Active() / tree.SetActive()
}

// Agent runs an LLM agent loop with tool execution.
// All conversations are backed by a Tree.
type Agent struct {
	cfg   AgentConfig
	tools *ToolRegistry
}

// NewAgent creates a new Agent. If no Tree is provided, one is created
// automatically from the SystemPrompt.
func NewAgent(cfg AgentConfig) *Agent {
	if cfg.MaxIter <= 0 {
		cfg.MaxIter = 10
	}
	tools := cfg.Tools
	if tools == nil {
		tools = NewToolRegistry()
	}

	if cfg.Tree == nil {
		tree, _ := NewTree(NewSystemMessage(cfg.SystemPrompt))
		cfg.Tree = tree
	}

	// Register sub-agents as delegate tools
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

	return &Agent{cfg: cfg, tools: tools}
}

// Tree returns the agent's conversation tree.
func (a *Agent) Tree() *Tree {
	return a.cfg.Tree
}

// Invoke starts the agent loop on the active branch and returns a stream of deltas.
// Input messages are appended as child nodes and all responses are persisted to the tree.
func (a *Agent) Invoke(ctx context.Context, input []Message, branch ...BranchID) *EventStream {
	b := a.cfg.Tree.Active()
	if len(branch) > 0 {
		b = branch[0]
	}

	ctx, cancel := context.WithCancel(ctx)
	stream := newEventStream(cancel)

	go a.runLoop(ctx, stream, input, b)

	return stream
}

func (a *Agent) runLoop(ctx context.Context, stream *EventStream, input []Message, branch BranchID) {
	defer func() {
		stream.send(DoneDelta{})
		stream.close(nil)
	}()

	tree := a.cfg.Tree

	// Append input messages as child nodes on the branch.
	for _, msg := range input {
		tip, err := tree.Tip(branch)
		if err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}
		if _, err := tree.AddChild(tip.ID, msg); err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}
	}

	toolDefs := a.tools.Definitions()

	for range a.cfg.MaxIter {
		select {
		case <-ctx.Done():
			stream.send(ErrorDelta{Error: ErrStreamCancelled})
			return
		default:
		}

		// Flatten the branch to get current message history.
		messages, err := tree.FlattenBranch(branch)
		if err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}

		// Compact if configured.
		if a.cfg.Compactor != nil {
			compacted, err := a.cfg.Compactor.Compact(ctx, messages, a.cfg.Provider)
			if err == nil {
				messages = compacted
			}
		}

		// Call LLM.
		rx, err := a.cfg.Provider.ChatStream(ctx, messages, toolDefs)
		if err != nil {
			stream.send(ErrorDelta{Error: fmt.Errorf("%w: %v", ErrProviderFailed, err)})
			return
		}

		// Accumulate response.
		agg := NewDefaultAggregator()
		for delta := range rx {
			stream.send(delta)
			agg.Push(delta)
		}

		msg := agg.Message()
		if msg == nil {
			break
		}

		// Persist assistant message to tree.
		tip, err := tree.Tip(branch)
		if err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}
		if _, err := tree.AddChild(tip.ID, msg); err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}

		// Check for tool calls.
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

			stream.send(ToolCallStartDelta{ID: tc.ID, Name: tc.Name})

			result, execErr := a.tools.Execute(ctx, tc.Name, tc.Arguments)
			if execErr != nil {
				result = "Error: " + execErr.Error()
			}

			stream.send(ToolCallEndDelta{Arguments: map[string]any{
				"id":     tc.ID,
				"name":   tc.Name,
				"result": result,
			}})

			// Persist tool result to tree.
			toolMsg := NewToolResultMessage(tc.ID, result)
			tip, err := tree.Tip(branch)
			if err != nil {
				stream.send(ErrorDelta{Error: err})
				return
			}
			if _, err := tree.AddChild(tip.ID, toolMsg); err != nil {
				stream.send(ErrorDelta{Error: err})
				return
			}
		}

		if !hasTools {
			break
		}
	}
}

// NewID generates a new unique ID.
func NewID() string {
	return uuid.New().String()
}
