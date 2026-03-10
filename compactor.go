package agentsdk

import "context"

// Compactor reduces message history to fit context windows.
type Compactor interface {
	Compact(ctx context.Context, messages []Message, provider Provider) ([]Message, error)
}

// NoopCompactor passes messages through unchanged.
type NoopCompactor struct{}

func (NoopCompactor) Compact(_ context.Context, messages []Message, _ Provider) ([]Message, error) {
	return messages, nil
}

// SlidingWindowCompactor keeps the first message (system) and the last N messages.
type SlidingWindowCompactor struct {
	WindowSize int
}

func NewSlidingWindowCompactor(n int) *SlidingWindowCompactor {
	return &SlidingWindowCompactor{WindowSize: n}
}

func (c *SlidingWindowCompactor) Compact(_ context.Context, messages []Message, _ Provider) ([]Message, error) {
	if len(messages) <= c.WindowSize+1 {
		return messages, nil
	}
	// Keep first (system) + last N
	result := make([]Message, 0, c.WindowSize+1)
	result = append(result, messages[0])
	result = append(result, messages[len(messages)-c.WindowSize:]...)
	return result, nil
}

// SummarizeCompactor summarizes older messages when history exceeds a threshold.
type SummarizeCompactor struct {
	Threshold int
}

func NewSummarizeCompactor(threshold int) *SummarizeCompactor {
	return &SummarizeCompactor{Threshold: threshold}
}

func (c *SummarizeCompactor) Compact(ctx context.Context, messages []Message, provider Provider) ([]Message, error) {
	if len(messages) <= c.Threshold {
		return messages, nil
	}

	// Summarize all but last 4 messages using the provider
	keepLast := 4
	if keepLast > len(messages)-1 {
		keepLast = len(messages) - 1
	}

	toSummarize := messages[1 : len(messages)-keepLast]
	if len(toSummarize) == 0 {
		return messages, nil
	}

	// Build summary prompt
	summaryReq := []Message{
		NewSystemMessage("Summarize the following conversation concisely, preserving key facts and decisions."),
		NewUserMessage(messagesToText(toSummarize)),
	}

	rx, err := provider.ChatStream(ctx, summaryReq, nil)
	if err != nil {
		return messages, nil // fallback: no compaction
	}

	var summary string
	for delta := range rx {
		if tc, ok := delta.(TextContentDelta); ok {
			summary += tc.Content
		}
	}

	result := make([]Message, 0, keepLast+2)
	result = append(result, messages[0]) // system
	result = append(result, NewUserMessage("Previous conversation summary: "+summary))
	result = append(result, messages[len(messages)-keepLast:]...)
	return result, nil
}

func messagesToText(msgs []Message) string {
	var text string
	for _, m := range msgs {
		switch v := m.(type) {
		case UserMessage:
			for _, c := range v.Content {
				if tc, ok := c.(TextContent); ok {
					text += "User: " + tc.Text + "\n"
				}
			}
		case AssistantMessage:
			for _, c := range v.Content {
				if tc, ok := c.(TextContent); ok {
					text += "Assistant: " + tc.Text + "\n"
				}
			}
		case ToolResultMessage:
			for _, c := range v.Content {
				if tc, ok := c.(TextContent); ok {
					text += "Tool: " + tc.Text + "\n"
				}
			}
		}
	}
	return text
}
