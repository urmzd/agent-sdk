---
name: agent-sdk
description: Build streaming LLM agent loops in Go with typed deltas, tool execution, context compaction, and sub-agent delegation. Use when building AI agents, integrating LLM providers, or implementing tool-use patterns.
argument-hint: [task]
---

# Agent SDK

Build LLM agent loops using `agent-sdk`.

## Quick Start

```go
import (
    agentsdk "github.com/urmzd/agent-sdk"
    "github.com/urmzd/agent-sdk/ollama"
)

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

stream := agent.Invoke(ctx, []agentsdk.Message{
    agentsdk.NewUserMessage("Hello!"),
})

for delta := range stream.Deltas() {
    if d, ok := delta.(agentsdk.TextContentDelta); ok {
        fmt.Print(d.Content)
    }
}
```

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Provider** | Implement `ChatStream` to plug in any LLM backend |
| **Tools** | Register tools via `ToolRegistry`; use `ToolFunc` for inline definitions |
| **Compaction** | `NoopCompactor`, `SlidingWindowCompactor`, or `SummarizeCompactor` |
| **Sub-agents** | Delegate tasks to child agents with their own providers and tools |
| **SSE Bridge** | `WriteSSE(w, flusher, stream)` to stream deltas over HTTP |

## Adding a Tool

```go
tool := &agentsdk.ToolFunc{
    Def: agentsdk.ToolDef{
        Name: "greet", Description: "Greet a person",
        Parameters: agentsdk.ParameterSchema{
            Type: "object", Required: []string{"name"},
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
