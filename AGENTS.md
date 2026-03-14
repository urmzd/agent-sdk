# AGENTS.md

## Identity

You are an agent working on **agent-sdk** — a strongly-typed Go SDK for building streaming LLM agent loops. The SDK provides typed deltas (LLM-side, execution-side, marker, metadata), sealed message types (system, user, assistant), parallel tool execution, sub-agent delegation with delta streaming, a branching conversation tree, runtime configuration via the tree, provider resilience (retry + fallback), structured errors, session replay, file upload with content negotiation and a configurable file resolution pipeline, vector embeddings, human-in-the-loop markers, structured output via `StructuredOutputProvider`, a bubbletea-based TUI for streaming progress, and testing utilities (`agenttest`). Four provider adapters are included: Ollama, OpenAI, Anthropic, and Gemini.

## Architecture

Go module: `github.com/urmzd/agent-sdk`. Go 1.25+. Dependencies: `github.com/google/uuid`, `github.com/openai/openai-go/v3`, `github.com/anthropics/anthropic-sdk-go`, `google.golang.org/genai`, `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles`, `github.com/charmbracelet/lipgloss`.

### Root Package (`agentsdk`)

| File | Role |
|------|------|
| `agent.go` | Agent loop (`NewAgent`, `Invoke`, `runLoop`), config resolution (`resolveConfig`, `stripConfig`), `callProvider` (structured output dispatch), file resolution pipeline (`resolveFiles`), parallel tool dispatch with marker handling, sub-agent registration. `AgentConfig` fields: `Resolvers`, `Extractors`, `ResponseSchema` |
| `stream.go` | `EventStream` (delta consumer), `Replay` (session restoration). `Resolution` struct. `ResolveMarker`, `ResolveMarkerWithMessage` (consumer-facing approval API), `awaitResolution` (internal). Context-aware `send` |
| `aggregator.go` | `StreamAggregator` interface, `DefaultAggregator` — reconstruct `AssistantMessage` from streaming deltas |
| `subagent.go` | `SubAgentDef` struct, `SubAgentInvoker` interface, `subAgentTool` (implements both `Tool` and `SubAgentInvoker`) |

### `core/` Package (Domain Types)

| File | Role |
|------|------|
| `message.go` | Sealed `Message` interface (`SystemMessage`, `UserMessage`, `AssistantMessage`). Constructors: `NewSystemMessage`, `NewUserMessage`, `NewToolResultMessage`, `NewUserToolResultMessage`, `NewFileMessage`, `NewUserMessageWithFiles` |
| `content.go` | Sealed content-role interfaces (`SystemContent`, `UserContent`, `AssistantContent`). Concrete blocks: `TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`, `FileContent`. `MediaType` constants (15 types) |
| `delta.go` | Sealed `Delta` interface. 14 concrete types: text streaming (3), tool call streaming (3), tool execution (3), marker (`MarkerDelta`), metadata (`UsageDelta`), terminal (`ErrorDelta`, `DoneDelta`) |
| `provider.go` | `Provider` interface (`ChatStream`), `NamedProvider`, `StructuredOutputProvider` (`ChatStreamWithSchema`), `ProviderName()` helper |
| `tool.go` | `Tool` interface, `ToolDef`, `ToolFunc` adapter, `ToolRegistry` (Get/Register/Definitions/Execute — thread-safe via `sync.RWMutex`). `PropertyDef` enriched with `Enum`, `Items`, `Properties`, `Required`, `Default` for nested JSON schemas |
| `marker.go` | `Marker` struct (`Kind`, `Message`, `Meta`). `MarkedTool` (wraps `Tool` with `[]Marker`). `WithMarkers` constructor |
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
| `provider/ollama/` | `Client` (HTTP), `Adapter` (Provider + NamedProvider + ContentNegotiator), `OllamaEmbedder` (Embedder). Native JPEG/PNG via `images` field. Convenience: `Generate`, `GenerateWithModel`, `GenerateStream`, `Embed` |
| `provider/openai/` | `Adapter` (Provider + NamedProvider + StructuredOutputProvider + ContentNegotiator). `NewAdapter(apiKey, model, ...option.RequestOption)`. `Embedder` (`NewEmbedder(apiKey, model, ...)`). Uses `github.com/openai/openai-go/v3`. Native JPEG/PNG/GIF/WebP/PDF |
| `provider/anthropic/` | `Adapter` (Provider + NamedProvider + ContentNegotiator). `NewAdapter(apiKey, model, ...Option)`. `WithMaxTokens` option (default 4096). Uses `github.com/anthropics/anthropic-sdk-go`. Native JPEG/PNG/GIF/WebP/PDF |
| `provider/gemini/` | `Adapter` (Provider + NamedProvider + ContentNegotiator). `NewAdapter(ctx, apiKey, model) (*Adapter, error)`. `Embedder` (`NewEmbedder(ctx, apiKey, model)`). Uses `google.golang.org/genai`. Native JPEG/PNG/GIF/WebP/PDF |

### `store/` Packages

| Package | Role |
|---------|------|
| `store/memwal/` | In-memory WAL implementation |

### `tui/` Package

