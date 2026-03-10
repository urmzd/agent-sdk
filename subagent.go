package agentsdk

// SubAgentDef defines a sub-agent that can be delegated to.
type SubAgentDef struct {
	Name         string
	Description  string
	SystemPrompt string
	Provider     Provider
	Tools        *ToolRegistry
	MaxIter      int
}

// ── Sub-agent deltas ────────────────────────────────────────────────

// SubAgentStartDelta signals delegation to a sub-agent.
type SubAgentStartDelta struct {
	Name string
	Task string
}

func (SubAgentStartDelta) isDelta() {}

// SubAgentDeltaDelta wraps a child delta.
type SubAgentDeltaDelta struct {
	Name  string
	Inner Delta
}

func (SubAgentDeltaDelta) isDelta() {}

// SubAgentEndDelta signals the sub-agent completed.
type SubAgentEndDelta struct {
	Name   string
	Result string
}

func (SubAgentEndDelta) isDelta() {}
