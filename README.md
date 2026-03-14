# agent-sdk

A strongly-typed Go SDK for building streaming LLM agent loops.

```
go get github.com/urmzd/agent-sdk
```

## Why?

Most LLM SDKs hand you flat, untyped structs and leave you to build the agent loop yourself:

- **Untyped deltas** — you switch on string fields to figure out what the LLM is streaming.
- **Flat messages** — system, user, and assistant share the same struct with a role string.
- **Coupled agent loops** — tool execution, context compaction, and sub-agents are your problem.

**agent-sdk** solves this:

| Problem | Solution |
|---------|----------|
| Untyped streaming | Sealed `Delta` interface with 13 concrete types |
| Flat messages | Sealed `Message` interface — three roles, typed content blocks |
| Manual tool dispatch | Parallel tool execution with streaming attribution |
| No sub-agent model | Sub-agents as tools, with full delta forwarding |
| Provider lock-in | One method to implement: `ChatStream` |
| No retry/fallback | Built-in exponential backoff and multi-provider failover |
| Context overflow | Data-driven compaction (sliding window or summarize) |
| Static config | Runtime config changes via `ConfigContent` in the tree |

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    agentsdk "github.com/urmzd/agent-sdk"
    "github.com/urmzd/agent-sdk/core"
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

    stream := agent.Invoke(context.Background(), []core.Message{
        core.NewUserMessage("What is the capital of France?"),
    })

    for delta := range stream.Deltas() {
        switch d := delta.(type) {
        case core.TextContentDelta:
            fmt.Print(d.Content)
        case core.UsageDelta:
            fmt.Printf("\n[tokens: %d prompt, %d completion, latency: %s]\n",
                d.PromptTokens, d.CompletionTokens, d.Latency)
        }
    }
}
```

## Table of Contents

- [Messages](#messages)
- [Deltas](#deltas)
- [Content Blocks](#content-blocks)
- [Provider Interface](#provider-interface)
- [Tool System](#tools)
- [Agent Loop](#agent-loop)
- [Sub-Agents](#sub-agents)
- [Provider Resilience](#provider-resilience)
- [Structured Errors](#structured-errors)
- [Runtime Configuration](#runtime-configuration)
- [Compaction](#compaction)
- [Conversation Tree](#conversation-tree)
- [Session Replay](#session-replay)
- [File Upload](#file-upload)
- [Embeddings](#embeddings)
- [Ollama Adapter](#ollama-adapter)
- [Testing](#testing)
- [Architecture](#architecture)

---

## Messages

Three roles. Tool results are content blocks, not a separate role.

| Type | Role | Content Types |
|------|------|---------------|
| `SystemMessage` | `system` | `TextContent`, `ToolResultContent`, `ConfigContent` |
| `UserMessage` | `user` | `TextContent`, `ToolResultContent`, `ConfigContent`, `FileContent` |
| `AssistantMessage` | `assistant` | `TextContent`, `ToolUseContent` |

**Why no tool role?** When the SDK auto-executes a tool, the result goes in a `SystemMessage`. When a human provides a result (human-in-the-loop), it's a `UserMessage`. The provider adapter maps both to whatever wire format the LLM expects.

```go
// Constructors
core.NewSystemMessage("You are a helpful assistant.")
core.NewUserMessage("Hello!")
core.NewToolResultMessage(core.ToolResultContent{ToolCallID: "abc", Text: "result"})
core.NewUserToolResultMessage(core.ToolResultContent{ToolCallID: "abc", Text: "human result"})
core.NewFileMessage("file:///path/to/image.png")
core.NewUserMessageWithFiles("Describe this", core.FileContent{URI: "file:///img.jpg"})
```

## Deltas

Deltas are split into three categories: **LLM-side** (what the model generates), **execution-side** (what happens when tools run), and **metadata**.

| Type | Category | Fields | Purpose |
|------|----------|--------|---------|
| `TextStartDelta` | LLM | — | Text block opened |
| `TextContentDelta` | LLM | `Content` | Text chunk |
| `TextEndDelta` | LLM | — | Text block closed |
| `ToolCallStartDelta` | LLM | `ID`, `Name` | Tool call generation started |
| `ToolCallArgumentDelta` | LLM | `Content` | JSON argument chunk |
| `ToolCallEndDelta` | LLM | `Arguments` | Tool call complete (parsed args) |
| `ToolExecStartDelta` | Execution | `ToolCallID`, `Name` | Tool began executing |
| `ToolExecDelta` | Execution | `ToolCallID`, `Inner` | Streaming delta from tool/sub-agent |
| `ToolExecEndDelta` | Execution | `ToolCallID`, `Result`, `Error` | Tool finished |
| `UsageDelta` | Metadata | `PromptTokens`, `CompletionTokens`, `TotalTokens`, `Latency` | Token usage + wall-clock timing |
| `ErrorDelta` | Terminal | `Error` | Provider or tool error |
| `DoneDelta` | Terminal | — | Stream complete |

Every execution delta carries a `ToolCallID` so consumers can demux parallel tool executions.

```go
for delta := range stream.Deltas() {
    switch d := delta.(type) {
    case core.TextContentDelta:
        fmt.Print(d.Content)
    case core.ToolExecStartDelta:
        fmt.Printf("[tool %s started: %s]\n", d.ToolCallID, d.Name)
    case core.ToolExecDelta:
        if inner, ok := d.Inner.(core.TextContentDelta); ok {
            fmt.Print(inner.Content) // sub-agent text
        }
    case core.ToolExecEndDelta:
        fmt.Printf("[tool %s done]\n", d.ToolCallID)
    case core.UsageDelta:
        fmt.Printf("[%d prompt + %d completion tokens, %s]\n",
            d.PromptTokens, d.CompletionTokens, d.Latency)
    case core.ErrorDelta:
        fmt.Fprintf(os.Stderr, "error: %v\n", d.Error)
    case core.DoneDelta:
        // stream complete
    }
}
```

## Content Blocks

| Type | Allowed In | Fields | Purpose |
|------|-----------|--------|---------|
| `TextContent` | System, User, Assistant | `Text` | Plain text |
| `ToolUseContent` | Assistant | `ID`, `Name`, `Arguments` | Tool invocation |
| `ToolResultContent` | System, User | `ToolCallID`, `Text` | Tool result |
| `ConfigContent` | System, User | `Model`, `MaxIter`, `Compact`, `CompactNow` | Runtime config |
| `FileContent` | User | `URI`, `MediaType`, `Data`, `Filename` | File attachment |

## Provider Interface

Implement one method to integrate any LLM backend:

```go
type Provider interface {
    ChatStream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan Delta, error)
}
```

Each provider uses its own configured default model. Model selection is handled via `ConfigContent` in the message tree, not as a parameter to `ChatStream`.

**Optional interfaces:**

```go
// NamedProvider — identification in logs and error messages
type NamedProvider interface {
    Provider
    Name() string
}

