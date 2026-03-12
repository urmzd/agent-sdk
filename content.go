package agentsdk

// ── Content role interfaces (sealed) ────────────────────────────────

// SystemContent is content allowed in a SystemMessage.
type SystemContent interface{ isSystemContent() }

// UserContent is content allowed in a UserMessage.
type UserContent interface{ isUserContent() }

// AssistantContent is content allowed in an AssistantMessage.
type AssistantContent interface{ isAssistantContent() }

// ── Concrete content blocks ─────────────────────────────────────────

// TextContent holds plain text. Valid in System, User, and Assistant messages.
type TextContent struct {
	Text string
}

func (TextContent) isSystemContent()    {}
func (TextContent) isUserContent()      {}
func (TextContent) isAssistantContent() {}

// ToolUseContent represents a tool invocation by the assistant.
type ToolUseContent struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func (ToolUseContent) isAssistantContent() {}

// ToolResultContent carries the result of a tool execution.
// Valid in SystemMessage (automatic execution) or UserMessage (human-in-the-loop).
type ToolResultContent struct {
	ToolCallID string
	Text       string
}

func (ToolResultContent) isSystemContent() {}
func (ToolResultContent) isUserContent()   {}

// ConfigContent carries agent configuration. Persisted to the tree so
// that serialise/restore round-trips include the full agent config.
// Zero-valued fields mean "no change" — only non-zero fields override.
type ConfigContent struct {
	Model      string         // model name passed to Provider (empty = use default)
	MaxIter    int            // max loop iterations (0 = use previous/default)
	Compact    *CompactConfig // compaction strategy (nil = no change)
	CompactNow bool           // trigger immediate compaction this iteration
}

func (ConfigContent) isSystemContent() {}
func (ConfigContent) isUserContent()   {}
