package agentsdk

import "context"

// SubAgentDef defines a sub-agent that can be delegated to.
type SubAgentDef struct {
	Name         string
	Description  string
	SystemPrompt string
	Provider     Provider
	Tools        *ToolRegistry
	SubAgents    []SubAgentDef // sub-agents can have their own sub-agents
	MaxIter      int
}

// SubAgentInvoker is implemented by tools that wrap a sub-agent.
// The agent loop checks for this interface to enable delta forwarding
// instead of opaque Execute().
type SubAgentInvoker interface {
	InvokeAgent(ctx context.Context, task string) *EventStream
}