// ContentNegotiator — declare which media types are handled natively
type ContentNegotiator interface {
    ContentSupport() ContentSupport
}
```

### Implementing a Provider

1. Create a package under `provider/` (e.g., `provider/openai/`)
2. Implement `core.Provider` — map messages/tools to your wire format, stream deltas back
3. Optionally implement `core.NamedProvider` for identification
4. Return `*core.ProviderError` with appropriate `ErrorKind` on failure
5. Emit `core.UsageDelta` as the last delta before closing the channel
6. Optionally implement `core.ContentNegotiator` if your provider supports file uploads natively

## Tools

Define tools with JSON schema parameters:

```go
tool := &core.ToolFunc{
    Def: core.ToolDef{
        Name:        "greet",
        Description: "Greet a person",
        Parameters: core.ParameterSchema{
            Type:     "object",
            Required: []string{"name"},
            Properties: map[string]core.PropertyDef{
                "name": {Type: "string", Description: "Person's name"},
            },
        },
    },
    Fn: func(ctx context.Context, args map[string]any) (string, error) {
        return fmt.Sprintf("Hello, %s!", args["name"]), nil
    },
}

registry := core.NewToolRegistry(tool)
```

**`ToolRegistry` methods:**

| Method | Signature | Purpose |
|--------|-----------|---------|
| `Get` | `(name string) (Tool, bool)` | Look up a tool by name |
| `Register` | `(Tool)` | Add a tool to the registry |
| `Definitions` | `() []ToolDef` | Return all tool schemas (for the LLM) |
| `Execute` | `(ctx, name, args) (string, error)` | Run a tool by name |

When the LLM requests multiple tool calls in a single response, all tools execute concurrently. Results are collected into a single `SystemMessage` with one `ToolResultContent` block per tool call.

## Agent Loop

`Agent.Invoke()` runs an iterative loop:

1. Flatten the conversation tree into `[]Message`
2. Resolve config from the tree (`ConfigContent` blocks, last write wins per field)
3. Check iteration cap
4. Strip `ConfigContent` from messages before sending to LLM
5. Compact if configured or `CompactNow` is set
6. Send messages + tool definitions to the provider via `ChatStream`
7. Aggregate streaming deltas into a complete `AssistantMessage`, forward to caller
8. Capture `UsageDelta` from the provider, enrich with wall-clock latency, forward
9. Persist the assistant message to the tree
10. If tool calls present → execute all tools **in parallel** → collect results into single `SystemMessage` → persist
11. Sub-agent tools forward child deltas wrapped in `ToolExecDelta{ToolCallID, Inner}`
12. Repeat until text-only response or max iterations reached

All deltas are forwarded to the caller's `EventStream` in real-time.

```go
agent := agentsdk.NewAgent(agentsdk.AgentConfig{
    Name:         "assistant",
    SystemPrompt: "You are a helpful assistant.",
    Provider:     adapter,
    Tools:        registry,
    MaxIter:      10,        // default: 10
    CompactCfg:   &core.CompactConfig{Strategy: core.CompactSlidingWindow, WindowSize: 20},
})

