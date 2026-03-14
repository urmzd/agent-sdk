# AGENTS.md

## Identity

You are an agent working on **agent-sdk** — a strongly-typed Go SDK for building streaming LLM agent loops. The SDK provides typed deltas (LLM-side, execution-side, metadata), sealed message types (system, user, assistant), parallel tool execution, sub-agent delegation with delta streaming, a branching conversation tree, runtime configuration via the tree, provider resilience (retry + fallback), structured errors, session replay, file upload with content negotiation, and vector embeddings.

## Architecture

Go module: `github.com/urmzd/agent-sdk`. Minimal dependencies (only `github.com/google/uuid`). Go 1.25+.

### Root Package (`agentsdk`)

| File | Role |
|------|------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`, `runLoop`), config resolution (`resolveConfig`, `stripConfig`), parallel tool dispatch, sub-agent registration |
| `stream.go` | `EventStream` (delta consumer), `Replay` (session restoration from stored messages) |
| `aggregator.go` | `StreamAggregator` interface, `DefaultAggregator` — reconstruct `AssistantMessage` from streaming deltas |
| `subagent.go` | `SubAgentDef` struct, `SubAgentInvoker` interface, `subAgentTool` (implements both `Tool` and `SubAgentInvoker`) |

### `core/` Package (Domain Types)

| File | Role |
|------|------|
| `message.go` | Sealed `Message` interface (`SystemMessage`, `UserMessage`, `AssistantMessage`). Constructors: `NewSystemMessage`, `NewUserMessage`, `NewToolResultMessage`, `NewUserToolResultMessage`, `NewFileMessage`, `NewUserMessageWithFiles` |
| `content.go` | Sealed content-role interfaces (`SystemContent`, `UserContent`, `AssistantContent`). Concrete blocks: `TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`, `FileContent`. `MediaType` constants (15 types) |
| `delta.go` | Sealed `Delta` interface. 13 concrete types: text streaming (3), tool call streaming (3), tool execution (3), metadata (`UsageDelta`), terminal (`ErrorDelta`, `DoneDelta`) |
| `provider.go` | `Provider` interface (`ChatStream`), `NamedProvider`, `ProviderName()` helper |
| `tool.go` | `Tool` interface, `ToolDef`, `ToolFunc` adapter, `ToolRegistry` (Get/Register/Definitions/Execute) |
| `errors.go` | `ProviderError`, `FallbackError`, `RetryError`. `ErrorKind` (transient/permanent). Helpers: `IsTransient()`, `ClassifyHTTPStatus()`. Sentinels: `ErrToolNotFound`, `ErrMaxIterations`, `ErrStreamCanceled`, `ErrProviderFailed`, `ErrUnsupportedMediaType`, `ErrResolverNotFound` |
| `compactor.go` | `Compactor` interface. `CompactConfig` (data-driven: strategy + params). `CompactStrategy`: `CompactNone`, `CompactSlidingWindow`, `CompactSummarize`. Built-in: `NoopCompactor`, `SlidingWindowCompactor`, `SummarizeCompactor`. Also: `MessagesToText()` helper |
| `embedder.go` | `Embedder` interface — `Embed(ctx, []string) ([][]float32, error)` |
| `resolver.go` | `Resolver` interface — `Resolve(ctx, uri) (ResolvedFile, error)`. `ResolverFunc` adapter. `ResolvedFile` struct |
| `extractor.go` | `Extractor` interface — `Extract(ctx, data, mediaType) ([]UserContent, error)`. `ExtractorFunc` adapter |
| `negotiate.go` | `ContentNegotiator` interface — `ContentSupport()`. `ContentSupport` struct with `NativeTypes` map and `Supports()` method |
| `node.go` | `Node`, `NodeID`, `BranchID`, `CheckpointID`, `NodeState`, `TreePath`, `Checkpoint` |
| `store.go` | `Store`, `StoreTx` persistence interfaces |
| `wal.go` | `WAL` interface, `TxID`, `TxOpKind`, `TxOp` |
| `id.go` | `NewID()` — UUID generation |
| `result.go` | Result types |

### `tree/` Package

| File | Role |
|------|------|
| `tree.go` | `Tree` struct. Operations: `New`, `AddChild`, `Branch`, `UpdateUserMessage`, `SetActive`, `Active`, `Tip`, `Path`, `Children`, `Branches`, `Archive`, `Restore`, `Checkpoint`, `Rewind`, `NodePath`. Options: `WithWAL`, `WithStore` |
| `flatten.go` | `FlattenBranch`, `FlattenAnnotated` — walk root-to-tip, skip archived nodes |
| `compact.go` | Tree-level compaction helpers |
| `diff.go` | Branch diff/comparison |
| `errors.go` | `ErrNodeNotFound`, `ErrBranchNotFound`, `ErrCheckpointNotFound`, `ErrRootImmutable`, `ErrNodeArchived`, `ErrInvalidBranchPoint` |

### `provider/` Packages

| Package | Role |
|---------|------|
| `provider/retry/` | `retry.Provider` — exponential backoff. `Config` struct (MaxAttempts, BaseDelay, MaxDelay, Multiplier, ShouldRetry). `DefaultConfig()`. Implements `NamedProvider` |
| `provider/fallback/` | `fallback.Provider` — multi-provider failover. `FallbackOn` predicate field. Implements `NamedProvider` |
| `provider/ollama/` | `Client` (HTTP), `Adapter` (Provider + NamedProvider + ContentNegotiator), `OllamaEmbedder` (Embedder). Native JPEG/PNG via `images` field. Convenience: `Generate`, `GenerateWithModel`, `GenerateStream`, `Embed`, `ExtractEntities` |

### `store/` Packages

| Package | Role |
|---------|------|
| `store/memwal/` | In-memory WAL implementation |

## Key Interfaces

| Interface | Package | Method | Purpose |
|-----------|---------|--------|---------|
| `Provider` | `core` | `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)` | LLM integration |
| `NamedProvider` | `core` | `Provider` + `Name() string` | Identification in logs/errors |
| `ContentNegotiator` | `core` | `ContentSupport() ContentSupport` | Declare native file type support |
| `Tool` | `core` | `Definition() ToolDef` + `Execute(ctx, args) (string, error)` | Tool execution |
| `SubAgentInvoker` | root | `InvokeAgent(ctx, task) *EventStream` | Delta forwarding for sub-agents |
| `StreamAggregator` | root | `Push(Delta)` + `Message() Message` + `Reset()` | Delta-to-message reconstruction |
| `Compactor` | `core` | `Compact(ctx, []Message, Provider) ([]Message, error)` | Context window management |
| `Embedder` | `core` | `Embed(ctx, []string) ([][]float32, error)` | Vector embeddings |
| `Resolver` | `core` | `Resolve(ctx, uri) (ResolvedFile, error)` | URI-to-bytes resolution |
| `Extractor` | `core` | `Extract(ctx, data, mediaType) ([]UserContent, error)` | Bytes-to-content conversion |
| `Store` | `core` | Persistence backend for tree nodes | Durable storage |
| `WAL` | `core` | Write-ahead log for atomic tree mutations | Crash recovery |

## Message Model

Three roles only. Tool results are content blocks, not a separate role:

- **`SystemMessage`** — system instructions, auto-executed tool results, config. Content: `TextContent`, `ToolResultContent`, `ConfigContent`
- **`UserMessage`** — user input, human-in-the-loop tool results, config, files. Content: `TextContent`, `ToolResultContent`, `ConfigContent`, `FileContent`
- **`AssistantMessage`** — model responses. Content: `TextContent`, `ToolUseContent`

## Delta Categories

| Category | Types | Key Fields |
|----------|-------|------------|
| LLM | `TextStartDelta`, `TextContentDelta`, `TextEndDelta` | `Content` |
| LLM | `ToolCallStartDelta`, `ToolCallArgumentDelta`, `ToolCallEndDelta` | `ID`, `Name`, `Content`, `Arguments` |
| Execution | `ToolExecStartDelta`, `ToolExecDelta`, `ToolExecEndDelta` | `ToolCallID`, `Name`, `Inner`, `Result`, `Error` |
| Metadata | `UsageDelta` | `PromptTokens`, `CompletionTokens`, `TotalTokens`, `Latency` |
| Terminal | `ErrorDelta`, `DoneDelta` | `Error` |

All execution deltas carry `ToolCallID` for parallel demux.

## Agent Loop Flow

1. Flatten conversation tree into `[]Message` from root to active branch tip
2. Resolve config from tree (`ConfigContent` blocks, last write wins per field)
3. Check iteration cap from resolved config
4. Strip `ConfigContent` from messages before sending to LLM
5. Compact if configured or `CompactNow` set
6. Send messages + tool defs to provider via `ChatStream`
7. Aggregate streaming deltas into `AssistantMessage`, forward to caller
8. Capture `UsageDelta` from provider, enrich with wall-clock latency, forward
9. Persist assistant message to tree
10. If tool calls present → execute all tools **in parallel** → collect results into single `SystemMessage` with `ToolResultContent` blocks → persist
11. Sub-agent tools forward child deltas wrapped in `ToolExecDelta{ToolCallID, Inner}`
12. Repeat until text-only response or max iterations reached

## Persistence vs Streaming

- **Messages** are the persistence format — stored as `Node`s in the `Tree`
- **Deltas** are the streaming format — ephemeral, real-time observation
- **`Replay(messages)`** bridges the two — converts stored messages back into deltas for session restoration (skips `ConfigContent`)

## Error Handling

Structured errors with `errors.Is`/`errors.As` support:

| Type | When | Key Fields |
|------|------|------------|
| `ProviderError` | Single provider fails | `Provider`, `Model`, `Kind` (transient/permanent), `Code`, `Err` |
| `FallbackError` | All fallback providers fail | `Errors []error` (multi-unwrap) |
| `RetryError` | All retry attempts exhausted | `Attempts`, `Last` |

All satisfy `errors.Is(err, ErrProviderFailed)`. Use `IsTransient(err)` to check retry eligibility.

## Commands

| Task | Command |
|------|---------|
| Install | `go get github.com/urmzd/agent-sdk` |
| Test all | `go test ./...` |
| Test verbose | `go test -v ./...` |
| Test package | `go test ./tree/...` |
| Format | `gofmt -w .` |
| Lint | `golangci-lint run` |

## Code Style

- Go 1.25+, minimal dependencies
- Sealed interfaces via unexported marker methods (`isMessage()`, `isDelta()`, `isSystemContent()`, etc.)
- Functional options for `tree.New` (`WithWAL`, `WithStore`)
- Streaming-first design — all LLM interaction via channels
- Structured errors with `Is`/`As`/`Unwrap` — no string matching
- Data-driven config (`CompactConfig`) over interface-heavy design

## Adding a New Provider

1. Create a package under `provider/` (e.g., `provider/openai/`)
2. Implement `core.Provider`: `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)`
3. Optionally implement `core.NamedProvider` for identification
4. Return `*core.ProviderError` with appropriate `ErrorKind` (use `ClassifyHTTPStatus` for HTTP APIs)
5. Emit `core.UsageDelta` as the last delta before closing the channel (if token counts are available)
6. Map provider-specific streaming events to SDK delta types (text → `TextStart`/`TextContent`/`TextEnd`, tool calls → `ToolCallStart`/`ToolCallArgument`/`ToolCallEnd`)
7. Optionally implement `core.ContentNegotiator` if the provider handles file uploads natively
8. Add tests
