# agent-sdk

A strongly-typed Go SDK for building streaming LLM agent loops.

## Why?

Most LLM SDKs hand you flat, untyped structs and leave you to build the agent loop yourself:

- **Untyped deltas** — you switch on string fields to figure out what the LLM is streaming.
- **Flat messages** — system, user, and assistant messages share the same struct with a role string.
- **Coupled agent loops** — tool execution, context compaction, and sub-agents are your problem.

**agent-sdk** solves this:

- **Typed deltas** — sealed `Delta` interface with concrete types for text chunks, tool calls, tool execution, usage metadata, and errors.
- **Sealed messages** — `Message` interface with distinct types per role and typed content blocks. Only three roles: system, user, assistant.
- **Parallel tool execution** — multiple tool calls in a single response execute concurrently with streaming attribution.
- **Sub-agents as agents** — a sub-agent is just an agent called by an agent, with full delta streaming through the parent.
- **Pluggable providers** — implement one method (`ChatStream`) to integrate any LLM.
- **Provider resilience** — built-in retry with backoff and multi-provider fallback.
- **Runtime configuration** — change model, compaction, and iteration limits mid-conversation via `ConfigContent` in the tree.
- **Usage tracking** — token counts and latency emitted as `UsageDelta` after every LLM call.
- **Session replay** — restore conversations from the tree as a delta stream.

## Features

- Typed streaming deltas separated by concern (LLM-side vs execution-side vs metadata)
- Sealed message types per role (system, user, assistant — no fake "tool" role)
- Tool results as content blocks in system or user messages
- Parallel tool execution with concurrent goroutines
- Sub-agent delegation with nested delta forwarding
- Branching conversation tree with checkpoints, rewind, and compaction
- Session replay from persisted history
- Data-driven compaction config (`CompactConfig` with strategy, window size, threshold)
- Runtime config via `ConfigContent` (model, max iterations, compaction, immediate compaction trigger)
- Token usage and latency tracking via `UsageDelta`
- Provider fallback (`FallbackProvider`) — tries providers in order
- Provider retry (`RetryProvider`) — exponential backoff on transient errors
- Structured errors (`ProviderError`, `FallbackError`, `RetryError`) with `errors.Is`/`errors.As` support
- Error classification (transient vs permanent) for retry/fallback decisions
- Ollama adapter (implements Provider with `NamedProvider` identification)

## Installation

```bash
go get github.com/urmzd/agent-sdk
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    agentsdk "github.com/urmzd/agent-sdk"
    "github.com/urmzd/agent-sdk/ollama"
)

func main() {
    client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
    adapter := ollama.NewAdapter(client)

    agent := agentsdk.NewAgent(agentsdk.AgentConfig{
        Name:         "assistant",
        SystemPrompt: "You are a helpful assistant.",
        Provider:     adapter,
    })

    stream := agent.Invoke(context.Background(), []agentsdk.Message{
        agentsdk.NewUserMessage("What is the capital of France?"),
    })

    for delta := range stream.Deltas() {
        switch d := delta.(type) {
        case agentsdk.TextContentDelta:
            fmt.Print(d.Content)
        case agentsdk.UsageDelta:
            fmt.Printf("\n[tokens: %d prompt, %d completion, latency: %s]\n",
                d.PromptTokens, d.CompletionTokens, d.Latency)
        case agentsdk.ToolExecStartDelta:
            fmt.Printf("\n[tool %s: %s]\n", d.ToolCallID, d.Name)
        case agentsdk.ToolExecDelta:
            if inner, ok := d.Inner.(agentsdk.TextContentDelta); ok {
                fmt.Print(inner.Content)
            }
        case agentsdk.ToolExecEndDelta:
            fmt.Printf("\n[tool %s done]\n", d.ToolCallID)
        }
    }
}
```

## Core Types

### Messages

There are three roles. Tool results are not a separate role — they are content blocks within system or user messages.

| Type | Role | Content Types |
|------|------|---------------|
| `SystemMessage` | `system` | `TextContent`, `ToolResultContent`, `ConfigContent` |
| `UserMessage` | `user` | `TextContent`, `ToolResultContent`, `ConfigContent` |
| `AssistantMessage` | `assistant` | `TextContent`, `ToolUseContent` |

