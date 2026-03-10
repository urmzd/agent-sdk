# AGENTS.md

## Identity

You are an agent working on **agent-sdk** — a strongly-typed Go SDK for building streaming LLM agent loops. It provides typed deltas, sealed message types, a tool registry, context compaction, sub-agent delegation, and an SSE bridge.

## Architecture

Go module with minimal dependencies (only `github.com/google/uuid`).

| File | Role |
|------|------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`, `runLoop`) |
| `message.go` | Sealed `Message` interface + concrete types per role |
| `content.go` | Content block types (`TextContent`, `ToolUseContent`) |
| `delta.go` | Sealed `Delta` interface + streaming event types |
| `stream.go` | `EventStream` + `WriteSSE` for HTTP SSE |
| `provider.go` | `Provider` interface (`ChatStream`) |
| `tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry` |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `compactor.go` | Context compaction strategies (noop, sliding window, summarize) |
| `subagent.go` | `SubAgentDef` for child agent delegation |
| `result.go` | Generic `Result[T]` (delta/final) |
| `errors.go` | Sentinel errors |
| `ollama/` | Ollama client + `Provider` adapter |

### Core Interfaces

| Interface | Purpose |
|-----------|---------|
| `Provider` | `ChatStream(ctx, messages, tools) (<-chan Delta, error)` — plug in any LLM |
| `Tool` | `Definition() ToolDef` + `Execute(ctx, args) (string, error)` |
| `Compactor` | `Compact(ctx, messages, provider) ([]Message, error)` |

### Agent Loop Flow

1. Send messages + tool defs to provider via `ChatStream`
2. Aggregate streaming deltas into `AssistantMessage`
3. If tool calls present → execute tools → append `ToolResultMessage`s
4. If sub-agents configured → delegate and stream nested deltas
5. Repeat until text-only response or `MaxIter` reached
6. Run compactor to manage context window

### Delta Types

| Type | Purpose |
|------|---------|
| `TextStartDelta` / `TextContentDelta` / `TextEndDelta` | Text streaming |
| `ToolCallStartDelta` / `ToolCallArgumentDelta` / `ToolCallEndDelta` | Tool call streaming |
| `SubAgentStartDelta` / `SubAgentDeltaDelta` / `SubAgentEndDelta` | Sub-agent events |
| `ErrorDelta` | Provider or tool error |
| `DoneDelta` | Stream complete |

## Commands

| Task | Command |
|------|---------|
| Install | `go get github.com/urmzd/agent-sdk` |
| Test | `go test ./...` |
| Format | `gofmt -w .` |
| Lint | `golangci-lint run` |

## Code Style

- Go 1.25+, minimal dependencies
- Sealed interfaces via unexported marker methods
- Functional options where appropriate
- Streaming-first design — all LLM interaction via channels

## Adding a New Provider

1. Create a new package (e.g., `openai/`)
2. Implement `Provider` interface: `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)`
3. Map provider-specific streaming events to SDK delta types
4. Add integration tests