// Invoke on the active branch (default: "main")
stream := agent.Invoke(ctx, []core.Message{core.NewUserMessage("Hello!")})

// Or invoke on a specific branch
stream = agent.Invoke(ctx, messages, core.BranchID("feature-branch"))

// EventStream API
for delta := range stream.Deltas() { /* ... */ }
err := stream.Wait()   // block until done
stream.Cancel()         // stop the stream
```

## Sub-Agents

A sub-agent is just an agent called by an agent. Sub-agents are registered as tools (`delegate_to_<name>`) and execute within the parallel tool dispatch:

```go
agent := agentsdk.NewAgent(agentsdk.AgentConfig{
    Provider: adapter,
    SubAgents: []agentsdk.SubAgentDef{
        {
            Name:         "researcher",
            Description:  "Searches the web for information",
            SystemPrompt: "You are a research assistant.",
            Provider:     adapter,
            Tools:        core.NewToolRegistry(searchTool),
            MaxIter:      5,
        },
    },
})
```

When a sub-agent executes:
- Its deltas are forwarded through the parent's stream as `ToolExecDelta{ToolCallID, Inner}`
- Multiple sub-agents invoked in the same response run concurrently
- Sub-agents can have their own sub-agents (arbitrary nesting)
- Each child agent gets a fresh conversation tree — context isolation is total

The `SubAgentInvoker` interface enables the agent loop to detect sub-agent tools and stream their deltas instead of just collecting a string result:

```go
type SubAgentInvoker interface {
    InvokeAgent(ctx context.Context, task string) *EventStream
}
```

## Provider Resilience

### Retry

Wrap any provider with exponential backoff for transient errors:

```go
import "github.com/urmzd/agent-sdk/provider/retry"

provider := retry.New(adapter, retry.Config{
    MaxAttempts: 3,
    BaseDelay:   500 * time.Millisecond,
    MaxDelay:    10 * time.Second,
    Multiplier:  2.0,
})

// Or use defaults: 3 attempts, 500ms base, 10s cap, 2x backoff
provider = retry.New(adapter, retry.DefaultConfig())
```

By default, only transient errors (429, 5xx, timeouts) are retried. Set `ShouldRetry` to customize.

### Fallback

Try multiple providers in order:

```go
import "github.com/urmzd/agent-sdk/provider/fallback"

