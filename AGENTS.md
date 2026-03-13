# AGENTS.md

## Identity

You are an agent working on **agent-sdk** — a strongly-typed Go SDK for building streaming LLM agent loops. It provides typed deltas (LLM-side, execution-side, and metadata), sealed message types (three roles: system, user, assistant), parallel tool execution, sub-agent delegation with delta streaming, a branching conversation tree, runtime configuration via the tree, provider resilience (retry + fallback), structured errors, session replay, file upload with URI-based content resolution and provider content negotiation, and vector embeddings.

## Architecture

Go module with minimal dependencies (only `github.com/google/uuid`).

| File | Role |
|------|------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`, `runLoop`), config resolution (`resolveConfig`, `stripConfig`), parallel tool dispatch, sub-agent registration |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `stream.go` | `EventStream`, `Replay` for session restoration |
| `subagent.go` | `SubAgentDef`, `SubAgentInvoker` interface |
| `core/message.go` | Sealed `Message` interface (system, user, assistant — no tool role) |
| `core/content.go` | Content blocks (`TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`, `FileContent`); `MediaType` string type with MIME constants |
| `core/delta.go` | Sealed `Delta` interface (LLM-side + execution-side + metadata + terminal) |
| `core/provider.go` | `Provider` interface (`ChatStream`), `NamedProvider` |
| `core/errors.go` | Structured errors (`ProviderError`, `FallbackError`, `RetryError`), `ErrorKind`, `IsTransient`, `ClassifyHTTPStatus`; sentinels `ErrUnsupportedMediaType`, `ErrResolverNotFound` |
| `core/tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry`, `subAgentTool` |
| `core/compactor.go` | Context compaction strategies (noop, sliding window, summarize) + `CompactConfig` data-driven config |
| `core/embedder.go` | `Embedder` interface — batch text-to-vector embeddings |
| `core/resolver.go` | `Resolver` interface + `ResolverFunc` adapter — resolve a URI to raw bytes (`ResolvedFile`) |
| `core/extractor.go` | `Extractor` interface + `ExtractorFunc` adapter — convert raw bytes to `[]UserContent` |
| `core/negotiate.go` | `ContentNegotiator` optional provider interface + `ContentSupport` — declare natively supported media types |
| `core/node.go` | `Node`, `TreePath`, `BranchID`, `NodeID` |
| `core/store.go` | `Store` persistence interface |
| `core/wal.go` | `WAL` write-ahead log interface |
| `tree/tree.go` | Branching conversation tree with checkpoints, rewind, archive |
| `tree/flatten.go` | `FlattenBranch`, `FlattenAnnotated` |
| `tree/compact.go` | Tree-level compaction helpers |
| `provider/content.go` | `ResolverRegistry`, `ExtractorRegistry`, `PrepareMessages` — URI resolution, content negotiation, built-in file/http resolvers |
| `provider/fallback/` | `FallbackProvider` — multi-provider failover |
| `provider/retry/` | `RetryProvider` — exponential backoff retry |
| `provider/ollama/adapter.go` | Ollama `Provider` adapter (implements `NamedProvider`, `ContentNegotiator`; emits `UsageDelta`; maps `FileContent` → base64 images) |
| `provider/ollama/embed.go` | `OllamaEmbedder` — implements `core.Embedder` via Ollama embed API |
| `provider/ollama/client.go` | Raw Ollama HTTP client |
| `store/memwal/` | In-memory `WAL` implementation |

### Core Interfaces

| Interface | Purpose |
|-----------|---------|
| `Provider` | `ChatStream(ctx, messages, tools) (<-chan Delta, error)` — plug in any LLM |
| `NamedProvider` | `Provider` + `Name() string` — optional identification for logs/errors |
| `Tool` | `Definition() ToolDef` + `Execute(ctx, args) (string, error)` |
| `SubAgentInvoker` | `InvokeAgent(ctx, task) *EventStream` — enables delta forwarding for sub-agents |
| `Compactor` | `Compact(ctx, messages, provider) ([]Message, error)` |
| `Embedder` | `Embed(ctx, texts []string) ([][]float32, error)` — batch text-to-vector embeddings |
| `Resolver` | `Resolve(ctx, uri string) (ResolvedFile, error)` — fetch raw bytes from a URI |
| `Extractor` | `Extract(ctx, data []byte, mediaType) ([]UserContent, error)` — convert raw bytes to content blocks |
| `ContentNegotiator` | Optional provider interface: `ContentSupport() ContentSupport` — declare natively supported media types |

### Message Model

Three roles only. Tool results are content blocks, not a separate role:

- **`SystemMessage`** — system instructions, auto-executed tool results, or config (`TextContent`, `ToolResultContent`, `ConfigContent`)
- **`UserMessage`** — user input, human-in-the-loop tool results, config, or file attachments (`TextContent`, `ToolResultContent`, `ConfigContent`, `FileContent`)
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
6. Resolve `FileContent` URIs; negotiate with provider via `ContentNegotiator` — pass through natively supported types, run `Extractor` for unsupported types
7. Send messages + tool defs to provider via `ChatStream`
8. Aggregate streaming deltas into `AssistantMessage`, forward to caller
9. Capture `UsageDelta` from provider, enrich with wall-clock latency, forward
10. Persist assistant message to tree
11. If tool calls present → execute all tools **in parallel** → collect results into single `SystemMessage` with `ToolResultContent` blocks → persist
12. Sub-agent tools forward child deltas wrapped in `ToolExecDelta{ToolCallID, Inner}`
13. Repeat until text-only response or max iterations reached

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
2. Implement `Provider` interface: `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)`
3. Optionally implement `NamedProvider` for identification
4. Optionally implement `ContentNegotiator`: `ContentSupport() ContentSupport` — declare which `MediaType` values the provider accepts natively (e.g., images); the agent loop uses this to decide whether to pass `FileContent` directly or run an `Extractor` first
5. Return `*ProviderError` from `ChatStream` with appropriate `ErrorKind`
6. Emit `UsageDelta` as the last delta before closing the channel (if token counts are available)
7. Map provider-specific streaming events to SDK delta types
8. Add integration tests
