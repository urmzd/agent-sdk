package agentsdk

// ── Content role interfaces (sealed) ────────────────────────────────

// SystemContent is content allowed in a SystemMessage.
type SystemContent interface{ isSystemContent() }

// UserContent is content allowed in a UserMessage.
type UserContent interface{ isUserContent() }

// AssistantContent is content allowed in an AssistantMessage.
type AssistantContent interface{ isAssistantContent() }

// ToolResultContent is content allowed in a ToolResultMessage.
type ToolResultContent interface{ isToolResultContent() }

// ── Concrete content blocks ─────────────────────────────────────────

// TextContent holds plain text. Implements all content interfaces.
type TextContent struct {
	Text string
}

func (TextContent) isSystemContent()     {}
func (TextContent) isUserContent()       {}
func (TextContent) isAssistantContent()  {}
func (TextContent) isToolResultContent() {}

// ToolUseContent represents a tool invocation by the assistant.
type ToolUseContent struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func (ToolUseContent) isAssistantContent() {}