provider := fallback.New(primary, backup)

// Fallback on transient errors only
provider.FallbackOn = core.IsTransient
```

### Composition

Retry and fallback compose naturally — each provider retries independently before falling back:

```go
provider := fallback.New(
    retry.New(primary, retry.DefaultConfig()),
    retry.New(backup, retry.DefaultConfig()),
)
```

## Structured Errors

Errors follow Go conventions — use `errors.Is` and `errors.As` to inspect them.

| Type | When | Key Fields |
|------|------|------------|
| `ProviderError` | Single provider call fails | `Provider`, `Model`, `Kind`, `Code`, `Err` |
| `FallbackError` | All providers in a fallback chain fail | `Errors []error` |
| `RetryError` | All retry attempts exhausted | `Attempts`, `Last` |

All provider errors satisfy `errors.Is(err, ErrProviderFailed)`.

### Error Classification

```go
if core.IsTransient(err) {
    // safe to retry: 429, 5xx, timeout, connection refused
}

kind := core.ClassifyHTTPStatus(statusCode) // ErrorKindTransient or ErrorKindPermanent
```

| Transient (retry) | Permanent (don't retry) |
|--------------------|------------------------|
| 408 Request Timeout | 400 Bad Request |
| 429 Too Many Requests | 401 Unauthorized |
| 500-599 Server Errors | 403 Forbidden |
| Connection refused | 404 Not Found |
| Timeout | Other 4xx |

### Sentinel Errors

| Error | When |
|-------|------|
| `ErrToolNotFound` | Tool name not in registry |
| `ErrMaxIterations` | Agent loop exceeded iteration cap |
| `ErrStreamCanceled` | Context canceled or `stream.Cancel()` called |
| `ErrProviderFailed` | Any provider error (use `errors.Is`) |
| `ErrUnsupportedMediaType` | Provider does not support the media type |
| `ErrResolverNotFound` | No resolver registered for the URI scheme |

## Runtime Configuration

Change agent behavior mid-conversation by adding `ConfigContent` to the tree. The agent reads config each iteration — last write wins per field. Zero values mean "no change".

```go
// Change model mid-conversation
agent.Invoke(ctx, []core.Message{
    core.UserMessage{Content: []core.UserContent{
        core.ConfigContent{Model: "gpt-4"},
        core.TextContent{Text: "Use the better model for this."},
    }},
})

// Trigger immediate compaction
agent.Invoke(ctx, []core.Message{
    core.SystemMessage{Content: []core.SystemContent{
        core.ConfigContent{
            CompactNow: true,
            Compact:    &core.CompactConfig{Strategy: core.CompactSlidingWindow, WindowSize: 10},
        },
    }},
})
```

`ConfigContent` blocks are automatically stripped before messages are sent to the LLM.

| Field | Type | Effect |
|-------|------|--------|
| `Model` | `string` | Override model name (empty = use default) |
| `MaxIter` | `int` | Max loop iterations (0 = no change) |
| `Compact` | `*CompactConfig` | Compaction strategy (nil = no change) |
| `CompactNow` | `bool` | Trigger immediate compaction this iteration |

## Compaction

Data-driven compaction configuration:

```go
agentsdk.AgentConfig{
    CompactCfg: &core.CompactConfig{
        Strategy:   core.CompactSlidingWindow,
        WindowSize: 20,
    },
}
```

| Strategy | Behavior |
|----------|----------|
| `CompactNone` | No compaction |
| `CompactSlidingWindow` | Keep system prompt + last N messages |
| `CompactSummarize` | Summarize older messages via the provider when threshold exceeded |

The `Compactor` interface is also available for custom implementations:

```go
type Compactor interface {
    Compact(ctx context.Context, messages []Message, provider Provider) ([]Message, error)
}
```

## Conversation Tree

All messages are persisted to a branching conversation tree. The tree is the single source of truth — the flat `[]Message` slice the LLM sees is derived from it each iteration.

```go
tr := agent.Tree()

