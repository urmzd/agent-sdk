package ollama

// Ollama API wire types.

type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Parameters  ToolFunctionParams `json:"parameters"`
}

type ToolFunctionParams struct {
	Type       string                  `json:"type"`
	Required   []string                `json:"required"`
	Properties map[string]ToolProperty `json:"properties"`
}

type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []Tool        `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

type ChatChunk struct {
	Message ChatMessage `json:"message"`
	Done    bool        `json:"done"`
}

type GenerateRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Stream  bool   `json:"stream"`
	Format  any    `json:"format,omitempty"`
	Options any    `json:"options,omitempty"`
}

type GenerateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type EmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

type ExtractedEntity struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

type ExtractedRelation struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
	Fact   string `json:"fact"`
}

type ExtractedData struct {
	Entities  []ExtractedEntity  `json:"entities"`
	Relations []ExtractedRelation `json:"relations"`
}