**Why no `ToolResultMessage`?** A tool result is the environment reporting back what happened. When the SDK auto-executes a tool, the result is a `SystemMessage` (no human was involved). When a human provides the result on interrupt, it's a `UserMessage`. The adapter maps both to the wire format the LLM expects.

### Deltas

Deltas are split into three categories: **LLM-side** (what the model generates), **execution-side** (what happens when tools run), and **metadata** (usage and timing).

| Type | Category | Purpose |
|------|----------|---------|
| `TextStartDelta` | LLM | Text block opened |
| `TextContentDelta` | LLM | Text chunk received |
| `TextEndDelta` | LLM | Text block closed |
| `ToolCallStartDelta` | LLM | Model generating a tool call (ID + name) |
| `ToolCallArgumentDelta` | LLM | Argument JSON chunk |
| `ToolCallEndDelta` | LLM | Tool call generation complete (parsed arguments) |
| `ToolExecStartDelta` | Execution | Tool has begun executing (ToolCallID + name) |
| `ToolExecDelta` | Execution | Inner delta from a streaming tool or sub-agent (ToolCallID + inner delta) |
| `ToolExecEndDelta` | Execution | Tool finished (ToolCallID + result + error) |
| `UsageDelta` | Metadata | Token counts (prompt, completion, total) and wall-clock latency |
| `ErrorDelta` | Terminal | Provider or tool error |
| `DoneDelta` | Terminal | Stream complete |

Every execution delta carries a `ToolCallID` so consumers can demux parallel tool executions.

### Content Blocks

| Type | Allowed In | Purpose |
|------|-----------|---------|
| `TextContent` | System, User, Assistant | Plain text |
| `ToolUseContent` | Assistant | Tool invocation (ID, name, arguments) |
| `ToolResultContent` | System, User | Tool execution result (ToolCallID, text) |
| `ConfigContent` | System, User | Runtime configuration (model, max iterations, compaction) |

## Provider Interface

```go
type Provider interface {
    ChatStream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan Delta, error)
}
```

Implement this single method to integrate any LLM backend. Each provider uses its own configured default model. Model selection is handled via `ConfigContent` in the message tree, not as a parameter. The SDK ships an Ollama adapter.

Providers can optionally implement `NamedProvider` for identification in logs and error messages:

```go
type NamedProvider interface {
    Provider
    Name() string
}
```

## Provider Resilience

### Retry

Wrap any provider with exponential backoff retry for transient errors (429, 5xx, timeouts):

```go
retryCfg := agentsdk.RetryConfig{
    MaxAttempts: 3,
    BaseDelay:   500 * time.Millisecond,
    MaxDelay:    10 * time.Second,
    Multiplier:  2.0,
}
provider := agentsdk.NewRetryProvider(adapter, retryCfg)
```

By default, only transient errors are retried. Override `ShouldRetry` to customize.

### Fallback

Try multiple providers in order — if one fails, fall back to the next:

```go
primary := ollama.NewAdapter(ollama.NewClient("http://primary:11434", "llama3", ""))
backup  := ollama.NewAdapter(ollama.NewClient("http://backup:11434", "llama3", ""))

provider := agentsdk.NewFallbackProvider(primary, backup)
```

By default, falls back on any error. Set `FallbackOn` to control which errors trigger fallback (e.g., `IsTransient` for transient-only).

### Composition

Retry and fallback compose naturally:

```go
retryCfg := agentsdk.DefaultRetryConfig()

provider := agentsdk.NewFallbackProvider(
    agentsdk.NewRetryProvider(primary, retryCfg),
    agentsdk.NewRetryProvider(backup, retryCfg),
)

agent := agentsdk.NewAgent(agentsdk.AgentConfig{
    Provider: provider,
    // ...
})
```

Each provider retries independently before falling back to the next.

## Structured Errors

Errors follow Go conventions — use `errors.Is` and `errors.As` to inspect them.

| Type | When | Key Fields |
|------|------|------------|
| `ProviderError` | Single provider call fails | `Provider`, `Model`, `Kind`, `Code`, `Err` |
| `FallbackError` | All providers in a fallback chain fail | `Errors []error` |
| `RetryError` | All retry attempts exhausted | `Attempts`, `Last` |

All provider errors satisfy `errors.Is(err, ErrProviderFailed)`.

### Error Classification

