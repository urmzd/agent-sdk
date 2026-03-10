package agentsdk

import "context"

// Provider is the narrow LLM interface the agent loop needs.
type Provider interface {
	ChatStream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan Delta, error)
}
