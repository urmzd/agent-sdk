package agentsdk

import "context"

// Provider is the narrow LLM interface the agent loop needs.
// When model is empty, the provider uses its default model.
type Provider interface {
	ChatStream(ctx context.Context, messages []Message, tools []ToolDef, model string) (<-chan Delta, error)
}

// NamedProvider is an optional interface providers can implement
// for identification in logs and error messages.
type NamedProvider interface {
	Provider
	Name() string
}

// providerName returns the name of a provider if it implements NamedProvider,
// otherwise returns "unknown".
func providerName(p Provider) string {
	if np, ok := p.(NamedProvider); ok {
		return np.Name()
	}
	return "unknown"
}
