package agentsdk

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AgentConfig holds configuration for an Agent.
type AgentConfig struct {
	Name         string
	SystemPrompt string
	Provider     Provider
	Tools        *ToolRegistry
	CompactCfg   *CompactConfig // initial compaction config (replaces Compactor)
	MaxIter      int
	SubAgents    []SubAgentDef
	Tree         *Tree // optional; auto-created if nil
}

// Agent runs an LLM agent loop with tool execution.
// All conversations are backed by a Tree.
type Agent struct {
	cfg   AgentConfig
	tools *ToolRegistry
}

// NewAgent creates a new Agent. If no Tree is provided, one is created
// automatically from the SystemPrompt. Initial config is seeded into the
// tree so that serialise/restore round-trips include the full agent config.
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

	// Register sub-agents as delegate tools.
	for _, sa := range cfg.SubAgents {
		registerSubAgent(tools, sa)
	}

	return &Agent{cfg: cfg, tools: tools}
}

func registerSubAgent(registry *ToolRegistry, sa SubAgentDef) {
	registry.Register(&subAgentTool{
		def: ToolDef{
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
		factory: func() *Agent {
			return NewAgent(AgentConfig{
				Name:         sa.Name,
				SystemPrompt: sa.SystemPrompt,
				Provider:     sa.Provider,
				Tools:        sa.Tools,
				SubAgents:    sa.SubAgents,
				MaxIter:      sa.MaxIter,
			})
		},
	})
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

// ── Config resolution ────────────────────────────────────────────────

// resolvedConfig holds the effective configuration for a single iteration,
// derived by walking all ConfigContent blocks in the tree.
type resolvedConfig struct {
	model      string
	maxIter    int
	compactor  Compactor
	compactNow bool
}

// resolveConfig walks messages and merges ConfigContent blocks (last write wins per field).
// Starts from AgentConfig defaults; ConfigContent in the tree overrides them.
func (a *Agent) resolveConfig(messages []Message) resolvedConfig {
	rc := resolvedConfig{maxIter: a.cfg.MaxIter}
	if a.cfg.CompactCfg != nil {
		rc.compactor = a.cfg.CompactCfg.ToCompactor()
	}

	for _, msg := range messages {
		switch v := msg.(type) {
		case SystemMessage:
			for _, c := range v.Content {
				if cc, ok := c.(ConfigContent); ok {
					mergeConfig(&rc, cc)
				}
			}
		case UserMessage:
			for _, c := range v.Content {
				if cc, ok := c.(ConfigContent); ok {
					mergeConfig(&rc, cc)
				}
			}
		}
	}

	return rc
}

func mergeConfig(rc *resolvedConfig, cc ConfigContent) {
	if cc.Model != "" {
		rc.model = cc.Model
	}
	if cc.MaxIter != 0 {
		rc.maxIter = cc.MaxIter
	}
	if cc.Compact != nil {
		rc.compactor = cc.Compact.ToCompactor()
	}
	if cc.CompactNow {
		rc.compactNow = true
	}
}

// stripConfig removes ConfigContent blocks from messages before sending to the LLM.
func stripConfig(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		switch v := msg.(type) {
		case SystemMessage:
			filtered := make([]SystemContent, 0, len(v.Content))
			for _, c := range v.Content {
				if _, ok := c.(ConfigContent); !ok {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) > 0 {
				out = append(out, SystemMessage{Content: filtered})
			}
		case UserMessage:
			filtered := make([]UserContent, 0, len(v.Content))
			for _, c := range v.Content {
				if _, ok := c.(ConfigContent); !ok {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) > 0 {
				out = append(out, UserMessage{Content: filtered})
			}
		default:
			out = append(out, msg)
		}
	}
	return out
}

// ── Run loop ─────────────────────────────────────────────────────────

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

	for iterCount := 0; ; iterCount++ {
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

		// Resolve config from tree each iteration.
		resolved := a.resolveConfig(messages)

		// Check iteration cap.
		if iterCount >= resolved.maxIter {
			break
		}

		// Strip config before sending to LLM or compactor.
		llmMessages := stripConfig(messages)

		// Compact if configured.
		if resolved.compactNow || resolved.compactor != nil {
			if resolved.compactor != nil {
				compacted, err := resolved.compactor.Compact(ctx, llmMessages, a.cfg.Provider)
				if err == nil {
					llmMessages = compacted
				}
			}
		}

		// Call LLM + timing.
		start := time.Now()
		rx, err := a.cfg.Provider.ChatStream(ctx, llmMessages, toolDefs)
		if err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}

		// Accumulate response, capture UsageDelta from provider.
		agg := NewDefaultAggregator()
		var usage *UsageDelta
		for delta := range rx {
			if u, ok := delta.(UsageDelta); ok {
				usage = &u
				continue
			}
			stream.send(delta)
			agg.Push(delta)
		}

		// Emit enriched usage delta.
		latency := time.Since(start)
		enriched := UsageDelta{Latency: latency}
		if usage != nil {
			enriched.PromptTokens = usage.PromptTokens
			enriched.CompletionTokens = usage.CompletionTokens
			enriched.TotalTokens = usage.TotalTokens
		}
		stream.send(enriched)

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

		var toolCalls []ToolUseContent
		for _, block := range assistantMsg.Content {
			if tc, ok := block.(ToolUseContent); ok {
				toolCalls = append(toolCalls, tc)
			}
		}

		if len(toolCalls) == 0 {
			break
		}

		// Execute all tool calls in parallel.
		results := a.executeToolsConcurrently(ctx, stream, toolCalls)

		// Build a single SystemMessage with all tool results and persist.
		toolResultContents := make([]ToolResultContent, len(results))
		for i, r := range results {
			text := r.result
			if r.err != "" && text == "" {
				text = "Error: " + r.err
			}
			toolResultContents[i] = ToolResultContent{
				ToolCallID: r.toolCallID,
				Text:       text,
			}
		}

		toolResultMsg := NewToolResultMessage(toolResultContents...)
		tip, err = tree.Tip(branch)
		if err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}
		if _, err := tree.AddChild(tip.ID, toolResultMsg); err != nil {
			stream.send(ErrorDelta{Error: err})
			return
		}
	}
}

