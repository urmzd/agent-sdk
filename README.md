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
- File upload via `FileContent` — attach files by URI (file://, http://, https://)
- Content negotiation — providers declare native media type support; unsupported types are handled by pluggable extractors
- Embeddings via `Embedder` interface — batch-first API, implemented by `OllamaEmbedder`
- 15 built-in `MediaType` constants covering images, documents, audio, and video

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
    "github.com/urmzd/agent-sdk/provider/ollama"
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
| `UserMessage` | `user` | `TextContent`, `ToolResultContent`, `ConfigContent`, `FileContent` |
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
| `FileContent` | User | File attachment (URI, MediaType, Data, Filename) |

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

Providers can optionally implement `ContentNegotiator` to declare which media types they handle natively. When a `FileContent` block is in a message, the SDK checks this interface first. If the provider supports the type natively (e.g., Ollama supports JPEG and PNG via its images field), the `FileContent` is passed through as-is. If not, a registered `Extractor` is used to convert the content. If neither applies, `ErrUnsupportedMediaType` is returned.

```go
type ContentNegotiator interface {
    ContentSupport() ContentSupport
}

// ContentSupport declares which media types a provider handles natively.
type ContentSupport struct {
    NativeTypes map[MediaType]bool
}
```

## File Upload & Content Negotiation

Attach files to user messages using `FileContent`. The URI is the source of truth — raw bytes are populated at resolution time.

```go
// Single file by URI (media type inferred from extension)
msg := agentsdk.NewFileMessage("file:///path/to/image.png")

// Explicit media type
msg = agentsdk.NewFileMessage("https://example.com/doc.pdf", agentsdk.MediaPDF)

// Text + files together
msg = agentsdk.NewUserMessageWithFiles("Describe this image",
    agentsdk.FileContent{URI: "file:///tmp/photo.jpg", MediaType: agentsdk.MediaJPEG},
)
```

### MediaType constants

```go
agentsdk.MediaJPEG  // "image/jpeg"
agentsdk.MediaPNG   // "image/png"
agentsdk.MediaGIF   // "image/gif"
agentsdk.MediaWebP  // "image/webp"
agentsdk.MediaPDF   // "application/pdf"
agentsdk.MediaCSV   // "text/csv"
agentsdk.MediaMP3   // "audio/mpeg"
agentsdk.MediaWAV   // "audio/wav"
agentsdk.MediaMP4   // "video/mp4"
agentsdk.MediaDOCX  // application/vnd.openxmlformats-officedocument.wordprocessingml.document
agentsdk.MediaXLSX  // application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
agentsdk.MediaPPTX  // application/vnd.openxmlformats-officedocument.presentationml.presentation
agentsdk.MediaHTML  // "text/html"
agentsdk.MediaText  // "text/plain"
agentsdk.MediaJSON  // "application/json"
```

### Resolver interface

A `Resolver` converts a URI to raw bytes. Implement it to add support for custom URI schemes (e.g., `s3://`, `gs://`):

```go
type Resolver interface {
    Resolve(ctx context.Context, uri string) (ResolvedFile, error)
}

// ResolverFunc adapts a plain function to Resolver.
var myResolver core.ResolverFunc = func(ctx context.Context, uri string) (core.ResolvedFile, error) {
    // fetch bytes, return core.ResolvedFile{Data: ..., MediaType: ...}
}
```

### Extractor interface

An `Extractor` converts raw bytes into `[]UserContent` blocks. Use this to add extraction for types your provider does not handle natively (e.g., parse a PDF into text):

```go
type Extractor interface {
    Extract(ctx context.Context, data []byte, mediaType MediaType) ([]UserContent, error)
}

// ExtractorFunc adapts a plain function to Extractor.
var pdfExtractor core.ExtractorFunc = func(ctx context.Context, data []byte, mt core.MediaType) ([]core.UserContent, error) {
    text := extractTextFromPDF(data)
    return []core.UserContent{core.TextContent{Text: text}}, nil
}
```

### Sentinel errors

| Error | When |
|-------|------|
| `ErrUnsupportedMediaType` | Provider does not support the media type and no extractor is registered |
| `ErrResolverNotFound` | No resolver is registered for the URI scheme |

## Embeddings

Generate vector embeddings using the `Embedder` interface. The API is batch-first — pass multiple texts in a single call:

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

### OllamaEmbedder

```go
import "github.com/urmzd/agent-sdk/provider/ollama"

client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
embedder := ollama.NewEmbedder(client)

vecs, err := embedder.Embed(ctx, []string{"hello world", "goodbye world"})
// vecs[0] is the embedding for "hello world"
// vecs[1] is the embedding for "goodbye world"
```

The embed model is the third argument to `ollama.NewClient`. `OllamaEmbedder` calls the Ollama embedding endpoint once per text and returns results in input order.

## Provider Resilience

### Retry

Wrap any provider with exponential backoff retry for transient errors (429, 5xx, timeouts):

```go
import "github.com/urmzd/agent-sdk/provider/retry"

retryCfg := retry.Config{
    MaxAttempts: 3,
    BaseDelay:   500 * time.Millisecond,
    MaxDelay:    10 * time.Second,
    Multiplier:  2.0,
}
provider := retry.New(adapter, retryCfg)
```

By default, only transient errors are retried. Set `ShouldRetry` on the config to customize.

### Fallback

Try multiple providers in order — if one fails, fall back to the next:

```go
import "github.com/urmzd/agent-sdk/provider/fallback"
import "github.com/urmzd/agent-sdk/provider/ollama"

primary := ollama.NewAdapter(ollama.NewClient("http://primary:11434", "llama3", ""))
backup  := ollama.NewAdapter(ollama.NewClient("http://backup:11434", "llama3", ""))

provider := fallback.New(primary, backup)
```

By default, falls back on any error. Set `FallbackOn` on the returned `*fallback.Provider` to control which errors trigger fallback (e.g., `core.IsTransient` for transient-only).

### Composition

Retry and fallback compose naturally:

```go
retryCfg := retry.DefaultConfig()

provider := fallback.New(
    retry.New(primary, retryCfg),
    retry.New(backup, retryCfg),
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
6. Resolve `FileContent` URIs and negotiate with provider via `PrepareMessages` — native types pass through, others go through extractors
7. Send messages + tool definitions to the provider via `ChatStream`
8. Aggregate streaming deltas into a complete `AssistantMessage`, forward to caller
9. Capture `UsageDelta` from the provider, enrich with wall-clock latency, forward to caller
10. Persist the assistant message to the tree
11. If the message contains `ToolUseContent`, execute all tool calls **in parallel**
12. Collect results into a single `SystemMessage` with `ToolResultContent` blocks and persist
13. Repeat until the assistant responds with text only (no tool calls) or max iterations reached

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

// adapter implements core.Provider, core.NamedProvider, and core.ContentNegotiator
// Emits UsageDelta with token counts from Ollama's response
// Returns structured ProviderError with transient/permanent classification
// Also exposes: Generate, GenerateWithModel, GenerateStream, Embed, ExtractEntities
```

The adapter implements `ContentNegotiator` and declares native support for JPEG and PNG. When a `UserMessage` contains `FileContent` blocks with those types, the adapter base64-encodes the raw bytes into Ollama's `images` field automatically.

For embeddings, use `ollama.NewEmbedder(client)` — see the [Embeddings](#embeddings) section.

## Architecture

| File | Purpose |
|------|---------|
| `agent.go` | Agent loop, config resolution, parallel tool dispatch, sub-agent registration |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `stream.go` | `EventStream`, `Replay` |
| `subagent.go` | `SubAgentDef`, `SubAgentInvoker` |
| `core/message.go` | Sealed `Message` interface + convenience constructors (`NewFileMessage`, `NewUserMessageWithFiles`) |
| `core/content.go` | Content blocks: `TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`, `FileContent`; `MediaType` constants |
| `core/delta.go` | Sealed `Delta` interface (LLM-side + execution-side + metadata + terminal) |
| `core/provider.go` | `Provider` and `NamedProvider` interfaces |
| `core/embedder.go` | `Embedder` interface — batch vector embedding |
| `core/resolver.go` | `Resolver` interface + `ResolverFunc` adapter + `ResolvedFile` struct |
| `core/extractor.go` | `Extractor` interface + `ExtractorFunc` adapter |
| `core/negotiate.go` | `ContentNegotiator` optional provider interface + `ContentSupport` struct |
| `core/errors.go` | Structured errors (`ProviderError`, `FallbackError`, `RetryError`), sentinels, classification |
| `core/tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry` |
| `core/compactor.go` | Compaction strategies + `CompactConfig` data-driven config |
| `core/node.go` | `Node`, `TreePath`, `BranchID`, `NodeID` |
| `core/store.go` | `Store` persistence interface |
| `core/wal.go` | `WAL` write-ahead log interface |
| `tree/tree.go` | Branching conversation tree |
| `tree/flatten.go` | `FlattenBranch`, `FlattenAnnotated` |
| `tree/compact.go` | Tree-level compaction |
| `tree/diff.go` | Branch diff |
| `store/memwal/` | In-memory WAL implementation |
| `provider/content.go` | `ResolverRegistry`, `ExtractorRegistry`, `PrepareMessages` — URI resolution, content negotiation, built-in file/http resolvers |
| `provider/ollama/adapter.go` | Ollama provider adapter (implements `Provider`, `NamedProvider`, `ContentNegotiator`) |
| `provider/ollama/client.go` | Ollama HTTP client |
| `provider/ollama/embed.go` | `OllamaEmbedder` — implements `core.Embedder` |
| `provider/ollama/types.go` | Ollama wire types |
| `provider/retry/` | `RetryProvider` — exponential backoff retry |
| `provider/fallback/` | `FallbackProvider` — multi-provider failover |
