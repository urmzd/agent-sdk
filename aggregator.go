package agentsdk

import "strings"

// StreamAggregator accumulates deltas into a complete Message.
type StreamAggregator interface {
	Push(delta Delta)
	Message() Message
	Reset()
}

// DefaultAggregator builds an AssistantMessage from streaming deltas.
type DefaultAggregator struct {
	contentBlocks []AssistantContent
	textBuf       strings.Builder
	inText        bool
	toolID        string
	toolName      string
	argsBuf       strings.Builder
	inTool        bool
}

// NewDefaultAggregator creates a new DefaultAggregator.
func NewDefaultAggregator() *DefaultAggregator {
	return &DefaultAggregator{}
}

func (a *DefaultAggregator) Push(d Delta) {
	switch v := d.(type) {
	case TextStartDelta:
		a.inText = true
		a.textBuf.Reset()
	case TextContentDelta:
		if a.inText {
			a.textBuf.WriteString(v.Content)
		}
	case TextEndDelta:
		if a.inText {
			a.contentBlocks = append(a.contentBlocks, TextContent{Text: a.textBuf.String()})
			a.inText = false
		}
	case ToolCallStartDelta:
		a.inTool = true
		a.toolID = v.ID
		a.toolName = v.Name
		a.argsBuf.Reset()
	case ToolCallArgumentDelta:
		if a.inTool {
			a.argsBuf.WriteString(v.Content)
		}
	case ToolCallEndDelta:
		if a.inTool {
			a.contentBlocks = append(a.contentBlocks, ToolUseContent{
				ID:        a.toolID,
				Name:      a.toolName,
				Arguments: v.Arguments,
			})
			a.inTool = false
		}
	}
}

func (a *DefaultAggregator) Message() Message {
	// Finalize any in-progress text
	blocks := make([]AssistantContent, len(a.contentBlocks))
	copy(blocks, a.contentBlocks)

	if a.inText && a.textBuf.Len() > 0 {
		blocks = append(blocks, TextContent{Text: a.textBuf.String()})
	}

	if len(blocks) == 0 {
		return nil
	}
	return AssistantMessage{Content: blocks}
}

func (a *DefaultAggregator) Reset() {
	a.contentBlocks = nil
	a.textBuf.Reset()
	a.inText = false
	a.toolID = ""
	a.toolName = ""
	a.argsBuf.Reset()
	a.inTool = false
}