// toolResult collects the outcome of a single tool execution.
type toolResult struct {
	toolCallID string
	result     string
	err        string
}

// executeToolsConcurrently runs all tool calls in parallel, streaming deltas
// as they arrive. Results are returned in the same order as toolCalls.
func (a *Agent) executeToolsConcurrently(ctx context.Context, stream *EventStream, toolCalls []ToolUseContent) []toolResult {
	results := make([]toolResult, len(toolCalls))
	var wg sync.WaitGroup

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc ToolUseContent) {
			defer wg.Done()

			stream.send(ToolExecStartDelta{ToolCallID: tc.ID, Name: tc.Name})

			tool, found := a.tools.Get(tc.Name)
			if !found {
				results[idx] = toolResult{
					toolCallID: tc.ID,
					err:        fmt.Sprintf("tool not found: %s", tc.Name),
				}
				stream.send(ToolExecEndDelta{
					ToolCallID: tc.ID,
					Error:      results[idx].err,
				})
				return
			}

			// Check if this is a sub-agent — if so, forward child deltas.
			if invoker, ok := tool.(SubAgentInvoker); ok {
				task, _ := tc.Arguments["task"].(string)
				childStream := invoker.InvokeAgent(ctx, task)

				var result string
				for d := range childStream.Deltas() {
					// Forward child deltas wrapped with attribution.
					stream.send(ToolExecDelta{
						ToolCallID: tc.ID,
						Inner:      d,
					})
					if tcd, ok := d.(TextContentDelta); ok {
						result += tcd.Content
					}
				}

				errStr := ""
				if err := childStream.Wait(); err != nil {
					errStr = err.Error()
				}
				results[idx] = toolResult{
					toolCallID: tc.ID,
					result:     result,
					err:        errStr,
				}
			} else {
				// Regular tool execution.
				result, execErr := tool.Execute(ctx, tc.Arguments)
				errStr := ""
				if execErr != nil {
					errStr = execErr.Error()
					result = "Error: " + errStr
				}
				results[idx] = toolResult{
					toolCallID: tc.ID,
					result:     result,
					err:        errStr,
				}
			}

			stream.send(ToolExecEndDelta{
				ToolCallID: results[idx].toolCallID,
				Result:     results[idx].result,
				Error:      results[idx].err,
			})
		}(i, tc)
	}

	wg.Wait()
	return results
}

// NewID generates a new unique ID.
func NewID() string {
	return uuid.New().String()
}
