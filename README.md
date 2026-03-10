# agent-sdk

A strongly-typed Go SDK for building streaming LLM agent loops.

## Why?

Most LLM SDKs hand you flat, untyped structs and leave you to build the agent loop yourself:

- **Untyped deltas** — you switch on string fields to figure out what the LLM is streaming.
- **Flat messages** — system, user, assistant, and tool messages share the same struct.
- **Coupled agent loops** — tool execution, context compaction, and sub-agents are your problem.

**agent-sdk** solves this:

- **Typed deltas** — sealed `Delta` interface with concrete types for text chunks, tool calls, errors, and sub-agent events.
- **Sealed messages** — `Message` interface with distinct types per role and typed content blocks.
- **Pluggable providers** — implement one method (`ChatStream`) to integrate any LLM.
- **Batteries included** — tool registry, context compaction, sub-agents, and SSE bridge out of the box.

## Features

- Typed streaming deltas (text start/content/end, tool call start/argument/end, done, error)
- Sealed message types per role (system, user, assistant, tool result)
- Typed content blocks (TextContent, ToolUseContent)
- Tool registry with JSON schema parameters
- ToolFunc adapter for inline tool definitions
- Context compaction (noop, sliding window, summarize)
- Sub-agent delegation with nested delta streaming
- SSE bridge for HTTP server-sent events
- Ollama adapter (implements Provider)

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
        Tools:        agentsdk.NewToolRegistry(),
        Compactor:    agentsdk.NoopCompactor{},
        MaxIter:      10,
    })

    stream := agent.Invoke(context.Background(), []agentsdk.Message{
        agentsdk.NewUserMessage("What is the capital of France?"),
    })

    for delta := range stream.Deltas() {
        if d, ok := delta.(agentsdk.TextContentDelta); ok {
            fmt.Print(d.Content)
        }
    }
}
```

## Core Types

### Messages

| Type | Role | Content Types |
|------|------|---------------|
| `SystemMessage` | `system` | `TextContent` |
| `UserMessage` | `user` | `TextContent` |
| `AssistantMessage` | `assistant` | `TextContent`, `ToolUseContent` |
| `ToolResultMessage` | `tool` | `TextContent` |

### Deltas

| Type | Purpose |
|------|---------|
| `TextStartDelta` | Text block opened |
| `TextContentDelta` | Text chunk received |
| `TextEndDelta` | Text block closed |
| `ToolCallStartDelta` | Tool call opened (ID + name) |
| `ToolCallArgumentDelta` | Argument JSON chunk |
| `ToolCallEndDelta` | Tool call closed (parsed arguments) |
| `SubAgentStartDelta` | Sub-agent invoked |
| `SubAgentDeltaDelta` | Nested delta from sub-agent |
| `SubAgentEndDelta` | Sub-agent finished |
| `ErrorDelta` | Provider or tool error |
| `DoneDelta` | Stream complete |

### Content Blocks

| Type | Allowed In |
|------|-----------|
| `TextContent` | System, User, Assistant, ToolResult |
| `ToolUseContent` | Assistant |

## Provider Interface

```go
type Provider interface {
    ChatStream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan Delta, error)
}
```

Implement this single method to integrate any LLM backend. The SDK ships an Ollama adapter.

## Tool System

Define tools with JSON schema parameters:

```go
type ToolDef struct {
    Name        string
    Description string
    Parameters  ParameterSchema
}
```

Implement the `Tool` interface or use the `ToolFunc` adapter for inline definitions:

```go
// Interface
type Tool interface {
    Definition() ToolDef
    Execute(ctx context.Context, args map[string]any) (string, error)
}

// Inline adapter
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

The registry provides `Get`, `Definitions`, and `Execute` methods.

## Agent Loop

`Agent.Invoke()` runs an iterative loop:

1. Send messages + tool definitions to the provider via `ChatStream`
2. Aggregate streaming deltas into a complete `AssistantMessage`
3. If the message contains `ToolUseContent`, execute each tool and append `ToolResultMessage`s
4. If sub-agents are configured and invoked, delegate and stream nested deltas
5. Repeat until the assistant responds with text only (no tool calls) or `MaxIter` is reached
6. Run the compactor to manage context window size

All deltas are forwarded to the caller's `EventStream` in real-time.

## Compaction

| Compactor | Behavior |
|-----------|----------|
| `NoopCompactor` | Pass-through, no compaction |
| `SlidingWindowCompactor` | Keep the last N messages (preserves system prompt) |
| `SummarizeCompactor` | Summarize older messages via the provider when threshold is exceeded |

## Sub-Agents

Delegate tasks to child agents with their own providers and tools:

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

Sub-agent deltas are wrapped in `SubAgentStartDelta`, `SubAgentDeltaDelta`, and `SubAgentEndDelta`.

## Ollama Adapter

```go
client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
adapter := ollama.NewAdapter(client)

// adapter implements agentsdk.Provider
// Also exposes: Generate, GenerateWithModel, GenerateStream, Embed, ExtractEntities
```

## SSE Bridge

Stream deltas directly to HTTP clients:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    flusher := w.(http.Flusher)
    stream := agent.Invoke(r.Context(), messages)
    agentsdk.WriteSSE(w, flusher, stream)
}
```

## Architecture

| File | Purpose |
|------|---------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`) |
| `message.go` | Message sealed interface + concrete types |
| `content.go` | Content block types (`TextContent`, `ToolUseContent`) |
| `delta.go` | Delta sealed interface + streaming types |
| `stream.go` | `EventStream` + `WriteSSE` |
| `provider.go` | `Provider` interface |
| `tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry` |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `compactor.go` | Context compaction strategies |
| `subagent.go` | `SubAgentDef` |
| `result.go` | Generic `Result[T]` (delta/final) |
| `errors.go` | Sentinel errors |
| `ollama/` | Ollama client + adapter |
