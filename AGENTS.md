# AGENTS.md

## Identity

You are an agent working on **agent-sdk** — a strongly-typed Go SDK for building streaming LLM agent loops. It provides typed deltas (LLM-side, execution-side, and metadata), sealed message types (three roles: system, user, assistant), parallel tool execution, sub-agent delegation with delta streaming, a branching conversation tree, runtime configuration via the tree, provider resilience (retry + fallback), structured errors, and session replay.

## Architecture

Go module with minimal dependencies (only `github.com/google/uuid`).

| File | Role |
|------|------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`, `runLoop`), config resolution (`resolveConfig`, `stripConfig`), parallel tool dispatch, sub-agent registration |
| `message.go` | Sealed `Message` interface (system, user, assistant — no tool role) |
| `content.go` | Content blocks (`TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`) |
| `delta.go` | Sealed `Delta` interface (LLM-side + execution-side + metadata + terminal) |
| `stream.go` | `EventStream`, `Replay` for session restoration |
| `provider.go` | `Provider` interface (`ChatStream` with model param), `NamedProvider` |
| `provider_fallback.go` | `FallbackProvider` — multi-provider failover |
| `provider_retry.go` | `RetryProvider` — exponential backoff retry |
| `errors.go` | Structured errors (`ProviderError`, `FallbackError`, `RetryError`), `ErrorKind`, `IsTransient`, `ClassifyHTTPStatus` |
| `tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry`, `subAgentTool` |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `compactor.go` | Context compaction strategies (noop, sliding window, summarize) + `CompactConfig` data-driven config |
| `subagent.go` | `SubAgentDef`, `SubAgentInvoker` interface |
| `node.go` | `Node`, `TreePath`, `BranchID`, `NodeID` |
| `tree.go` | Branching conversation tree with checkpoints, rewind, archive |
| `flatten.go` | `FlattenBranch`, `FlattenAnnotated` |
| `store.go` | `Store` persistence interface |
| `tx.go` | `WAL` write-ahead log interface |
| `ollama/` | Ollama client + `Provider` adapter (implements `NamedProvider`, emits `UsageDelta`, returns `ProviderError`) |

### Core Interfaces

| Interface | Purpose |
|-----------|---------|
| `Provider` | `ChatStream(ctx, messages, tools, model) (<-chan Delta, error)` — plug in any LLM |
| `NamedProvider` | `Provider` + `Name() string` — optional identification for logs/errors |
| `Tool` | `Definition() ToolDef` + `Execute(ctx, args) (string, error)` |
| `SubAgentInvoker` | `InvokeAgent(ctx, task) *EventStream` — enables delta forwarding for sub-agents |
| `Compactor` | `Compact(ctx, messages, provider) ([]Message, error)` |

### Message Model

Three roles only. Tool results are content blocks, not a separate role:

- **`SystemMessage`** — system instructions, auto-executed tool results, or config (`TextContent`, `ToolResultContent`, `ConfigContent`)
- **`UserMessage`** — user input, human-in-the-loop tool results, or config (`TextContent`, `ToolResultContent`, `ConfigContent`)
- **`AssistantMessage`** — model responses (`TextContent`, `ToolUseContent`)

### Delta Types

Separated into LLM-side (what the model generates), execution-side (what happens when tools run), and metadata:

| Type | Category | Purpose |
|------|----------|---------|
| `TextStartDelta` / `TextContentDelta` / `TextEndDelta` | LLM | Text streaming |
| `ToolCallStartDelta` / `ToolCallArgumentDelta` / `ToolCallEndDelta` | LLM | Tool call generation |
| `ToolExecStartDelta` / `ToolExecDelta` / `ToolExecEndDelta` | Execution | Tool execution with `ToolCallID` for parallel demux |
| `UsageDelta` | Metadata | Token counts (prompt, completion, total) + wall-clock latency |
| `ErrorDelta` | Terminal | Error |
| `DoneDelta` | Terminal | Stream complete |

### Error Types

Structured errors following Go conventions (`errors.Is`/`errors.As` compatible):

| Type | When | Key Fields |
|------|------|------------|
| `ProviderError` | Single provider call fails | `Provider`, `Model`, `Kind` (transient/permanent), `Code`, `Err` |
| `FallbackError` | All providers in a fallback chain fail | `Errors []error` (multi-unwrap) |
| `RetryError` | All retry attempts exhausted | `Attempts`, `Last` |

All satisfy `errors.Is(err, ErrProviderFailed)`. Use `IsTransient(err)` to check if retry is appropriate.

### Agent Loop Flow

1. Flatten conversation tree into `[]Message`
2. Resolve config from tree (`ConfigContent` blocks, last write wins per field)
3. Check iteration cap from resolved config
4. Strip `ConfigContent` from messages
5. Compact if configured or `CompactNow` set
6. Send messages + tool defs to provider via `ChatStream` with resolved model
7. Aggregate streaming deltas into `AssistantMessage`, forward to caller
8. Capture `UsageDelta` from provider, enrich with wall-clock latency, forward
9. Persist assistant message to tree
10. If tool calls present → execute all tools **in parallel** → collect results into single `SystemMessage` with `ToolResultContent` blocks → persist
11. Sub-agent tools forward child deltas wrapped in `ToolExecDelta{ToolCallID, Inner}`
12. Repeat until text-only response or max iterations reached

### Persistence vs Streaming

- **Messages** are the persistence format — stored as `Node`s in the `Tree`
- **Deltas** are the streaming format — ephemeral, real-time observation
- **`Replay(messages)`** bridges the two — converts stored messages back into deltas for session restoration (skips `ConfigContent`)

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
- Structured errors with `Is`/`As`/`Unwrap` — no string matching

## Adding a New Provider

1. Create a new package (e.g., `openai/`)
2. Implement `Provider` interface: `ChatStream(ctx, []Message, []ToolDef, model) (<-chan Delta, error)`
3. Optionally implement `NamedProvider` for identification
4. Return `*ProviderError` from `ChatStream` with appropriate `ErrorKind`
5. Emit `UsageDelta` as the last delta before closing the channel (if token counts are available)
6. Map provider-specific streaming events to SDK delta types
7. Add integration tests
