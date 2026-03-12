package ollama

import (
	"context"
	"strings"

	agentsdk "github.com/urmzd/agent-sdk"
)

// Name implements agentsdk.NamedProvider.
func (a *Adapter) Name() string { return "ollama" }

// Adapter wraps the Ollama Client and implements agentsdk.Provider.
type Adapter struct {
	Client *Client
}

// NewAdapter creates a new Ollama Provider adapter.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{Client: client}
}

// ChatStream implements agentsdk.Provider.
func (a *Adapter) ChatStream(ctx context.Context, messages []agentsdk.Message, tools []agentsdk.ToolDef, model string) (<-chan agentsdk.Delta, error) {
	oMsgs := toOllamaMessages(messages)
	oTools := toOllamaTools(tools)

	// Use model param if provided, else fall back to client default.
	m := a.Client.Model
	if model != "" {
		m = model
	}
	origModel := a.Client.Model
	a.Client.Model = m
	rx, err := a.Client.ChatStream(ctx, oMsgs, oTools)
	a.Client.Model = origModel
	if err != nil {
		return nil, &agentsdk.ProviderError{
			Provider: "ollama",
			Model:    m,
			Kind:     classifyOllamaError(err),
			Err:      err,
		}
	}

	out := make(chan agentsdk.Delta, 64)
	go func() {
		defer close(out)

		textStarted := false
		for chunk := range rx {
			if chunk.Done {
				if textStarted {
					out <- agentsdk.TextEndDelta{}
					textStarted = false
				}
				// Emit usage delta from the final chunk.
				out <- agentsdk.UsageDelta{
					PromptTokens:     chunk.PromptEvalCount,
					CompletionTokens: chunk.EvalCount,
					TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
				}
				continue
			}

			// Handle text content
			if chunk.Message.Content != "" {
				if !textStarted {
					out <- agentsdk.TextStartDelta{}
					textStarted = true
				}
				out <- agentsdk.TextContentDelta{Content: chunk.Message.Content}
			}

			// Handle tool calls
			if len(chunk.Message.ToolCalls) > 0 {
				if textStarted {
					out <- agentsdk.TextEndDelta{}
					textStarted = false
				}
				for _, tc := range chunk.Message.ToolCalls {
					id := agentsdk.NewID()
					out <- agentsdk.ToolCallStartDelta{ID: id, Name: tc.Function.Name}
					out <- agentsdk.ToolCallEndDelta{Arguments: tc.Function.Arguments}
				}
			}
		}

		if textStarted {
			out <- agentsdk.TextEndDelta{}
		}
	}()

	return out, nil
}

// ── Convenience methods (not part of Provider) ──────────────────────

// Generate delegates to the underlying client.
func (a *Adapter) Generate(ctx context.Context, prompt string) (string, error) {
	return a.Client.Generate(ctx, prompt)
}

// GenerateWithModel delegates to the underlying client.
func (a *Adapter) GenerateWithModel(ctx context.Context, prompt, model string, format, options any) (string, error) {
	return a.Client.GenerateWithModel(ctx, prompt, model, format, options)
}

// GenerateStream delegates to the underlying client.
func (a *Adapter) GenerateStream(ctx context.Context, prompt string) (<-chan string, error) {
	return a.Client.GenerateStream(ctx, prompt)
}

// Embed delegates to the underlying client.
func (a *Adapter) Embed(ctx context.Context, text string) ([]float32, error) {
	return a.Client.Embed(ctx, text)
}

// ExtractEntities delegates to the underlying client.
func (a *Adapter) ExtractEntities(ctx context.Context, text string) ([]ExtractedEntity, []ExtractedRelation, error) {
	return a.Client.ExtractEntities(ctx, text)
}

// ── Conversion helpers ──────────────────────────────────────────────

func toOllamaMessages(msgs []agentsdk.Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		switch v := m.(type) {
		case agentsdk.SystemMessage:
			// Split: text goes to system role, tool results go to tool role.
			var textParts []string
			var toolResults []agentsdk.ToolResultContent
			for _, c := range v.Content {
				switch bc := c.(type) {
				case agentsdk.TextContent:
					textParts = append(textParts, bc.Text)
				case agentsdk.ToolResultContent:
					toolResults = append(toolResults, bc)
				}
			}
			if len(textParts) > 0 {
				out = append(out, ChatMessage{Role: "system", Content: strings.Join(textParts, "")})
			}
			for _, tr := range toolResults {
				out = append(out, ChatMessage{Role: "tool", Content: tr.Text})
			}
		case agentsdk.UserMessage:
			// Split: text goes to user role, tool results go to tool role.
			var textParts []string
			var toolResults []agentsdk.ToolResultContent
			for _, c := range v.Content {
				switch bc := c.(type) {
				case agentsdk.TextContent:
					textParts = append(textParts, bc.Text)
				case agentsdk.ToolResultContent:
					toolResults = append(toolResults, bc)
				}
			}
			if len(textParts) > 0 {
				out = append(out, ChatMessage{Role: "user", Content: strings.Join(textParts, "")})
			}
			for _, tr := range toolResults {
				out = append(out, ChatMessage{Role: "tool", Content: tr.Text})
			}
		case agentsdk.AssistantMessage:
			msg := ChatMessage{Role: "assistant"}
			for _, c := range v.Content {
				switch bc := c.(type) {
				case agentsdk.TextContent:
					msg.Content += bc.Text
				case agentsdk.ToolUseContent:
					msg.ToolCalls = append(msg.ToolCalls, ToolCall{
						Function: ToolCallFunction{
							Name:      bc.Name,
							Arguments: bc.Arguments,
						},
					})
				}
			}
			out = append(out, msg)
		}
	}
	return out
}

func toOllamaTools(defs []agentsdk.ToolDef) []Tool {
	out := make([]Tool, len(defs))
	for i, d := range defs {
		props := make(map[string]ToolProperty, len(d.Parameters.Properties))
		for k, v := range d.Parameters.Properties {
			props[k] = ToolProperty{Type: v.Type, Description: v.Description}
		}
		out[i] = Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters: ToolFunctionParams{
					Type:       d.Parameters.Type,
					Required:   d.Parameters.Required,
					Properties: props,
				},
			},
		}
	}
	return out
}

// classifyOllamaError inspects the error to determine if it's transient.
func classifyOllamaError(err error) agentsdk.ErrorKind {
	s := err.Error()
	if strings.Contains(s, "connection refused") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "returned 5") ||
		strings.Contains(s, "returned 429") {
		return agentsdk.ErrorKindTransient
	}
	return agentsdk.ErrorKindPermanent
}
