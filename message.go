package agentsdk

// Role represents the sender of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a sealed interface — one of SystemMessage, UserMessage,
// or AssistantMessage.
type Message interface {
	Role() Role
	isMessage()
}

// SystemMessage contains system instructions or automatic tool results.
type SystemMessage struct {
	Content []SystemContent
}

func (SystemMessage) Role() Role { return RoleSystem }
func (SystemMessage) isMessage()    {}

// UserMessage contains user input or human-provided tool results.
type UserMessage struct {
	Content []UserContent
}

func (UserMessage) Role() Role { return RoleUser }
func (UserMessage) isMessage()    {}

// AssistantMessage contains the model's response (text and/or tool calls).
type AssistantMessage struct {
	Content []AssistantContent
}

func (AssistantMessage) Role() Role { return RoleAssistant }
func (AssistantMessage) isMessage()    {}

// ── Convenience constructors ────────────────────────────────────────

// NewSystemMessage creates a SystemMessage with a single text block.
func NewSystemMessage(text string) SystemMessage {
	return SystemMessage{Content: []SystemContent{TextContent{Text: text}}}
}

// NewUserMessage creates a UserMessage with a single text block.
func NewUserMessage(text string) UserMessage {
	return UserMessage{Content: []UserContent{TextContent{Text: text}}}
}

// NewToolResultMessage creates a SystemMessage containing tool results.
// Tool results from automatic execution are system messages — the SDK
// executed the tools, not the user.
func NewToolResultMessage(results ...ToolResultContent) SystemMessage {
	content := make([]SystemContent, len(results))
	for i, r := range results {
		content[i] = r
	}
	return SystemMessage{Content: content}
}

// NewUserToolResultMessage creates a UserMessage containing tool results.
// Used for human-in-the-loop: the agent requested a tool call but a human
// provided the response (e.g., on interrupt).
func NewUserToolResultMessage(results ...ToolResultContent) UserMessage {
	content := make([]UserContent, len(results))
	for i, r := range results {
		content[i] = r
	}
	return UserMessage{Content: content}
}
