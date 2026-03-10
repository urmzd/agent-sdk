package agentsdk

// Role represents the sender of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a sealed interface — one of SystemMessage, UserMessage,
// AssistantMessage, or ToolResultMessage.
type Message interface {
	GetRole() Role
	isMessage()
}

// SystemMessage contains system instructions.
type SystemMessage struct {
	Content []SystemContent
}

func (SystemMessage) GetRole() Role { return RoleSystem }
func (SystemMessage) isMessage()    {}

// UserMessage contains user input.
type UserMessage struct {
	Content []UserContent
}

func (UserMessage) GetRole() Role { return RoleUser }
func (UserMessage) isMessage()    {}

// AssistantMessage contains the model's response (text and/or tool calls).
type AssistantMessage struct {
	Content []AssistantContent
}

func (AssistantMessage) GetRole() Role { return RoleAssistant }
func (AssistantMessage) isMessage()    {}

// ToolResultMessage carries the result of a tool execution.
type ToolResultMessage struct {
	ToolCallID string
	Content    []ToolResultContent
}

func (ToolResultMessage) GetRole() Role { return RoleTool }
func (ToolResultMessage) isMessage()    {}

// ── Convenience constructors ────────────────────────────────────────

// NewSystemMessage creates a SystemMessage with a single text block.
func NewSystemMessage(text string) SystemMessage {
	return SystemMessage{Content: []SystemContent{TextContent{Text: text}}}
}

// NewUserMessage creates a UserMessage with a single text block.
func NewUserMessage(text string) UserMessage {
	return UserMessage{Content: []UserContent{TextContent{Text: text}}}
}

// NewToolResultMessage creates a ToolResultMessage with a single text result.
func NewToolResultMessage(toolCallID, text string) ToolResultMessage {
	return ToolResultMessage{
		ToolCallID: toolCallID,
		Content:    []ToolResultContent{TextContent{Text: text}},
	}
}