| File | Role |
|------|------|
| `tui/stream.go` | `StreamModel` — bubbletea model consuming a delta channel. Tracks sub-agent tool executions with pipeline stages (Initializing → Analyzing → Synthesizing → Done). `StreamVerbose` — non-interactive streaming consumer (no TTY required). Verbose formatting helpers: `FormatDelegateStart`, `FormatAgentOutput`, `FormatAgentDone`, `FormatAgentError` |
| `tui/styles.go` | Lipgloss styles and icon constants for the TUI |

### `agenttest/` Package

| File | Role |
|------|------|
| `agenttest/agenttest.go` | `ScriptedProvider` — replays predefined `[][]core.Delta` sequences, one per `ChatStream` call (thread-safe). `MockTool` — configurable result, records all call argument maps. Helpers: `TextResponse`, `ToolCallResponse`, `CollectDeltas`, `CollectText`, `CollectToolCalls`, `AssertTextContains`, `AssertToolCalled`, `AssertNoErrors`, `AssertDone` |

### `examples/` Directory

| Path | Description |
|------|-------------|
| `examples/basic/` | Tool-using agent (add two numbers) with Ollama |
| `examples/subagents/` | Parent agent delegating to a researcher sub-agent |
| `examples/resilient/` | Retry + fallback provider composition |
| `examples/streaming/` | All delta types with ANSI color terminal output |
| `examples/multimodal/` | File pipeline with a `file://` resolver and content negotiation |
| `examples/tui/` | Interactive bubbletea TUI and non-interactive verbose streaming |

## Key Interfaces

| Interface | Package | Method | Purpose |
|-----------|---------|--------|---------|
| `Provider` | `core` | `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)` | LLM integration |
| `NamedProvider` | `core` | `Provider` + `Name() string` | Identification in logs/errors |
| `StructuredOutputProvider` | `core` | `Provider` + `ChatStreamWithSchema(ctx, []Message, []ToolDef, *ParameterSchema) (<-chan Delta, error)` | JSON schema-constrained output |
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
| Marker | `MarkerDelta` | `ToolCallID`, `ToolName`, `Arguments`, `Markers` |
| Metadata | `UsageDelta` | `PromptTokens`, `CompletionTokens`, `TotalTokens`, `Latency` |
| Terminal | `ErrorDelta`, `DoneDelta` | `Error` |

All execution and marker deltas carry `ToolCallID` for parallel demux.

## Agent Loop Flow

1. Flatten conversation tree into `[]Message` from root to active branch tip
2. Resolve config from tree (`ConfigContent` blocks, last write wins per field)
3. Check iteration cap from resolved config
4. Strip `ConfigContent` from messages before sending to LLM
5. Resolve file URIs — walk `FileContent` blocks, call scheme-matched `Resolver`, then check provider `ContentNegotiator`; extract via `Extractor` if media type is not native
6. Compact if configured or `CompactNow` set
7. Send messages + tool defs to provider via `ChatStream` (or `ChatStreamWithSchema` if `ResponseSchema` is set and provider implements `StructuredOutputProvider`)
8. Aggregate streaming deltas into `AssistantMessage`, forward to caller
9. Capture `UsageDelta` from provider, enrich with wall-clock latency, forward
10. Persist assistant message to tree
11. If tool calls present → execute all tools **in parallel**: for `MarkedTool` instances, emit `MarkerDelta` and block until `ResolveMarker` is called; on approval proceed to execution; on rejection record error result and continue → collect all results into single `SystemMessage` with `ToolResultContent` blocks → persist
12. Sub-agent tools forward child deltas wrapped in `ToolExecDelta{ToolCallID, Inner}`
13. Repeat until text-only response or max iterations reached

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

- Go 1.25+
- Sealed interfaces via unexported marker methods (`isMessage()`, `isDelta()`, `isSystemContent()`, etc.)
- Functional options for `tree.New` (`WithWAL`, `WithStore`) and provider adapters (`anthropic.WithMaxTokens`)
- Streaming-first design — all LLM interaction via channels
- Structured errors with `Is`/`As`/`Unwrap` — no string matching
- Data-driven config (`CompactConfig`) over interface-heavy design
- Thread-safe `ToolRegistry` via `sync.RWMutex`

## Adding a New Provider

1. Create a package under `provider/` (e.g., `provider/myprovider/`)
2. Implement `core.Provider`: `ChatStream(ctx, []Message, []ToolDef) (<-chan Delta, error)`
3. Optionally implement `core.NamedProvider` for identification
4. Optionally implement `core.StructuredOutputProvider` if the provider supports JSON schema-constrained output
5. Return `*core.ProviderError` with appropriate `ErrorKind` (use `ClassifyHTTPStatus` for HTTP APIs)
6. Emit `core.UsageDelta` as the last delta before closing the channel (if token counts are available)
7. Map provider-specific streaming events to SDK delta types (text → `TextStart`/`TextContent`/`TextEnd`, tool calls → `ToolCallStart`/`ToolCallArgument`/`ToolCallEnd`)
8. Optionally implement `core.ContentNegotiator` if the provider handles file uploads natively
9. Add tests; use `agenttest.ScriptedProvider` and `agenttest.MockTool` for unit tests