// Key operations
tr.AddChild(parentID, msg)                        // append a message
tr.Branch(fromNodeID, "experiment", msg)           // fork from any node
tr.UpdateUserMessage(nodeID, newMsg)               // edit → creates new branch
tr.SetActive(branchID)                             // switch branches
tr.Tip(branchID)                                   // get branch tip node
tr.FlattenBranch(branchID)                         // walk root-to-tip → []Message
tr.Checkpoint(branchID, "before-refactor")         // save branch state
tr.Rewind(checkpointID)                            // restore → new branch from checkpoint
tr.Archive(nodeID, "cleanup", true)                // soft-delete (recursive)
tr.Restore(nodeID, true)                           // un-archive
tr.Branches()                                      // list all branches
tr.Children(nodeID)                                // list child nodes
tr.Path(nodeID)                                    // root-to-node path
```

Optional persistence via `WAL` and `Store` interfaces:

```go
import "github.com/urmzd/agent-sdk/store/memwal"

wal := memwal.New()
tr, _ := tree.New(core.NewSystemMessage("..."), tree.WithWAL(wal), tree.WithStore(store))
```

## Session Replay

Restore a conversation from the tree as a delta stream:

```go
messages, _ := agent.Tree().FlattenBranch("main")
stream := agentsdk.Replay(messages)

for delta := range stream.Deltas() {
    // Same delta types as a live conversation.
    // Only assistant messages and tool results produce deltas.
}
```

## File Upload

Attach files to user messages using `FileContent`. The interfaces below enable pluggable URI resolution and content extraction for provider adapters.

```go
// Single file by URI
msg := core.NewFileMessage("file:///path/to/image.png")

// Explicit media type
msg = core.NewFileMessage("https://example.com/doc.pdf", core.MediaPDF)

// Text + files together
msg = core.NewUserMessageWithFiles("Describe this image",
    core.FileContent{URI: "file:///tmp/photo.jpg", MediaType: core.MediaJPEG},
)
```

### MediaType Constants

```go
core.MediaJPEG   // "image/jpeg"
core.MediaPNG    // "image/png"
core.MediaGIF    // "image/gif"
core.MediaWebP   // "image/webp"
core.MediaPDF    // "application/pdf"
core.MediaCSV    // "text/csv"
core.MediaMP3    // "audio/mpeg"
core.MediaWAV    // "audio/wav"
core.MediaMP4    // "video/mp4"
core.MediaDOCX   // "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
core.MediaXLSX   // "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
core.MediaPPTX   // "application/vnd.openxmlformats-officedocument.presentationml.presentation"
core.MediaHTML   // "text/html"
core.MediaText   // "text/plain"
core.MediaJSON   // "application/json"
```

### Extensibility Interfaces

**Resolver** — convert a URI to raw bytes. Implement to add support for custom URI schemes (e.g., `s3://`, `gs://`):

```go
type Resolver interface {
    Resolve(ctx context.Context, uri string) (ResolvedFile, error)
}

// Adapt a function
var myResolver core.ResolverFunc = func(ctx context.Context, uri string) (core.ResolvedFile, error) {
    data, _ := fetchFromS3(uri)
    return core.ResolvedFile{Data: data, MediaType: core.MediaPDF}, nil
}
```

**Extractor** — convert raw bytes into content blocks. Use for types your provider doesn't handle natively:

```go
type Extractor interface {
    Extract(ctx context.Context, data []byte, mediaType MediaType) ([]UserContent, error)
}

// Adapt a function
var pdfExtractor core.ExtractorFunc = func(ctx context.Context, data []byte, mt core.MediaType) ([]core.UserContent, error) {
    text := extractTextFromPDF(data)
    return []core.UserContent{core.TextContent{Text: text}}, nil
}
```

**ContentNegotiator** — providers declare native media type support:

```go
type ContentNegotiator interface {
    ContentSupport() ContentSupport
}

type ContentSupport struct {
    NativeTypes map[MediaType]bool
}
```

## Embeddings

Generate vector embeddings using the `Embedder` interface. The API is batch-first:

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