Errors are classified as **transient** (retry-worthy) or **permanent**:

```go
if agentsdk.IsTransient(err) {
    // safe to retry: 429, 5xx, timeout, connection refused
}

kind := agentsdk.ClassifyHTTPStatus(statusCode) // ErrorKindTransient or ErrorKindPermanent
```

| Transient (retry) | Permanent (don't retry) |
|--------------------|------------------------|
| 408 Request Timeout | 400 Bad Request |
| 429 Too Many Requests | 401 Unauthorized |
| 500-599 Server Errors | 403 Forbidden |
| Connection refused | 404 Not Found |
| Timeout | Other 4xx |

## Runtime Configuration

Agent behavior can be changed mid-conversation by adding `ConfigContent` to the tree. The agent reads config from the tree each iteration — last write wins per field. Zero values mean "no change".

```go
// Change model mid-conversation
agent.Invoke(ctx, []agentsdk.Message{
    agentsdk.UserMessage{Content: []agentsdk.UserContent{
        agentsdk.ConfigContent{Model: "gpt-4"},
        agentsdk.TextContent{Text: "Use the better model for this question."},
    }},
})

// Trigger immediate compaction
agent.Invoke(ctx, []agentsdk.Message{
    agentsdk.SystemMessage{Content: []agentsdk.SystemContent{
        agentsdk.ConfigContent{
            CompactNow: true,
            Compact: &agentsdk.CompactConfig{
                Strategy:   agentsdk.CompactSlidingWindow,
                WindowSize: 10,
            },
        },
    }},
})
```

`ConfigContent` blocks are automatically stripped before messages are sent to the LLM.

| Field | Type | Effect |
|-------|------|--------|
| `Model` | `string` | Model name passed to Provider (empty = use default) |
| `MaxIter` | `int` | Max loop iterations (0 = no change) |
| `Compact` | `*CompactConfig` | Compaction strategy (nil = no change) |
| `CompactNow` | `bool` | Trigger immediate compaction this iteration |

### CompactConfig

Data-driven compaction configuration that replaces the old `Compactor` interface field:

```go
agentsdk.AgentConfig{
    CompactCfg: &agentsdk.CompactConfig{
        Strategy:   agentsdk.CompactSlidingWindow,
        WindowSize: 20,
    },
}
```

| Strategy | Behavior |
|----------|----------|
| `CompactNone` | No compaction |
| `CompactSlidingWindow` | Keep system prompt + last N messages |
| `CompactSummarize` | Summarize older messages via the provider when threshold exceeded |

## Usage Tracking

Every LLM call emits a `UsageDelta` with token counts and wall-clock latency:

```go
for delta := range stream.Deltas() {
    if u, ok := delta.(agentsdk.UsageDelta); ok {
        fmt.Printf("Tokens: %d prompt, %d completion (%s)\n",
            u.PromptTokens, u.CompletionTokens, u.Latency)
    }
}
```

If the provider reports token usage (e.g., Ollama's `prompt_eval_count`/`eval_count`), those counts are included. Latency is always measured by the agent loop regardless of provider support.

## Tool System

Define tools with JSON schema parameters:

```go
tool := &agentsdk.ToolFunc{
    Def: agentsdk.ToolDef{
        Name:        "greet",
        Description: "Greet a person",
        Parameters: agentsdk.ParameterSchema{
            Type:     "object",
            Required: []string{"name"},
            Properties: map[string]agentsdk.PropertyDef{
                "name": {Type: "string", Description: "Person's name"},
            },
        },
    },
    Fn: func(ctx context.Context, args map[string]any) (string, error) {
        return fmt.Sprintf("Hello, %s!", args["name"]), nil
    },
}

registry := agentsdk.NewToolRegistry(tool)
```

When the LLM requests multiple tool calls in a single response, all tools execute concurrently. Results are collected into a single `SystemMessage` with one `ToolResultContent` block per tool call.

## Agent Loop

`Agent.Invoke()` runs an iterative loop:

1. Flatten the conversation tree into `[]Message`
2. Resolve config from the tree (`ConfigContent` blocks, last write wins)
3. Check iteration cap
4. Strip `ConfigContent` from messages
5. Compact if configured or `CompactNow` is set
6. Send messages + tool definitions to the provider via `ChatStream`
7. Aggregate streaming deltas into a complete `AssistantMessage`, forward to caller
8. Capture `UsageDelta` from the provider, enrich with wall-clock latency, forward to caller
9. Persist the assistant message to the tree
10. If the message contains `ToolUseContent`, execute all tool calls **in parallel**
11. Collect results into a single `SystemMessage` with `ToolResultContent` blocks and persist
12. Repeat until the assistant responds with text only (no tool calls) or max iterations reached

All deltas are forwarded to the caller's `EventStream` in real-time.

## Sub-Agents

A sub-agent is just an agent called by an agent. Sub-agents are registered as tools (`delegate_to_<name>`) and execute within the parallel tool dispatch:

```go
agent := agentsdk.NewAgent(agentsdk.AgentConfig{
    // ...
    SubAgents: []agentsdk.SubAgentDef{
        {
            Name:         "researcher",
            Description:  "Searches the web for information",
            SystemPrompt: "You are a research assistant.",
            Provider:     adapter,
            Tools:        agentsdk.NewToolRegistry(searchTool),
            MaxIter:      5,
        },
    },
})
```

When a sub-agent executes:
- Its deltas are forwarded through the parent's stream, wrapped in `ToolExecDelta{ToolCallID, Inner}` for attribution.
- Multiple sub-agents invoked in the same response run concurrently.
- Sub-agents can have their own sub-agents (arbitrary nesting).
- Each child agent gets a fresh conversation tree — context isolation is total.

The `SubAgentInvoker` interface enables the agent loop to detect sub-agent tools and stream their deltas instead of just collecting a string result.

## Session Replay

Restore a conversation from the tree as a delta stream:

```go
messages, _ := tree.FlattenBranch("main")
stream := agentsdk.Replay(messages)

for delta := range stream.Deltas() {
    // Same delta types as a live conversation.
    // ConfigContent blocks are automatically skipped.
}
```

This enables session restoration — clients receive the same delta types as if the conversation happened live.

## Conversation Tree

All messages are persisted to a branching conversation tree. The tree is the single source of truth; the flat `[]Message` slice the LLM sees is derived from it on every iteration.

Key operations:
- `AddChild` — append a message to a branch
- `Branch` — fork from any node
- `UpdateUserMessage` — edit a user message by creating a new branch
- `Checkpoint` / `Rewind` — save and restore branch state
- `Archive` / `Restore` — soft-delete nodes
- `Compact` — token-budget-aware summarization
- `FlattenBranch` — walk root-to-tip, skip archived nodes
- `Diff` — compare two branches

Optional persistence via `WAL` (write-ahead log) and `Store` interfaces.

## Ollama Adapter

```go
client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
adapter := ollama.NewAdapter(client)

// adapter implements agentsdk.Provider and agentsdk.NamedProvider
// Emits UsageDelta with token counts from Ollama's response
// Returns structured ProviderError with transient/permanent classification
// Also exposes: Generate, GenerateWithModel, GenerateStream, Embed, ExtractEntities
```

## Architecture

| File | Purpose |
|------|---------|
| `agent.go` | Agent loop, config resolution, parallel tool dispatch, sub-agent registration |
| `message.go` | Sealed `Message` interface (system, user, assistant) |
| `content.go` | Content blocks (`TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`) |
| `delta.go` | Sealed `Delta` interface (LLM-side + execution-side + metadata + terminal) |
| `stream.go` | `EventStream`, `Replay` |
| `provider.go` | `Provider` and `NamedProvider` interfaces |
| `provider_fallback.go` | `FallbackProvider` — multi-provider failover |
| `provider_retry.go` | `RetryProvider` — exponential backoff retry |
| `errors.go` | Structured errors (`ProviderError`, `FallbackError`, `RetryError`), classification |
| `tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry`, `subAgentTool` |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `compactor.go` | Compaction strategies + `CompactConfig` data-driven config |
| `subagent.go` | `SubAgentDef`, `SubAgentInvoker` |
| `node.go` | `Node`, `TreePath`, `BranchID`, `NodeID` |
| `tree.go` | Branching conversation tree |
| `flatten.go` | `FlattenBranch`, `FlattenAnnotated` |
| `store.go` | `Store` persistence interface |
| `tx.go` | `WAL` write-ahead log interface |
| `ollama/` | Ollama client + adapter |
