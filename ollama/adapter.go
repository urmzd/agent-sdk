package ollama

import (
	"context"

	agentsdk "github.com/urmzd/agent-sdk"
)

// Adapter wraps the Ollama Client and implements agentsdk.Provider.
type Adapter struct {
	Client *Client
}

// NewAdapter creates a new Ollama Provider adapter.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{Client: client}
}

// ChatStream implements agentsdk.Provider.
func (a *Adapter) ChatStream(ctx context.Context, messages []agentsdk.Message, tools []agentsdk.ToolDef) (<-chan agentsdk.Delta, error) {
	oMsgs := toOllamaMessages(messages)
	oTools := toOllamaTools(tools)

	rx, err := a.Client.ChatStream(ctx, oMsgs, oTools)
	if err != nil {
		return nil, err
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
			text := extractText(v.Content)
			out = append(out, ChatMessage{Role: "system", Content: text})
		case agentsdk.UserMessage:
			text := extractUserText(v.Content)
			out = append(out, ChatMessage{Role: "user", Content: text})
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
		case agentsdk.ToolResultMessage:
			text := extractToolResultText(v.Content)
			out = append(out, ChatMessage{Role: "tool", Content: text})
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

func extractText(content []agentsdk.SystemContent) string {
	var s string
	for _, c := range content {
		if tc, ok := c.(agentsdk.TextContent); ok {
			s += tc.Text
		}
	}
	return s
}

func extractUserText(content []agentsdk.UserContent) string {
	var s string
	for _, c := range content {
		if tc, ok := c.(agentsdk.TextContent); ok {
			s += tc.Text
		}
	}
	return s
}

func extractToolResultText(content []agentsdk.ToolResultContent) string {
	var s string
	for _, c := range content {
		if tc, ok := c.(agentsdk.TextContent); ok {
			s += tc.Text
		}
	}
	return s
}