### OllamaEmbedder

```go
client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
embedder := ollama.NewEmbedder(client)

vecs, err := embedder.Embed(ctx, []string{"hello world", "goodbye world"})
// vecs[0] → embedding for "hello world"
// vecs[1] → embedding for "goodbye world"
```

The embed model is the third argument to `ollama.NewClient`.

## Ollama Adapter

```go
client := ollama.NewClient("http://localhost:11434", "qwen2.5", "nomic-embed-text")
adapter := ollama.NewAdapter(client)

// adapter implements:
//   core.Provider          — ChatStream
//   core.NamedProvider     — Name() returns "ollama"
//   core.ContentNegotiator — native JPEG/PNG support
```

The adapter handles `FileContent` blocks with JPEG/PNG by base64-encoding raw bytes into Ollama's `images` field. It emits `UsageDelta` with token counts from Ollama's response and returns structured `ProviderError` with transient/permanent classification.

**Convenience methods** (not part of the Provider interface):

| Method | Purpose |
|--------|---------|
| `Generate(ctx, prompt)` | Non-streaming generate |
| `GenerateWithModel(ctx, prompt, model, format, options)` | Generate with specific model |
| `GenerateStream(ctx, prompt)` | Streaming generate |
| `Embed(ctx, text)` | Single-text embedding |
| `ExtractEntities(ctx, text)` | LLM-based entity extraction |

## Testing

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run a specific package
go test ./tree/...
go test ./provider/retry/...
go test ./provider/fallback/...
go test ./provider/ollama/...

# Run integration tests (requires Ollama running)
go test -v -run TestIntegration ./...
```

The test suite covers:
- **`integration_test.go`** — full agent loop: tool execution, sub-agents, config resolution, compaction, session replay, error handling
- **`tree/tree_test.go`** — tree operations: branching, checkpoints, rewind, archive, flatten
- **`provider/retry/retry_test.go`** — retry logic, backoff timing, error classification
- **`provider/fallback/fallback_test.go`** — failover behavior, FallbackOn predicates
- **`provider/ollama/embed_test.go`** — embedder tests
- **`errors_test.go`** — error wrapping, `Is`/`As` compatibility

## Architecture

| File | Purpose |
|------|---------|
| `agent.go` | Agent loop, config resolution, parallel tool dispatch, sub-agent registration |
| `aggregator.go` | `DefaultAggregator` — reconstruct messages from deltas |
| `stream.go` | `EventStream`, `Replay` |
| `subagent.go` | `SubAgentDef`, `SubAgentInvoker` |
| `core/message.go` | Sealed `Message` interface + convenience constructors |
| `core/content.go` | Content blocks: `TextContent`, `ToolUseContent`, `ToolResultContent`, `ConfigContent`, `FileContent`; `MediaType` constants |
| `core/delta.go` | Sealed `Delta` interface (LLM-side + execution-side + metadata + terminal) |
| `core/provider.go` | `Provider`, `NamedProvider`, `ProviderName()` |
| `core/tool.go` | `Tool`, `ToolDef`, `ToolFunc`, `ToolRegistry` |
| `core/errors.go` | Structured errors, sentinels, classification |
| `core/compactor.go` | Compaction strategies + `CompactConfig` |
| `core/embedder.go` | `Embedder` interface |
| `core/resolver.go` | `Resolver` interface + `ResolverFunc` adapter |
| `core/extractor.go` | `Extractor` interface + `ExtractorFunc` adapter |
| `core/negotiate.go` | `ContentNegotiator` + `ContentSupport` |
| `core/node.go` | `Node`, `TreePath`, `BranchID`, `NodeID`, `CheckpointID` |
| `core/store.go` | `Store` persistence interface |
| `core/wal.go` | `WAL` write-ahead log interface |
| `tree/` | Branching conversation tree, flatten, compact, diff |
| `store/memwal/` | In-memory WAL implementation |
| `provider/retry/` | Exponential backoff retry |
| `provider/fallback/` | Multi-provider failover |
| `provider/ollama/` | Ollama adapter (Provider, ContentNegotiator, Embedder) |
