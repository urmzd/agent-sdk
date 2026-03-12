package agentsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// Mock Providers
// ═══════════════════════════════════════════════════════════════════════

// toolCallProvider emits a tool call on the first invocation and text on subsequent ones.
type toolCallProvider struct {
	mu       sync.Mutex
	calls    int
	toolName string
	toolID   string
	toolArgs map[string]any
	response string
}

func (p *toolCallProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	ch := make(chan Delta, 10)
	if call == 0 {
		ch <- ToolCallStartDelta{ID: p.toolID, Name: p.toolName}
		ch <- ToolCallArgumentDelta{Content: `{"key":"value"}`}
		ch <- ToolCallEndDelta{Arguments: p.toolArgs}
	} else {
		ch <- TextStartDelta{}
		ch <- TextContentDelta{Content: p.response}
		ch <- TextEndDelta{}
	}
	close(ch)
	return ch, nil
}

// multiToolCallProvider emits multiple tool calls in one response.
type multiToolCallProvider struct {
	mu        sync.Mutex
	calls     int
	toolCalls []struct {
		ID   string
		Name string
		Args map[string]any
	}
	response string
}

func (p *multiToolCallProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	ch := make(chan Delta, 20)
	if call == 0 {
		for _, tc := range p.toolCalls {
			ch <- ToolCallStartDelta{ID: tc.ID, Name: tc.Name}
			ch <- ToolCallEndDelta{Arguments: tc.Args}
		}
	} else {
		ch <- TextStartDelta{}
		ch <- TextContentDelta{Content: p.response}
		ch <- TextEndDelta{}
	}
	close(ch)
	return ch, nil
}

// multiTurnToolProvider calls a tool for the first N invocations, then responds with text.
type multiTurnToolProvider struct {
	mu           sync.Mutex
	calls        int
	toolTurns    int
	toolName     string
	finalMessage string
}

func (p *multiTurnToolProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	ch := make(chan Delta, 10)
	if call < p.toolTurns {
		id := fmt.Sprintf("call-%d", call)
		ch <- ToolCallStartDelta{ID: id, Name: p.toolName}
		ch <- ToolCallEndDelta{Arguments: map[string]any{"step": float64(call)}}
	} else {
		ch <- TextStartDelta{}
		ch <- TextContentDelta{Content: p.finalMessage}
		ch <- TextEndDelta{}
	}
	close(ch)
	return ch, nil
}

// errorProvider always returns an error from ChatStream.
type errorProvider struct {
	err error
}

func (p *errorProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	return nil, &ProviderError{
		Provider: "error-mock",
		Kind:     ErrorKindPermanent,
		Err:      p.err,
	}
}

// emptyProvider returns an empty channel (no deltas).
type emptyProvider struct{}

func (p *emptyProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	ch := make(chan Delta)
	close(ch)
	return ch, nil
}

// recordingProvider records messages sent to it and responds with text.
type recordingProvider struct {
	mu       sync.Mutex
	calls    [][]Message
	response string
}

func (p *recordingProvider) ChatStream(_ context.Context, msgs []Message, _ []ToolDef) (<-chan Delta, error) {
	p.mu.Lock()
	copied := make([]Message, len(msgs))
	copy(copied, msgs)
	p.calls = append(p.calls, copied)
	p.mu.Unlock()

	ch := make(chan Delta, 3)
	ch <- TextStartDelta{}
	ch <- TextContentDelta{Content: p.response}
	ch <- TextEndDelta{}
	close(ch)
	return ch, nil
}

// delayedProvider waits for a signal before responding. Used for cancellation tests.
type delayedProvider struct {
	ready    chan struct{}
	response string
}

func (p *delayedProvider) ChatStream(ctx context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	ch := make(chan Delta, 10)
	go func() {
		defer close(ch)
		select {
		case <-p.ready:
			ch <- TextStartDelta{}
			ch <- TextContentDelta{Content: p.response}
			ch <- TextEndDelta{}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// sequenceProvider returns a specific sequence of responses based on call index.
type sequenceProvider struct {
	mu        sync.Mutex
	calls     int
	responses []func(ch chan<- Delta)
}

func (p *sequenceProvider) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.mu.Unlock()

	ch := make(chan Delta, 20)
	if idx < len(p.responses) {
		p.responses[idx](ch)
	}
	close(ch)
	return ch, nil
}

// ═══════════════════════════════════════════════════════════════════════
// Helper: collect all deltas from a stream
// ═══════════════════════════════════════════════════════════════════════

func collectDeltas(stream *EventStream) []Delta {
	var deltas []Delta
	for d := range stream.Deltas() {
		deltas = append(deltas, d)
	}
	return deltas
}

func collectDeltasByType[T Delta](deltas []Delta) []T {
	var result []T
	for _, d := range deltas {
		if v, ok := d.(T); ok {
			result = append(result, v)
		}
	}
	return result
}

func textFromDeltas(deltas []Delta) string {
	var sb strings.Builder
	for _, d := range deltas {
		if tc, ok := d.(TextContentDelta); ok {
			sb.WriteString(tc.Content)
		}
	}
	return sb.String()
}

// ═══════════════════════════════════════════════════════════════════════
// Agent Core Loop
// ═══════════════════════════════════════════════════════════════════════

func TestAgentTextOnlyResponse(t *testing.T) {
	provider := &mockProvider{response: "Hello, world!"}
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "You are a helper.",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})
	deltas := collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain TextStart, TextContent, TextEnd, Done
	starts := collectDeltasByType[TextStartDelta](deltas)
	contents := collectDeltasByType[TextContentDelta](deltas)
	ends := collectDeltasByType[TextEndDelta](deltas)
	dones := collectDeltasByType[DoneDelta](deltas)

	if len(starts) != 1 {
		t.Errorf("TextStartDelta count = %d, want 1", len(starts))
	}
	if len(contents) != 1 || contents[0].Content != "Hello, world!" {
		t.Errorf("TextContentDelta = %v, want 'Hello, world!'", contents)
	}
	if len(ends) != 1 {
		t.Errorf("TextEndDelta count = %d, want 1", len(ends))
	}
	if len(dones) != 1 {
		t.Errorf("DoneDelta count = %d, want 1", len(dones))
	}
}

func TestAgentSingleToolCall(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "greet", Description: "greet"},
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			return "tool result: greeted", nil
		},
	}

	provider := &toolCallProvider{
		toolName: "greet",
		toolID:   "call-1",
		toolArgs: map[string]any{"name": "test"},
		response: "Done!",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("greet me")})
	deltas := collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should see tool exec start/end deltas
	execStarts := collectDeltasByType[ToolExecStartDelta](deltas)
	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)

	if len(execStarts) != 1 {
		t.Errorf("ToolExecStartDelta count = %d, want 1", len(execStarts))
	}
	if execStarts[0].Name != "greet" {
		t.Errorf("tool name = %s, want greet", execStarts[0].Name)
	}
	if len(execEnds) != 1 {
		t.Errorf("ToolExecEndDelta count = %d, want 1", len(execEnds))
	}
	if execEnds[0].Result != "tool result: greeted" {
		t.Errorf("tool result = %q, want 'tool result: greeted'", execEnds[0].Result)
	}

	// Final text response
	text := textFromDeltas(deltas)
	if !strings.Contains(text, "Done!") {
		t.Errorf("expected final text 'Done!', got %q", text)
	}

	// Verify tree has all messages persisted: system, user, assistant(tool call), tool result, assistant(text)
	msgs, _ := agent.Tree().FlattenBranch("main")
	if len(msgs) != 5 {
		t.Fatalf("tree messages = %d, want 5", len(msgs))
	}
	if msgs[0].Role() != RoleSystem {
		t.Error("msgs[0] not system")
	}
	if msgs[1].Role() != RoleUser {
		t.Error("msgs[1] not user")
	}
	if msgs[2].Role() != RoleAssistant {
		t.Error("msgs[2] not assistant (tool call)")
	}
	if msgs[3].Role() != RoleSystem {
		t.Error("msgs[3] not system (tool result)")
	}
	if msgs[4].Role() != RoleAssistant {
		t.Error("msgs[4] not assistant (final)")
	}
}

func TestAgentMultipleToolCallsInParallel(t *testing.T) {
	var mu sync.Mutex
	execOrder := []string{}

	toolA := &ToolFunc{
		Def: ToolDef{Name: "tool_a", Description: "a"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			mu.Lock()
			execOrder = append(execOrder, "a")
			mu.Unlock()
			return "result-a", nil
		},
	}
	toolB := &ToolFunc{
		Def: ToolDef{Name: "tool_b", Description: "b"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			mu.Lock()
			execOrder = append(execOrder, "b")
			mu.Unlock()
			return "result-b", nil
		},
	}

	provider := &multiToolCallProvider{
		toolCalls: []struct {
			ID   string
			Name string
			Args map[string]any
		}{
			{ID: "call-a", Name: "tool_a", Args: map[string]any{}},
			{ID: "call-b", Name: "tool_b", Args: map[string]any{}},
		},
		response: "Both done.",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(toolA, toolB),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("do both")})
	deltas := collectDeltas(stream)
	stream.Wait()

	// Both tools should have been executed
	mu.Lock()
	if len(execOrder) != 2 {
		t.Errorf("exec order length = %d, want 2", len(execOrder))
	}
	mu.Unlock()

	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)
	if len(execEnds) != 2 {
		t.Errorf("ToolExecEndDelta count = %d, want 2", len(execEnds))
	}

	// Verify tool results are in the tree as a single SystemMessage
	msgs, _ := agent.Tree().FlattenBranch("main")
	// system + user + assistant(2 tool calls) + system(2 tool results) + assistant(text)
	if len(msgs) != 5 {
		t.Fatalf("tree messages = %d, want 5", len(msgs))
	}
	// The tool result message should contain both results
	sysMsg, ok := msgs[3].(SystemMessage)
	if !ok {
		t.Fatal("msgs[3] not SystemMessage")
	}
	if len(sysMsg.Content) != 2 {
		t.Errorf("tool result content blocks = %d, want 2", len(sysMsg.Content))
	}
}

func TestAgentToolNotFound(t *testing.T) {
	provider := &toolCallProvider{
		toolName: "nonexistent_tool",
		toolID:   "call-1",
		toolArgs: map[string]any{},
		response: "After error",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("call it")})
	deltas := collectDeltas(stream)
	stream.Wait()

	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)
	if len(execEnds) != 1 {
		t.Fatalf("ToolExecEndDelta count = %d, want 1", len(execEnds))
	}
	if !strings.Contains(execEnds[0].Error, "tool not found") {
		t.Errorf("expected 'tool not found' error, got %q", execEnds[0].Error)
	}

	// The error should be persisted as a tool result
	msgs, _ := agent.Tree().FlattenBranch("main")
	sysMsg := msgs[3].(SystemMessage)
	tr := sysMsg.Content[0].(ToolResultContent)
	if !strings.Contains(tr.Text, "Error:") {
		t.Errorf("tool result text = %q, expected Error prefix", tr.Text)
	}
}

func TestAgentToolReturnsError(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "failing", Description: "always fails"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			return "", errors.New("tool broke")
		},
	}

	provider := &toolCallProvider{
		toolName: "failing",
		toolID:   "call-1",
		toolArgs: map[string]any{},
		response: "After failure",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("do it")})
	deltas := collectDeltas(stream)
	stream.Wait()

	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)
	if len(execEnds) != 1 {
		t.Fatalf("ToolExecEndDelta count = %d, want 1", len(execEnds))
	}
	if execEnds[0].Error != "tool broke" {
		t.Errorf("tool error = %q, want 'tool broke'", execEnds[0].Error)
	}
	if execEnds[0].Result != "Error: tool broke" {
		t.Errorf("tool result = %q, want 'Error: tool broke'", execEnds[0].Result)
	}
}

func TestAgentMultiTurnToolLoop(t *testing.T) {
	callCount := 0
	tool := &ToolFunc{
		Def: ToolDef{Name: "step_tool", Description: "step"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			callCount++
			return fmt.Sprintf("step-%d", callCount), nil
		},
	}

	provider := &multiTurnToolProvider{
		toolTurns:    3,
		toolName:     "step_tool",
		finalMessage: "All steps done",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("multi-step")})
	deltas := collectDeltas(stream)
	stream.Wait()

	if callCount != 3 {
		t.Errorf("tool was called %d times, want 3", callCount)
	}

	text := textFromDeltas(deltas)
	if text != "All steps done" {
		t.Errorf("final text = %q, want 'All steps done'", text)
	}

	// Tree: system + user + 3*(assistant+toolresult) + final_assistant = 2 + 6 + 1 = 9
	msgs, _ := agent.Tree().FlattenBranch("main")
	if len(msgs) != 9 {
		t.Errorf("tree messages = %d, want 9", len(msgs))
	}
}

func TestAgentMaxIterationsEnforced(t *testing.T) {
	// Provider always wants to call a tool — never sends text-only.
	infiniteToolProvider := &sequenceProvider{
		responses: make([]func(ch chan<- Delta), 100),
	}
	for i := range infiniteToolProvider.responses {
		infiniteToolProvider.responses[i] = func(ch chan<- Delta) {
			id := fmt.Sprintf("call-%d", i)
			ch <- ToolCallStartDelta{ID: id, Name: "repeat"}
			ch <- ToolCallEndDelta{Arguments: map[string]any{}}
		}
	}

	tool := &ToolFunc{
		Def: ToolDef{Name: "repeat", Description: "repeat"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			return "ok", nil
		},
	}

	agent := NewAgent(AgentConfig{
		Provider:     infiniteToolProvider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
		MaxIter:      3,
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("loop")})
	collectDeltas(stream)
	stream.Wait()

	// With MaxIter=3, should have at most 3 tool call rounds
	infiniteToolProvider.mu.Lock()
	calls := infiniteToolProvider.calls
	infiniteToolProvider.mu.Unlock()

	if calls > 3 {
		t.Errorf("provider called %d times, expected at most 3", calls)
	}
}

func TestAgentDefaultMaxIter(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Provider:     &mockProvider{response: "hi"},
		SystemPrompt: "sys",
	})
	// Default MaxIter is 10
	if agent.cfg.MaxIter != 10 {
		t.Errorf("default MaxIter = %d, want 10", agent.cfg.MaxIter)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Provider Error Handling
// ═══════════════════════════════════════════════════════════════════════

func TestAgentProviderError(t *testing.T) {
	provider := &errorProvider{err: errors.New("connection refused")}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})
	deltas := collectDeltas(stream)
	stream.Wait()

	errorDeltas := collectDeltasByType[ErrorDelta](deltas)
	if len(errorDeltas) != 1 {
		t.Fatalf("ErrorDelta count = %d, want 1", len(errorDeltas))
	}
	if !errors.Is(errorDeltas[0].Error, ErrProviderFailed) {
		t.Errorf("expected ErrProviderFailed, got %v", errorDeltas[0].Error)
	}
}

func TestAgentEmptyProviderResponse(t *testing.T) {
	provider := &emptyProvider{}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})
	deltas := collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty response => aggregator returns nil => loop breaks => DoneDelta
	dones := collectDeltasByType[DoneDelta](deltas)
	if len(dones) != 1 {
		t.Errorf("DoneDelta count = %d, want 1", len(dones))
	}

	// Tree should only have system + user (no assistant)
	msgs, _ := agent.Tree().FlattenBranch("main")
	if len(msgs) != 2 {
		t.Errorf("tree messages = %d, want 2", len(msgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Stream Cancellation
// ═══════════════════════════════════════════════════════════════════════

func TestAgentCancellation(t *testing.T) {
	provider := &delayedProvider{
		ready:    make(chan struct{}),
		response: "never",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})

	// Cancel the stream immediately
	stream.Cancel()

	// Should complete quickly
	done := make(chan struct{})
	go func() {
		stream.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not complete after cancellation")
	}
}

func TestAgentContextCancellation(t *testing.T) {
	provider := &delayedProvider{
		ready:    make(chan struct{}),
		response: "never",
	}

	ctx, cancel := context.WithCancel(context.Background())
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(ctx, []Message{NewUserMessage("Hi")})

	// Cancel via context
	cancel()

	done := make(chan struct{})
	go func() {
		stream.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not complete after context cancellation")
	}
}

func TestStreamCancelIdempotent(t *testing.T) {
	provider := &mockProvider{response: "hi"}
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})
	collectDeltas(stream)
	stream.Wait()

	// Calling Cancel multiple times should not panic
	stream.Cancel()
	stream.Cancel()
	stream.Cancel()
}

// ═══════════════════════════════════════════════════════════════════════
// Sub-Agent Integration
// ═══════════════════════════════════════════════════════════════════════

func TestSubAgentDelegation(t *testing.T) {
	childProvider := &mockProvider{response: "child result"}

	// Parent provider calls delegate_to_helper on first call, then returns text.
	parentProvider := &toolCallProvider{
		toolName: "delegate_to_helper",
		toolID:   "call-1",
		toolArgs: map[string]any{"task": "do something"},
		response: "Parent done based on child.",
	}

	agent := NewAgent(AgentConfig{
		Provider:     parentProvider,
		SystemPrompt: "parent sys",
		SubAgents: []SubAgentDef{
			{
				Name:         "helper",
				Description:  "A helper agent",
				SystemPrompt: "child sys",
				Provider:     childProvider,
			},
		},
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("delegate")})
	deltas := collectDeltas(stream)
	stream.Wait()

	// Should see ToolExecDelta with inner TextContentDelta from child
	execDeltas := collectDeltasByType[ToolExecDelta](deltas)
	if len(execDeltas) == 0 {
		t.Fatal("expected ToolExecDelta from child agent")
	}

	// Check that child deltas were forwarded
	foundChildText := false
	for _, ed := range execDeltas {
		if tc, ok := ed.Inner.(TextContentDelta); ok && tc.Content == "child result" {
			foundChildText = true
		}
	}
	if !foundChildText {
		t.Error("child text content not forwarded as ToolExecDelta")
	}

	// Final text from parent
	text := textFromDeltas(deltas)
	if !strings.Contains(text, "Parent done based on child.") {
		t.Errorf("parent final text = %q", text)
	}
}

func TestSubAgentRegisteredAsTool(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Provider:     &mockProvider{response: "hi"},
		SystemPrompt: "sys",
		SubAgents: []SubAgentDef{
			{Name: "helper", Description: "helps", Provider: &mockProvider{response: "ok"}},
		},
	})

	// Tool should be registered as delegate_to_helper
	tool, found := agent.tools.Get("delegate_to_helper")
	if !found {
		t.Fatal("delegate_to_helper not found in registry")
	}

	// Should implement SubAgentInvoker
	if _, ok := tool.(SubAgentInvoker); !ok {
		t.Error("subagent tool does not implement SubAgentInvoker")
	}

	// Tool definition should have "task" parameter
	def := tool.Definition()
	if def.Name != "delegate_to_helper" {
		t.Errorf("tool name = %s, want delegate_to_helper", def.Name)
	}
	if _, ok := def.Parameters.Properties["task"]; !ok {
		t.Error("tool def missing 'task' parameter")
	}
}

func TestSubAgentBlockingExecute(t *testing.T) {
	childProvider := &mockProvider{response: "child output"}

	sat := &subAgentTool{
		def: ToolDef{Name: "test_sub", Description: "test"},
		factory: func() *Agent {
			return NewAgent(AgentConfig{
				Provider:     childProvider,
				SystemPrompt: "child",
			})
		},
	}

	result, err := sat.Execute(context.Background(), map[string]any{"task": "do it"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "child output" {
		t.Errorf("result = %q, want 'child output'", result)
	}
}

func TestNestedSubAgents(t *testing.T) {
	// Grandchild provider
	grandchildProvider := &mockProvider{response: "grandchild says hi"}

	// Child calls delegate_to_grandchild, then returns text
	childProvider := &toolCallProvider{
		toolName: "delegate_to_grandchild",
		toolID:   "gc-call",
		toolArgs: map[string]any{"task": "nested task"},
		response: "child relayed grandchild",
	}

	// Parent calls delegate_to_child, then returns text
	parentProvider := &toolCallProvider{
		toolName: "delegate_to_child",
		toolID:   "c-call",
		toolArgs: map[string]any{"task": "delegate deeper"},
		response: "parent done",
	}

	agent := NewAgent(AgentConfig{
		Provider:     parentProvider,
		SystemPrompt: "parent",
		SubAgents: []SubAgentDef{
			{
				Name:         "child",
				Description:  "child",
				SystemPrompt: "child sys",
				Provider:     childProvider,
				SubAgents: []SubAgentDef{
					{
						Name:         "grandchild",
						Description:  "grandchild",
						SystemPrompt: "gc sys",
						Provider:     grandchildProvider,
					},
				},
			},
		},
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("go deep")})
	collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = textFromDeltas(collectDeltas(agent.Invoke(context.Background(), []Message{})))
	// Just verify it didn't crash — structure is correct if no panic
}

// ═══════════════════════════════════════════════════════════════════════
// Compactor Integration with Agent
// ═══════════════════════════════════════════════════════════════════════

func TestAgentWithNoopCompactor(t *testing.T) {
	recording := &recordingProvider{response: "hi"}

	agent := NewAgent(AgentConfig{
		Provider:     recording,
		SystemPrompt: "sys",
		CompactCfg:   &CompactConfig{Strategy: CompactNone},
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("hello")})
	collectDeltas(stream)
	stream.Wait()

	recording.mu.Lock()
	if len(recording.calls) != 1 {
		t.Fatalf("provider called %d times, want 1", len(recording.calls))
	}
	// NoopCompactor should pass all messages through unchanged
	if len(recording.calls[0]) != 2 { // system + user
		t.Errorf("messages sent to provider = %d, want 2", len(recording.calls[0]))
	}
	recording.mu.Unlock()
}

func TestAgentWithSlidingWindowCompactor(t *testing.T) {
	recording := &recordingProvider{response: "reply"}

	// Build a tree with several messages already
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	current := root
	for i := 0; i < 10; i++ {
		var msg Message
		if i%2 == 0 {
			msg = NewUserMessage(fmt.Sprintf("user-%d", i))
		} else {
			msg = AssistantMessage{Content: []AssistantContent{TextContent{Text: fmt.Sprintf("asst-%d", i)}}}
		}
		node, _ := tree.AddChild(current.ID, msg)
		current = node
	}

	agent := NewAgent(AgentConfig{
		Provider:   recording,
		CompactCfg: &CompactConfig{Strategy: CompactSlidingWindow, WindowSize: 3},
		Tree:       tree,
	})

	stream := agent.Invoke(context.Background(), []Message{})
	collectDeltas(stream)
	stream.Wait()

	recording.mu.Lock()
	// Should have compacted: system + last 3 = 4 messages
	if len(recording.calls[0]) != 4 {
		t.Errorf("messages after sliding window = %d, want 4", len(recording.calls[0]))
	}
	// First should be system
	if recording.calls[0][0].Role() != RoleSystem {
		t.Error("first message should be system")
	}
	recording.mu.Unlock()
}

func TestSlidingWindowCompactorBelowWindow(t *testing.T) {
	compactor := NewSlidingWindowCompactor(5)
	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
	}

	result, err := compactor.Compact(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("messages = %d, want 3 (no compaction)", len(result))
	}
}

func TestSummarizeCompactorBelowThreshold(t *testing.T) {
	compactor := NewSummarizeCompactor(10)
	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
	}

	result, err := compactor.Compact(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("messages = %d, want 2 (no compaction)", len(result))
	}
}

func TestSummarizeCompactorAboveThreshold(t *testing.T) {
	provider := &mockProvider{response: "conversation summary"}
	compactor := NewSummarizeCompactor(3)

	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
		NewUserMessage("three"),
		NewUserMessage("four"),
		NewUserMessage("five"),
	}

	result, err := compactor.Compact(context.Background(), msgs, provider)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Should be: system + summary + last 4 = 6
	if len(result) != 6 {
		t.Errorf("compacted length = %d, want 6", len(result))
	}

	// First should be system
	if result[0].Role() != RoleSystem {
		t.Error("first message should be system")
	}
	// Second should be summary
	um, ok := result[1].(UserMessage)
	if !ok {
		t.Fatal("second message should be UserMessage (summary)")
	}
	tc, ok := um.Content[0].(TextContent)
	if !ok {
		t.Fatal("summary content should be TextContent")
	}
	if !strings.Contains(tc.Text, "conversation summary") {
		t.Errorf("summary text = %q, expected to contain provider response", tc.Text)
	}
}

func TestSummarizeCompactorProviderError(t *testing.T) {
	provider := &errorProvider{err: errors.New("provider down")}
	compactor := NewSummarizeCompactor(2)

	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
		NewUserMessage("three"),
	}

	// Provider error should cause fallback to original messages
	result, err := compactor.Compact(context.Background(), msgs, provider)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(result) != 4 {
		t.Errorf("messages = %d, want 4 (fallback)", len(result))
	}
}

func TestAgentCompactorErrorSilentlyIgnored(t *testing.T) {
	// SummarizeCompactor with threshold=2 will try to summarize via the provider,
	// but the provider just responds with text — the compactor still works.
	// The key point: even if compaction produces no improvement, the agent proceeds.
	recording := &recordingProvider{response: "hi"}

	agent := NewAgent(AgentConfig{
		Provider:     recording,
		SystemPrompt: "sys",
		CompactCfg:   &CompactConfig{Strategy: CompactSummarize, Threshold: 2},
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("hello")})
	collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still work — agent proceeds regardless of compaction outcome.
	recording.mu.Lock()
	if len(recording.calls) < 1 {
		t.Fatalf("provider should have been called at least once")
	}
	recording.mu.Unlock()
}

// ═══════════════════════════════════════════════════════════════════════
// DefaultAggregator
// ═══════════════════════════════════════════════════════════════════════

func TestAggregatorTextOnly(t *testing.T) {
	agg := NewDefaultAggregator()
	agg.Push(TextStartDelta{})
	agg.Push(TextContentDelta{Content: "Hello "})
	agg.Push(TextContentDelta{Content: "World"})
	agg.Push(TextEndDelta{})

	msg := agg.Message()
	am, ok := msg.(AssistantMessage)
	if !ok {
		t.Fatal("expected AssistantMessage")
	}
	if len(am.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(am.Content))
	}
	tc, ok := am.Content[0].(TextContent)
	if !ok {
		t.Fatal("expected TextContent")
	}
	if tc.Text != "Hello World" {
		t.Errorf("text = %q, want 'Hello World'", tc.Text)
	}
}

func TestAggregatorToolCallOnly(t *testing.T) {
	agg := NewDefaultAggregator()
	agg.Push(ToolCallStartDelta{ID: "tc-1", Name: "search"})
	agg.Push(ToolCallArgumentDelta{Content: `{"q": "test"}`})
	agg.Push(ToolCallEndDelta{Arguments: map[string]any{"q": "test"}})

	msg := agg.Message()
	am, ok := msg.(AssistantMessage)
	if !ok {
		t.Fatal("expected AssistantMessage")
	}
	if len(am.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(am.Content))
	}
	tuc, ok := am.Content[0].(ToolUseContent)
	if !ok {
		t.Fatal("expected ToolUseContent")
	}
	if tuc.ID != "tc-1" || tuc.Name != "search" {
		t.Errorf("tool call = %+v", tuc)
	}
}

func TestAggregatorMixedTextAndToolCalls(t *testing.T) {
	agg := NewDefaultAggregator()

	// Text first
	agg.Push(TextStartDelta{})
	agg.Push(TextContentDelta{Content: "Let me search"})
	agg.Push(TextEndDelta{})

	// Then tool call
	agg.Push(ToolCallStartDelta{ID: "tc-1", Name: "search"})
	agg.Push(ToolCallEndDelta{Arguments: map[string]any{"q": "test"}})

	msg := agg.Message()
	am := msg.(AssistantMessage)
	if len(am.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(am.Content))
	}
	if _, ok := am.Content[0].(TextContent); !ok {
		t.Error("first block should be TextContent")
	}
	if _, ok := am.Content[1].(ToolUseContent); !ok {
		t.Error("second block should be ToolUseContent")
	}
}

func TestAggregatorEmptyReturnsNil(t *testing.T) {
	agg := NewDefaultAggregator()
	if msg := agg.Message(); msg != nil {
		t.Errorf("expected nil, got %v", msg)
	}
}

func TestAggregatorReset(t *testing.T) {
	agg := NewDefaultAggregator()
	agg.Push(TextStartDelta{})
	agg.Push(TextContentDelta{Content: "hello"})
	agg.Push(TextEndDelta{})

	agg.Reset()
	if msg := agg.Message(); msg != nil {
		t.Error("expected nil after reset")
	}
}

func TestAggregatorInProgressText(t *testing.T) {
	agg := NewDefaultAggregator()
	agg.Push(TextStartDelta{})
	agg.Push(TextContentDelta{Content: "partial"})
	// No TextEndDelta — in-progress

	msg := agg.Message()
	am := msg.(AssistantMessage)
	tc := am.Content[0].(TextContent)
	if tc.Text != "partial" {
		t.Errorf("in-progress text = %q, want 'partial'", tc.Text)
	}
}

func TestAggregatorIgnoresNonTextNonToolDeltas(t *testing.T) {
	agg := NewDefaultAggregator()
	agg.Push(ErrorDelta{Error: errors.New("ignored")})
	agg.Push(DoneDelta{})
	agg.Push(ToolExecStartDelta{})
	agg.Push(ToolExecEndDelta{})

	if msg := agg.Message(); msg != nil {
		t.Error("expected nil, aggregator should ignore non-text/tool deltas")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Replay
// ═══════════════════════════════════════════════════════════════════════

func TestReplayAssistantText(t *testing.T) {
	messages := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("hello"),
		AssistantMessage{Content: []AssistantContent{TextContent{Text: "hi there"}}},
	}

	stream := Replay(messages)
	deltas := collectDeltas(stream)
	stream.Wait()

	// System and user messages produce no deltas; assistant text produces TextStart/Content/End + Done
	starts := collectDeltasByType[TextStartDelta](deltas)
	contents := collectDeltasByType[TextContentDelta](deltas)
	ends := collectDeltasByType[TextEndDelta](deltas)
	dones := collectDeltasByType[DoneDelta](deltas)

	if len(starts) != 1 {
		t.Errorf("TextStartDelta count = %d, want 1", len(starts))
	}
	if len(contents) != 1 || contents[0].Content != "hi there" {
		t.Errorf("content = %v, want 'hi there'", contents)
	}
	if len(ends) != 1 {
		t.Errorf("TextEndDelta count = %d, want 1", len(ends))
	}
	if len(dones) != 1 {
		t.Errorf("DoneDelta count = %d, want 1", len(dones))
	}
}

func TestReplayAssistantToolUse(t *testing.T) {
	messages := []Message{
		AssistantMessage{Content: []AssistantContent{
			ToolUseContent{ID: "tc-1", Name: "search", Arguments: map[string]any{"q": "test"}},
		}},
	}

	stream := Replay(messages)
	deltas := collectDeltas(stream)
	stream.Wait()

	toolStarts := collectDeltasByType[ToolCallStartDelta](deltas)
	toolEnds := collectDeltasByType[ToolCallEndDelta](deltas)

	if len(toolStarts) != 1 || toolStarts[0].ID != "tc-1" || toolStarts[0].Name != "search" {
		t.Errorf("ToolCallStartDelta = %+v", toolStarts)
	}
	if len(toolEnds) != 1 {
		t.Errorf("ToolCallEndDelta count = %d, want 1", len(toolEnds))
	}
}

func TestReplayToolResults(t *testing.T) {
	messages := []Message{
		NewToolResultMessage(
			ToolResultContent{ToolCallID: "tc-1", Text: "result1"},
			ToolResultContent{ToolCallID: "tc-2", Text: "result2"},
		),
	}

	stream := Replay(messages)
	deltas := collectDeltas(stream)
	stream.Wait()

	execStarts := collectDeltasByType[ToolExecStartDelta](deltas)
	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)

	if len(execStarts) != 2 {
		t.Errorf("ToolExecStartDelta count = %d, want 2", len(execStarts))
	}
	if len(execEnds) != 2 {
		t.Errorf("ToolExecEndDelta count = %d, want 2", len(execEnds))
	}
}

func TestReplayUserToolResults(t *testing.T) {
	messages := []Message{
		NewUserToolResultMessage(ToolResultContent{ToolCallID: "tc-1", Text: "user result"}),
	}

	stream := Replay(messages)
	deltas := collectDeltas(stream)
	stream.Wait()

	execEnds := collectDeltasByType[ToolExecEndDelta](deltas)
	if len(execEnds) != 1 {
		t.Errorf("ToolExecEndDelta count = %d, want 1", len(execEnds))
	}
	if execEnds[0].Result != "user result" {
		t.Errorf("result = %q, want 'user result'", execEnds[0].Result)
	}
}

func TestReplayMixedConversation(t *testing.T) {
	messages := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("hello"),
		AssistantMessage{Content: []AssistantContent{
			TextContent{Text: "I'll search"},
			ToolUseContent{ID: "tc-1", Name: "search", Arguments: map[string]any{}},
		}},
		NewToolResultMessage(ToolResultContent{ToolCallID: "tc-1", Text: "found it"}),
		AssistantMessage{Content: []AssistantContent{TextContent{Text: "Here is the result"}}},
	}

	stream := Replay(messages)
	deltas := collectDeltas(stream)
	stream.Wait()

	// 2 text blocks (from 2 assistant messages) + 1 tool use + 1 tool result
	textStarts := collectDeltasByType[TextStartDelta](deltas)
	toolCallStarts := collectDeltasByType[ToolCallStartDelta](deltas)
	execStarts := collectDeltasByType[ToolExecStartDelta](deltas)

	if len(textStarts) != 2 {
		t.Errorf("TextStartDelta count = %d, want 2", len(textStarts))
	}
	if len(toolCallStarts) != 1 {
		t.Errorf("ToolCallStartDelta count = %d, want 1", len(toolCallStarts))
	}
	if len(execStarts) != 1 {
		t.Errorf("ToolExecStartDelta count = %d, want 1", len(execStarts))
	}
}

func TestReplayEmptyMessages(t *testing.T) {
	stream := Replay(nil)
	deltas := collectDeltas(stream)
	stream.Wait()

	// Only DoneDelta expected
	if len(deltas) != 1 {
		t.Errorf("deltas = %d, want 1 (DoneDelta only)", len(deltas))
	}
	if _, ok := deltas[0].(DoneDelta); !ok {
		t.Error("expected DoneDelta")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// ToolRegistry
// ═══════════════════════════════════════════════════════════════════════

func TestToolRegistryBasicOps(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "test_tool", Description: "test"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			return "ok", nil
		},
	}

	reg := NewToolRegistry(tool)

	// Get
	found, ok := reg.Get("test_tool")
	if !ok {
		t.Fatal("tool not found")
	}
	if found.Definition().Name != "test_tool" {
		t.Error("wrong tool returned")
	}

	// Get nonexistent
	_, ok = reg.Get("nope")
	if ok {
		t.Error("should not find nonexistent tool")
	}

	// Definitions
	defs := reg.Definitions()
	if len(defs) != 1 {
		t.Errorf("definitions count = %d, want 1", len(defs))
	}

	// Execute
	result, err := reg.Execute(context.Background(), "test_tool", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want 'ok'", result)
	}

	// Execute nonexistent
	_, err = reg.Execute(context.Background(), "nope", nil)
	if !errors.Is(err, ErrToolNotFound) {
		t.Errorf("expected ErrToolNotFound, got %v", err)
	}
}

func TestToolRegistryRegister(t *testing.T) {
	reg := NewToolRegistry()

	tool := &ToolFunc{
		Def: ToolDef{Name: "added", Description: "added later"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			return "added result", nil
		},
	}

	reg.Register(tool)
	_, ok := reg.Get("added")
	if !ok {
		t.Error("registered tool not found")
	}
}

func TestToolRegistryOverwrite(t *testing.T) {
	tool1 := &ToolFunc{
		Def: ToolDef{Name: "tool", Description: "v1"},
		Fn:  func(_ context.Context, _ map[string]any) (string, error) { return "v1", nil },
	}
	tool2 := &ToolFunc{
		Def: ToolDef{Name: "tool", Description: "v2"},
		Fn:  func(_ context.Context, _ map[string]any) (string, error) { return "v2", nil },
	}

	reg := NewToolRegistry(tool1)
	reg.Register(tool2)

	result, _ := reg.Execute(context.Background(), "tool", nil)
	if result != "v2" {
		t.Errorf("result = %q, want 'v2' (overwritten)", result)
	}
}

func TestEmptyToolRegistry(t *testing.T) {
	reg := NewToolRegistry()

	defs := reg.Definitions()
	if len(defs) != 0 {
		t.Errorf("definitions = %d, want 0", len(defs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Tree + Agent Integration
// ═══════════════════════════════════════════════════════════════════════

func TestAgentMultipleInvocationsOnSameTree(t *testing.T) {
	provider := &mockProvider{response: "response"}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	// First conversation turn
	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("turn 1")})
	collectDeltas(stream)
	stream.Wait()

	// Second conversation turn (continues on same branch)
	stream = agent.Invoke(context.Background(), []Message{NewUserMessage("turn 2")})
	collectDeltas(stream)
	stream.Wait()

	msgs, _ := agent.Tree().FlattenBranch("main")
	// system + user1 + asst1 + user2 + asst2 = 5
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5", len(msgs))
	}
	if msgs[3].Role() != RoleUser {
		t.Error("msgs[3] should be user (turn 2)")
	}
	if msgs[4].Role() != RoleAssistant {
		t.Error("msgs[4] should be assistant (turn 2)")
	}
}

func TestAgentInvokeWithMultipleInputMessages(t *testing.T) {
	provider := &mockProvider{response: "got both"}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{
		NewUserMessage("first"),
		NewUserMessage("second"),
	})
	collectDeltas(stream)
	stream.Wait()

	msgs, _ := agent.Tree().FlattenBranch("main")
	// system + user1 + user2 + assistant = 4
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
}

func TestAgentBranchAndContinue(t *testing.T) {
	provider := &mockProvider{response: "branched response"}

	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))
	asst, _ := tree.AddChild(user.ID, AssistantMessage{
		Content: []AssistantContent{TextContent{Text: "hi"}},
	})

	// Branch from assistant
	branchID, _, _ := tree.Branch(asst.ID, "edit", NewUserMessage("different question"))

	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tree:     tree,
	})

	// Invoke on the branch
	stream := agent.Invoke(context.Background(), []Message{}, branchID)
	collectDeltas(stream)
	stream.Wait()

	// Branch should have: sys + hello + hi + different question + branched response
	msgs, _ := tree.FlattenBranch(branchID)
	if len(msgs) != 5 {
		t.Fatalf("branch messages = %d, want 5", len(msgs))
	}

	// Main should still be: sys + hello + hi
	mainMsgs, _ := tree.FlattenBranch("main")
	if len(mainMsgs) != 3 {
		t.Errorf("main messages = %d, want 3", len(mainMsgs))
	}
}

func TestAgentInvokeOnNonExistentBranch(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Provider:     &mockProvider{response: "hi"},
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")}, "nonexistent")
	deltas := collectDeltas(stream)
	stream.Wait()

	// Should get an ErrorDelta for branch not found
	errorDeltas := collectDeltasByType[ErrorDelta](deltas)
	if len(errorDeltas) == 0 {
		t.Fatal("expected ErrorDelta for nonexistent branch")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// EventStream Edge Cases
// ═══════════════════════════════════════════════════════════════════════

func TestEventStreamDrainRequired(t *testing.T) {
	provider := &mockProvider{response: "hi"}
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})

	// Must drain before Wait completes (DoneDelta is sent to channel)
	done := make(chan error)
	go func() {
		done <- stream.Wait()
	}()

	// Drain
	for range stream.Deltas() {
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after draining")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Message Constructors and Content Types
// ═══════════════════════════════════════════════════════════════════════

func TestMessageConstructors(t *testing.T) {
	sys := NewSystemMessage("system prompt")
	if sys.Role() != RoleSystem {
		t.Error("system role wrong")
	}
	if len(sys.Content) != 1 {
		t.Fatal("system content length != 1")
	}
	if tc, ok := sys.Content[0].(TextContent); !ok || tc.Text != "system prompt" {
		t.Error("system text wrong")
	}

	usr := NewUserMessage("user input")
	if usr.Role() != RoleUser {
		t.Error("user role wrong")
	}
	if tc, ok := usr.Content[0].(TextContent); !ok || tc.Text != "user input" {
		t.Error("user text wrong")
	}

	tr := NewToolResultMessage(
		ToolResultContent{ToolCallID: "tc-1", Text: "result"},
	)
	if tr.Role() != RoleSystem {
		t.Error("tool result message role should be system")
	}
	if trc, ok := tr.Content[0].(ToolResultContent); !ok || trc.ToolCallID != "tc-1" {
		t.Error("tool result content wrong")
	}

	utr := NewUserToolResultMessage(
		ToolResultContent{ToolCallID: "tc-2", Text: "user result"},
	)
	if utr.Role() != RoleUser {
		t.Error("user tool result role should be user")
	}
	if trc, ok := utr.Content[0].(ToolResultContent); !ok || trc.ToolCallID != "tc-2" {
		t.Error("user tool result content wrong")
	}
}

func TestToolResultContentInSystemMessage(t *testing.T) {
	msg := NewToolResultMessage(
		ToolResultContent{ToolCallID: "a", Text: "result-a"},
		ToolResultContent{ToolCallID: "b", Text: "result-b"},
	)

	if len(msg.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(msg.Content))
	}

	for i, c := range msg.Content {
		trc, ok := c.(ToolResultContent)
		if !ok {
			t.Fatalf("content[%d] not ToolResultContent", i)
		}
		if trc.ToolCallID == "" {
			t.Errorf("content[%d] has empty ToolCallID", i)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// WAL Integration
// ═══════════════════════════════════════════════════════════════════════

func TestWALMultipleTransactions(t *testing.T) {
	wal := NewInMemoryWAL()

	// Multiple committed transactions
	for i := 0; i < 5; i++ {
		txID, _ := wal.Begin()
		wal.Append(txID, TxOp{Kind: TxOpAddNode})
		wal.Commit(txID)
	}

	committed, _ := wal.Recover()
	if len(committed) != 5 {
		t.Errorf("committed = %d, want 5", len(committed))
	}
}

func TestWALAbortedNotRecovered(t *testing.T) {
	wal := NewInMemoryWAL()

	txID, _ := wal.Begin()
	wal.Append(txID, TxOp{Kind: TxOpAddNode})
	wal.Abort(txID)

	committed, _ := wal.Recover()
	if len(committed) != 0 {
		t.Errorf("committed = %d, want 0 (all aborted)", len(committed))
	}
}

func TestWALReplayNonexistent(t *testing.T) {
	wal := NewInMemoryWAL()
	_, err := wal.Replay("nonexistent-tx")
	if err == nil {
		t.Error("expected error for nonexistent tx")
	}
}

func TestTreeBranchWithWAL(t *testing.T) {
	wal := NewInMemoryWAL()
	tree, _ := NewTree(NewSystemMessage("sys"), WithWAL(wal))
	root := tree.Root()

	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))
	tree.Branch(user.ID, "alt", NewUserMessage("branch msg"))

	committed, _ := wal.Recover()
	// AddChild(hello) + Branch(branch msg) = 2 transactions
	if len(committed) != 2 {
		t.Errorf("committed = %d, want 2", len(committed))
	}
}

func TestTreeUpdateUserMessageWithWAL(t *testing.T) {
	wal := NewInMemoryWAL()
	tree, _ := NewTree(NewSystemMessage("sys"), WithWAL(wal))
	root := tree.Root()

	user, _ := tree.AddChild(root.ID, NewUserMessage("original"))
	tree.UpdateUserMessage(user.ID, NewUserMessage("edited"))

	committed, _ := wal.Recover()
	// AddChild + UpdateUserMessage = 2 transactions
	if len(committed) != 2 {
		t.Errorf("committed = %d, want 2", len(committed))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Concurrent Agent Operations
// ═══════════════════════════════════════════════════════════════════════

func TestConcurrentInvocationsOnDifferentBranches(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	user, _ := tree.AddChild(root.ID, NewUserMessage("shared"))
	asst, _ := tree.AddChild(user.ID, AssistantMessage{
		Content: []AssistantContent{TextContent{Text: "shared reply"}},
	})

	// Create multiple branches
	branches := make([]BranchID, 5)
	for i := range branches {
		bid, _, _ := tree.Branch(asst.ID, fmt.Sprintf("branch-%d", i), NewUserMessage(fmt.Sprintf("branch %d input", i)))
		branches[i] = bid
	}

	provider := &mockProvider{response: "branch response"}
	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tree:     tree,
	})

	// Invoke on all branches concurrently
	var wg sync.WaitGroup
	for _, b := range branches {
		wg.Add(1)
		go func(bid BranchID) {
			defer wg.Done()
			stream := agent.Invoke(context.Background(), []Message{}, bid)
			collectDeltas(stream)
			stream.Wait()
		}(b)
	}
	wg.Wait()

	// Each branch should have: sys + shared + shared reply + branch input + branch response = 5
	for _, b := range branches {
		msgs, err := tree.FlattenBranch(b)
		if err != nil {
			t.Errorf("FlattenBranch(%s): %v", b, err)
			continue
		}
		if len(msgs) != 5 {
			t.Errorf("branch %s messages = %d, want 5", b, len(msgs))
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// messagesToText
// ═══════════════════════════════════════════════════════════════════════

func TestMessagesToText(t *testing.T) {
	msgs := []Message{
		NewSystemMessage("system prompt"),
		NewUserMessage("user question"),
		AssistantMessage{Content: []AssistantContent{TextContent{Text: "assistant reply"}}},
		NewToolResultMessage(ToolResultContent{ToolCallID: "tc-1", Text: "tool output"}),
	}

	text := messagesToText(msgs)

	if !strings.Contains(text, "System: system prompt") {
		t.Error("missing system text")
	}
	if !strings.Contains(text, "User: user question") {
		t.Error("missing user text")
	}
	if !strings.Contains(text, "Assistant: assistant reply") {
		t.Error("missing assistant text")
	}
	if !strings.Contains(text, "Tool Result [tc-1]: tool output") {
		t.Error("missing tool result text")
	}
}

func TestMessagesToTextUserToolResult(t *testing.T) {
	msgs := []Message{
		NewUserToolResultMessage(ToolResultContent{ToolCallID: "tc-1", Text: "user tool"}),
	}

	text := messagesToText(msgs)
	if !strings.Contains(text, "Tool Result [tc-1]: user tool") {
		t.Error("missing user tool result text")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Edge Cases: Agent with empty/unusual inputs
// ═══════════════════════════════════════════════════════════════════════

func TestAgentInvokeNoInputMessages(t *testing.T) {
	provider := &mockProvider{response: "unprompted"}
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{})
	deltas := collectDeltas(stream)
	stream.Wait()

	got := textFromDeltas(deltas)
	if got != "unprompted" {
		t.Errorf("text = %q, want 'unprompted'", got)
	}

	// Tree should have: system + assistant
	msgs, _ := agent.Tree().FlattenBranch("main")
	if len(msgs) != 2 {
		t.Errorf("messages = %d, want 2", len(msgs))
	}
}

func TestAgentInvokeNilInputMessages(t *testing.T) {
	provider := &mockProvider{response: "hi"}
	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), nil)
	collectDeltas(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAgentEmptySystemPrompt(t *testing.T) {
	provider := &mockProvider{response: "hi"}
	agent := NewAgent(AgentConfig{
		Provider: provider,
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("hello")})
	collectDeltas(stream)
	stream.Wait()

	msgs, _ := agent.Tree().FlattenBranch("main")
	// Even with empty system prompt, root is a SystemMessage
	if msgs[0].Role() != RoleSystem {
		t.Error("first message should be system even with empty prompt")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Tree: Archive edge cases
// ═══════════════════════════════════════════════════════════════════════

func TestArchiveNonRecursive(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))
	asst, _ := tree.AddChild(user.ID, AssistantMessage{
		Content: []AssistantContent{TextContent{Text: "hi"}},
	})

	// Archive user non-recursively
	tree.Archive(user.ID, "test", false)

	// asst should still be active
	tree.mu.RLock()
	asstNode := tree.nodes[asst.ID]
	userNode := tree.nodes[user.ID]
	tree.mu.RUnlock()

	if userNode.State != NodeArchived {
		t.Error("user should be archived")
	}
	if asstNode.State != NodeActive {
		t.Error("asst should still be active (non-recursive)")
	}
}

func TestArchiveNodeNotFound(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))

	err := tree.Archive("nonexistent", "test", false)
	if err == nil {
		t.Error("expected error for nonexistent node")
	}
}

func TestRestoreNodeNotFound(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))

	err := tree.Restore("nonexistent", false)
	if err == nil {
		t.Error("expected error for nonexistent node")
	}
}

func TestArchiveVersionIncrement(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))

	tree.mu.RLock()
	initialVersion := tree.nodes[user.ID].Version
	tree.mu.RUnlock()

	tree.Archive(user.ID, "test", false)

	tree.mu.RLock()
	archivedVersion := tree.nodes[user.ID].Version
	tree.mu.RUnlock()

	if archivedVersion != initialVersion+1 {
		t.Errorf("version after archive = %d, want %d", archivedVersion, initialVersion+1)
	}

	tree.Restore(user.ID, false)

	tree.mu.RLock()
	restoredVersion := tree.nodes[user.ID].Version
	tree.mu.RUnlock()

	if restoredVersion != archivedVersion+1 {
		t.Errorf("version after restore = %d, want %d", restoredVersion, archivedVersion+1)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Tree: Deep branching and multi-branch operations
// ═══════════════════════════════════════════════════════════════════════

func TestDeepConversationTree(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	// Build a deep chain of 50 messages
	current := root
	for i := 0; i < 50; i++ {
		var msg Message
		if i%2 == 0 {
			msg = NewUserMessage(fmt.Sprintf("user-%d", i))
		} else {
			msg = AssistantMessage{Content: []AssistantContent{TextContent{Text: fmt.Sprintf("asst-%d", i)}}}
		}
		node, err := tree.AddChild(current.ID, msg)
		if err != nil {
			t.Fatalf("AddChild %d: %v", i, err)
		}
		current = node
	}

	// Flatten should return all 51 messages (root + 50)
	msgs, err := tree.FlattenBranch("main")
	if err != nil {
		t.Fatalf("FlattenBranch: %v", err)
	}
	if len(msgs) != 51 {
		t.Errorf("messages = %d, want 51", len(msgs))
	}

	// NodePath should have depth 50
	path, _ := tree.NodePath(current.ID)
	if len(path) != 50 {
		t.Errorf("path length = %d, want 50", len(path))
	}
}

func TestMultipleBranchesFromSameNode(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))

	// Create 5 branches from the same node
	branchIDs := make([]BranchID, 5)
	for i := range 5 {
		bid, _, err := tree.Branch(user.ID, fmt.Sprintf("branch-%d", i), NewUserMessage(fmt.Sprintf("alt-%d", i)))
		if err != nil {
			t.Fatalf("Branch %d: %v", i, err)
		}
		branchIDs[i] = bid
	}

	// Each branch should flatten independently
	for i, bid := range branchIDs {
		msgs, _ := tree.FlattenBranch(bid)
		// sys + hello + alt-i = 3
		if len(msgs) != 3 {
			t.Errorf("branch %d messages = %d, want 3", i, len(msgs))
		}
	}

	// Children of user should be 5 (the branch nodes)
	children, _ := tree.Children(user.ID)
	if len(children) != 5 {
		t.Errorf("children = %d, want 5", len(children))
	}
}

func TestBranchNameCollision(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()
	user, _ := tree.AddChild(root.ID, NewUserMessage("hello"))

	// Create two branches with the same name
	bid1, _, err := tree.Branch(user.ID, "same", NewUserMessage("first"))
	if err != nil {
		t.Fatalf("Branch 1: %v", err)
	}
	bid2, _, err := tree.Branch(user.ID, "same", NewUserMessage("second"))
	if err != nil {
		t.Fatalf("Branch 2: %v", err)
	}

	// They should have different IDs due to dedup suffix
	if bid1 == bid2 {
		t.Error("branch IDs should differ despite same name")
	}

	// Both should be valid branches
	branches := tree.Branches()
	if _, ok := branches[bid1]; !ok {
		t.Error("branch 1 not found")
	}
	if _, ok := branches[bid2]; !ok {
		t.Error("branch 2 not found")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Tree Compaction Integration
// ═══════════════════════════════════════════════════════════════════════

func TestTreeCompactChangesActiveBranch(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	current := root
	for i := 0; i < 10; i++ {
		var msg Message
		if i%2 == 0 {
			msg = NewUserMessage(fmt.Sprintf("user-%d", i))
		} else {
			msg = AssistantMessage{Content: []AssistantContent{TextContent{Text: fmt.Sprintf("asst-%d", i)}}}
		}
		node, _ := tree.AddChild(current.ID, msg)
		current = node
	}

	if tree.Active() != "main" {
		t.Fatal("active should be main before compaction")
	}

	provider := &mockProvider{response: "summary"}
	tokenizer := &mockTokenizer{tokensPerMessage: 100}

	newBranch, err := tree.Compact(context.Background(), "main", provider, tokenizer, CompactOpts{
		MaxTokens: 500,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if tree.Active() != newBranch {
		t.Errorf("active = %s, want %s (compacted branch)", tree.Active(), newBranch)
	}
	if newBranch == "main" {
		t.Error("compacted branch should be different from main")
	}
}

func TestTreeCompactOriginalBranchIntact(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	current := root
	for i := 0; i < 8; i++ {
		msg := NewUserMessage(fmt.Sprintf("msg-%d", i))
		node, _ := tree.AddChild(current.ID, msg)
		current = node
	}

	provider := &mockProvider{response: "summary"}
	tokenizer := &mockTokenizer{tokensPerMessage: 100}

	originalMsgs, _ := tree.FlattenBranch("main")
	originalCount := len(originalMsgs)

	tree.Compact(context.Background(), "main", provider, tokenizer, CompactOpts{MaxTokens: 200})

	afterMsgs, _ := tree.FlattenBranch("main")
	if len(afterMsgs) != originalCount {
		t.Errorf("original branch changed: %d -> %d", originalCount, len(afterMsgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Checkpoint + Rewind + Agent Invoke
// ═══════════════════════════════════════════════════════════════════════

func TestCheckpointRewindAndInvoke(t *testing.T) {
	provider := &mockProvider{response: "response"}

	tree, _ := NewTree(NewSystemMessage("sys"))
	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tree:     tree,
	})

	// First turn
	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("turn 1")})
	collectDeltas(stream)
	stream.Wait()

	// Checkpoint after turn 1 (tip is asst1)
	cpID, err := tree.Checkpoint("main", "after-turn-1")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Second turn on main
	stream = agent.Invoke(context.Background(), []Message{NewUserMessage("turn 2")})
	collectDeltas(stream)
	stream.Wait()

	// Verify main has both turns: sys + user1 + asst1 + user2 + asst2
	mainMsgs, _ := tree.FlattenBranch("main")
	if len(mainMsgs) != 5 {
		t.Fatalf("main messages = %d, want 5", len(mainMsgs))
	}

	// Rewind to after turn 1 — creates new branch at checkpoint tip (asst1)
	rewindBranch, err := tree.Rewind(cpID)
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}

	// Verify rewind branch starts at checkpoint: sys + user1 + asst1
	rewindMsgsBefore, _ := tree.FlattenBranch(rewindBranch)
	if len(rewindMsgsBefore) != 3 {
		t.Fatalf("rewind before invoke = %d, want 3", len(rewindMsgsBefore))
	}

	// To properly invoke on the rewound branch, use Branch() which creates
	// a proper branch with its own BranchID on the nodes. Rewind only sets
	// the branch pointer without changing node ownership, so AddChild would
	// advance the original branch instead. Use Branch from the checkpoint node.
	tip, _ := tree.Tip(rewindBranch)
	altBranch, _, err := tree.Branch(tip.ID, "alt-turn-2", NewUserMessage("alternate turn 2"))
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}

	stream = agent.Invoke(context.Background(), []Message{}, altBranch)
	collectDeltas(stream)
	stream.Wait()

	// Alt branch: sys + user1 + asst1 + alt_user2 + alt_asst2
	altMsgs, _ := tree.FlattenBranch(altBranch)
	if len(altMsgs) != 5 {
		t.Errorf("alt branch messages = %d, want 5", len(altMsgs))
	}

	// Main should be unchanged at 5
	mainMsgs2, _ := tree.FlattenBranch("main")
	if len(mainMsgs2) != 5 {
		t.Errorf("main messages after alt invoke = %d, want 5", len(mainMsgs2))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// UpdateUserMessage + Agent Invoke
// ═══════════════════════════════════════════════════════════════════════

func TestUpdateUserMessageAndInvoke(t *testing.T) {
	provider := &mockProvider{response: "response"}

	tree, _ := NewTree(NewSystemMessage("sys"))
	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tree:     tree,
	})

	// First turn
	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("original question")})
	collectDeltas(stream)
	stream.Wait()

	// Find the user node
	msgs, _ := tree.FlattenBranch("main")
	tip, _ := tree.Tip("main")
	path, _ := tree.Path(tip.ID)
	// path[1] should be the user node
	userNodeID := path[1]

	// Edit the user message
	editBranch, _, err := tree.UpdateUserMessage(userNodeID, NewUserMessage("edited question"))
	if err != nil {
		t.Fatalf("UpdateUserMessage: %v", err)
	}

	// Invoke on the edit branch
	stream = agent.Invoke(context.Background(), []Message{}, editBranch)
	collectDeltas(stream)
	stream.Wait()

	// Edit branch: sys + edited + response = 3
	editMsgs, _ := tree.FlattenBranch(editBranch)
	if len(editMsgs) != 3 {
		t.Errorf("edit messages = %d, want 3", len(editMsgs))
	}
	um := editMsgs[1].(UserMessage)
	tc := um.Content[0].(TextContent)
	if tc.Text != "edited question" {
		t.Errorf("edited text = %q", tc.Text)
	}

	// Original branch unchanged
	if len(msgs) != 3 {
		t.Errorf("original messages changed: %d", len(msgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent + Tree: Active cursor integration
// ═══════════════════════════════════════════════════════════════════════

func TestAgentRespectsSetActive(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	user, _ := tree.AddChild(root.ID, NewUserMessage("setup"))
	bid, _, _ := tree.Branch(user.ID, "alt", NewUserMessage("alt setup"))

	tree.SetActive(bid)

	provider := &mockProvider{response: "on alt branch"}
	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tree:     tree,
	})

	// Invoke without explicit branch
	stream := agent.Invoke(context.Background(), []Message{})
	collectDeltas(stream)
	stream.Wait()

	// Should have invoked on alt branch
	altMsgs, _ := tree.FlattenBranch(bid)
	// sys + setup + alt setup + response = 4
	if len(altMsgs) != 4 {
		t.Errorf("alt branch messages = %d, want 4", len(altMsgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Diff integration
// ═══════════════════════════════════════════════════════════════════════

func TestDiffAfterAgentInvoke(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	user, _ := tree.AddChild(root.ID, NewUserMessage("shared"))
	asst, _ := tree.AddChild(user.ID, AssistantMessage{
		Content: []AssistantContent{TextContent{Text: "shared reply"}},
	})

	// Branch
	bid, _, _ := tree.Branch(asst.ID, "alt", NewUserMessage("alt question"))

	// Invoke on both branches
	provider := &mockProvider{response: "reply"}
	agent := NewAgent(AgentConfig{Provider: provider, Tree: tree})

	s1 := agent.Invoke(context.Background(), []Message{NewUserMessage("main q")})
	collectDeltas(s1)
	s1.Wait()

	s2 := agent.Invoke(context.Background(), []Message{}, bid)
	collectDeltas(s2)
	s2.Wait()

	diff, err := tree.DiffBranches("main", bid)
	if err != nil {
		t.Fatalf("DiffBranches: %v", err)
	}

	if diff.CommonAncestor != asst.ID {
		t.Error("common ancestor should be the shared assistant node")
	}
	// Main has: main_q + main_reply = 2 unique nodes
	if len(diff.Removed) != 2 {
		t.Errorf("removed = %d, want 2", len(diff.Removed))
	}
	// Alt has: alt_question + alt_reply = 2 unique nodes
	if len(diff.Added) != 2 {
		t.Errorf("added = %d, want 2", len(diff.Added))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Full end-to-end scenario: multi-turn with tools, branching, compaction
// ═══════════════════════════════════════════════════════════════════════

func TestEndToEndScenario(t *testing.T) {
	callCount := 0
	searchTool := &ToolFunc{
		Def: ToolDef{Name: "search", Description: "search the web"},
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			callCount++
			q, _ := args["query"].(string)
			return fmt.Sprintf("search results for: %s", q), nil
		},
	}

	// Provider: turn 1 calls search, turn 2 responds with text
	provider := &sequenceProvider{
		responses: []func(ch chan<- Delta){
			func(ch chan<- Delta) {
				ch <- ToolCallStartDelta{ID: "tc-1", Name: "search"}
				ch <- ToolCallEndDelta{Arguments: map[string]any{"query": "golang testing"}}
			},
			func(ch chan<- Delta) {
				ch <- TextStartDelta{}
				ch <- TextContentDelta{Content: "Based on search: Go testing is great"}
				ch <- TextEndDelta{}
			},
			// Turn 2 (second user input, new invocation)
			func(ch chan<- Delta) {
				ch <- TextStartDelta{}
				ch <- TextContentDelta{Content: "You're welcome!"}
				ch <- TextEndDelta{}
			},
		},
	}

	tree, _ := NewTree(NewSystemMessage("You are a helpful assistant."))
	agent := NewAgent(AgentConfig{
		Provider: provider,
		Tools:    NewToolRegistry(searchTool),
		Tree:     tree,
	})

	// Turn 1: user asks a question → tool call → tool result → final response
	s1 := agent.Invoke(context.Background(), []Message{NewUserMessage("Tell me about Go testing")})
	d1 := collectDeltas(s1)
	s1.Wait()

	if callCount != 1 {
		t.Fatalf("search called %d times, want 1", callCount)
	}

	text1 := textFromDeltas(d1)
	if !strings.Contains(text1, "Go testing is great") {
		t.Errorf("turn 1 text = %q", text1)
	}

	// Verify tree state after turn 1
	msgs1, _ := tree.FlattenBranch("main")
	// sys + user + asst(tool call) + sys(tool result) + asst(text) = 5
	if len(msgs1) != 5 {
		t.Fatalf("turn 1 messages = %d, want 5", len(msgs1))
	}

	// Turn 2: follow-up (no tool call)
	s2 := agent.Invoke(context.Background(), []Message{NewUserMessage("Thanks!")})
	d2 := collectDeltas(s2)
	s2.Wait()

	text2 := textFromDeltas(d2)
	if text2 != "You're welcome!" {
		t.Errorf("turn 2 text = %q", text2)
	}

	// Verify full tree state
	msgs2, _ := tree.FlattenBranch("main")
	// Previous 5 + user("Thanks!") + asst("You're welcome!") = 7
	if len(msgs2) != 7 {
		t.Fatalf("turn 2 messages = %d, want 7", len(msgs2))
	}

	// Checkpoint and branch for "what if" scenario
	cpID, _ := tree.Checkpoint("main", "after-turn-2")

	// Get the user node via the tree path
	tip1, _ := tree.Tip("main")
	fullPath, _ := tree.Path(tip1.ID)
	userNodeID := fullPath[1] // user node

	editBranch, _, _ := tree.UpdateUserMessage(userNodeID, NewUserMessage("Tell me about Rust testing instead"))

	// The edit branch should have: sys + edited_user = 2
	editMsgs, _ := tree.FlattenBranch(editBranch)
	if len(editMsgs) != 2 {
		t.Errorf("edit branch messages = %d, want 2", len(editMsgs))
	}

	// Rewind to checkpoint
	rewindBranch, _ := tree.Rewind(cpID)
	rewindMsgs, _ := tree.FlattenBranch(rewindBranch)
	if len(rewindMsgs) != 7 { // same as main at checkpoint time
		t.Errorf("rewind branch messages = %d, want 7", len(rewindMsgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// ToolFunc adapter
// ═══════════════════════════════════════════════════════════════════════

func TestToolFuncDefinitionAndExecute(t *testing.T) {
	tf := &ToolFunc{
		Def: ToolDef{
			Name:        "calculator",
			Description: "does math",
			Parameters: ParameterSchema{
				Type:     "object",
				Required: []string{"expression"},
				Properties: map[string]PropertyDef{
					"expression": {Type: "string", Description: "math expression"},
				},
			},
		},
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			expr, _ := args["expression"].(string)
			return "result of " + expr, nil
		},
	}

	def := tf.Definition()
	if def.Name != "calculator" {
		t.Errorf("name = %s", def.Name)
	}
	if len(def.Parameters.Required) != 1 || def.Parameters.Required[0] != "expression" {
		t.Error("parameters wrong")
	}

	result, err := tf.Execute(context.Background(), map[string]any{"expression": "1+1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "result of 1+1" {
		t.Errorf("result = %q", result)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with tools that receive correct arguments
// ═══════════════════════════════════════════════════════════════════════

func TestToolReceivesCorrectArguments(t *testing.T) {
	var receivedArgs map[string]any

	tool := &ToolFunc{
		Def: ToolDef{Name: "echo", Description: "echo args"},
		Fn: func(_ context.Context, args map[string]any) (string, error) {
			receivedArgs = args
			return "echoed", nil
		},
	}

	expectedArgs := map[string]any{
		"message": "hello",
		"count":   float64(3),
	}

	provider := &toolCallProvider{
		toolName: "echo",
		toolID:   "call-1",
		toolArgs: expectedArgs,
		response: "Done.",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("echo")})
	collectDeltas(stream)
	stream.Wait()

	if receivedArgs["message"] != "hello" {
		t.Errorf("message = %v, want 'hello'", receivedArgs["message"])
	}
	if receivedArgs["count"] != float64(3) {
		t.Errorf("count = %v, want 3", receivedArgs["count"])
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with Provider that sends multi-chunk text
// ═══════════════════════════════════════════════════════════════════════

func TestAgentMultiChunkTextStreaming(t *testing.T) {
	provider := &sequenceProvider{
		responses: []func(ch chan<- Delta){
			func(ch chan<- Delta) {
				ch <- TextStartDelta{}
				ch <- TextContentDelta{Content: "Hello "}
				ch <- TextContentDelta{Content: "beautiful "}
				ch <- TextContentDelta{Content: "world"}
				ch <- TextEndDelta{}
			},
		},
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("Hi")})
	deltas := collectDeltas(stream)
	stream.Wait()

	text := textFromDeltas(deltas)
	if text != "Hello beautiful world" {
		t.Errorf("text = %q, want 'Hello beautiful world'", text)
	}

	// Tree should have the full aggregated message
	msgs, _ := agent.Tree().FlattenBranch("main")
	am := msgs[2].(AssistantMessage)
	tc := am.Content[0].(TextContent)
	if tc.Text != "Hello beautiful world" {
		t.Errorf("tree text = %q", tc.Text)
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with mixed text + tool call in single response
// ═══════════════════════════════════════════════════════════════════════

func TestAgentMixedTextAndToolCallResponse(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "lookup", Description: "lookup"},
		Fn: func(_ context.Context, _ map[string]any) (string, error) {
			return "looked up", nil
		},
	}

	provider := &sequenceProvider{
		responses: []func(ch chan<- Delta){
			func(ch chan<- Delta) {
				// Text first, then tool call
				ch <- TextStartDelta{}
				ch <- TextContentDelta{Content: "Let me search"}
				ch <- TextEndDelta{}
				ch <- ToolCallStartDelta{ID: "tc-1", Name: "lookup"}
				ch <- ToolCallEndDelta{Arguments: map[string]any{}}
			},
			func(ch chan<- Delta) {
				ch <- TextStartDelta{}
				ch <- TextContentDelta{Content: "Found it!"}
				ch <- TextEndDelta{}
			},
		},
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("search")})
	deltas := collectDeltas(stream)
	stream.Wait()

	// Should have both text and tool exec deltas
	texts := collectDeltasByType[TextContentDelta](deltas)
	execStarts := collectDeltasByType[ToolExecStartDelta](deltas)

	if len(texts) < 2 {
		t.Errorf("TextContentDelta count = %d, want >= 2", len(texts))
	}
	if len(execStarts) != 1 {
		t.Errorf("ToolExecStartDelta count = %d, want 1", len(execStarts))
	}

	// The assistant message in tree should have both text and tool use
	msgs, _ := agent.Tree().FlattenBranch("main")
	am := msgs[2].(AssistantMessage)
	if len(am.Content) != 2 {
		t.Fatalf("assistant content blocks = %d, want 2", len(am.Content))
	}
	if _, ok := am.Content[0].(TextContent); !ok {
		t.Error("first block should be TextContent")
	}
	if _, ok := am.Content[1].(ToolUseContent); !ok {
		t.Error("second block should be ToolUseContent")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Replay round-trip: Invoke → FlattenBranch → Replay → verify same deltas
// ═══════════════════════════════════════════════════════════════════════

func TestReplayRoundTrip(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "greet", Description: "greet"},
		Fn:  func(_ context.Context, _ map[string]any) (string, error) { return "greeted", nil },
	}

	provider := &toolCallProvider{
		toolName: "greet",
		toolID:   "tc-1",
		toolArgs: map[string]any{},
		response: "Done greeting",
	}

	agent := NewAgent(AgentConfig{
		Provider:     provider,
		SystemPrompt: "sys",
		Tools:        NewToolRegistry(tool),
	})

	// Invoke
	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("greet me")})
	collectDeltas(stream)
	stream.Wait()

	// Get messages from tree
	msgs, _ := agent.Tree().FlattenBranch("main")

	// Replay
	replayStream := Replay(msgs)
	replayDeltas := collectDeltas(replayStream)
	replayStream.Wait()

	// Should get assistant text and tool events from replay
	replayTexts := collectDeltasByType[TextContentDelta](replayDeltas)
	replayToolStarts := collectDeltasByType[ToolCallStartDelta](replayDeltas)
	replayExecStarts := collectDeltasByType[ToolExecStartDelta](replayDeltas)

	if len(replayTexts) != 1 || replayTexts[0].Content != "Done greeting" {
		t.Errorf("replay text = %v", replayTexts)
	}
	if len(replayToolStarts) != 1 || replayToolStarts[0].Name != "greet" {
		t.Errorf("replay tool starts = %v", replayToolStarts)
	}
	if len(replayExecStarts) != 1 {
		t.Errorf("replay exec starts = %d, want 1", len(replayExecStarts))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with SummarizeCompactor in a multi-turn scenario
// ═══════════════════════════════════════════════════════════════════════

func TestAgentWithSummarizeCompactor(t *testing.T) {
	callIdx := 0
	provider := &sequenceProvider{
		responses: make([]func(ch chan<- Delta), 20),
	}
	for i := range provider.responses {
		i := i
		provider.responses[i] = func(ch chan<- Delta) {
			ch <- TextStartDelta{}
			ch <- TextContentDelta{Content: fmt.Sprintf("response-%d", i)}
			ch <- TextEndDelta{}
		}
	}
	_ = callIdx

	tree, _ := NewTree(NewSystemMessage("You are helpful."))
	agent := NewAgent(AgentConfig{
		Provider:   provider,
		CompactCfg: &CompactConfig{Strategy: CompactSummarize, Threshold: 5},
		Tree:       tree,
	})

	// Multiple turns
	for i := 0; i < 4; i++ {
		stream := agent.Invoke(context.Background(), []Message{NewUserMessage(fmt.Sprintf("turn-%d", i))})
		collectDeltas(stream)
		stream.Wait()
	}

	// Tree should have all messages (compactor doesn't modify tree, only what's sent to provider)
	msgs, _ := tree.FlattenBranch("main")
	// sys + 4*(user + asst) = 9
	if len(msgs) != 9 {
		t.Errorf("tree messages = %d, want 9", len(msgs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// FlattenAnnotated with compacted nodes
// ═══════════════════════════════════════════════════════════════════════

func TestFlattenAnnotatedWithCompactedNodes(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	current := root
	for i := 0; i < 6; i++ {
		var msg Message
		if i%2 == 0 {
			msg = NewUserMessage(fmt.Sprintf("user-%d", i))
		} else {
			msg = AssistantMessage{Content: []AssistantContent{TextContent{Text: fmt.Sprintf("asst-%d", i)}}}
		}
		node, _ := tree.AddChild(current.ID, msg)
		current = node
	}

	provider := &mockProvider{response: "summary"}
	tokenizer := &mockTokenizer{tokensPerMessage: 100}

	newBranch, err := tree.Compact(context.Background(), "main", provider, tokenizer, CompactOpts{
		MaxTokens: 300,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	annotated, err := tree.FlattenBranchAnnotated(newBranch)
	if err != nil {
		t.Fatalf("FlattenBranchAnnotated: %v", err)
	}

	// Should have some messages (exact count depends on compaction)
	if len(annotated) == 0 {
		t.Error("annotated should not be empty")
	}

	// Each annotated message should have valid metadata
	for i, a := range annotated {
		if a.NodeID == "" {
			t.Errorf("annotated[%d] has empty NodeID", i)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Stress test: many concurrent operations on tree
// ═══════════════════════════════════════════════════════════════════════

func TestTreeConcurrentReadWrite(t *testing.T) {
	tree, _ := NewTree(NewSystemMessage("sys"))
	root := tree.Root()

	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// 10 writers adding children
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := tree.AddChild(root.ID, NewUserMessage(fmt.Sprintf("writer-%d", idx)))
			if err != nil {
				errCh <- fmt.Errorf("writer %d: %w", idx, err)
			}
		}(i)
	}

	// 10 readers flattening
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tree.FlattenBranch("main")
			if err != nil {
				errCh <- err
			}
		}()
	}

	// 5 readers getting children
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tree.Children(root.ID)
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	// All writers should have succeeded
	children, _ := tree.Children(root.ID)
	if len(children) != 10 {
		t.Errorf("children = %d, want 10", len(children))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// SlidingWindowCompactor edge cases
// ═══════════════════════════════════════════════════════════════════════

func TestSlidingWindowExactlyAtBoundary(t *testing.T) {
	compactor := NewSlidingWindowCompactor(3)

	// Exactly WindowSize+1 = 4 messages → no compaction
	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
		NewUserMessage("three"),
	}

	result, _ := compactor.Compact(context.Background(), msgs, nil)
	if len(result) != 4 {
		t.Errorf("messages = %d, want 4 (no compaction at boundary)", len(result))
	}
}

func TestSlidingWindowOneOverBoundary(t *testing.T) {
	compactor := NewSlidingWindowCompactor(3)

	// WindowSize+2 = 5 messages → compaction
	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
		NewUserMessage("three"),
		NewUserMessage("four"),
	}

	result, _ := compactor.Compact(context.Background(), msgs, nil)
	// Should keep system + last 3 = 4
	if len(result) != 4 {
		t.Errorf("messages = %d, want 4", len(result))
	}
	if result[0].Role() != RoleSystem {
		t.Error("first should be system")
	}
}

func TestSlidingWindowPreservesSystem(t *testing.T) {
	compactor := NewSlidingWindowCompactor(2)

	msgs := []Message{
		NewSystemMessage("important system prompt"),
		NewUserMessage("old 1"),
		NewUserMessage("old 2"),
		NewUserMessage("old 3"),
		NewUserMessage("recent 1"),
		NewUserMessage("recent 2"),
	}

	result, _ := compactor.Compact(context.Background(), msgs, nil)
	// system + last 2 = 3
	if len(result) != 3 {
		t.Fatalf("messages = %d, want 3", len(result))
	}

	sm := result[0].(SystemMessage)
	tc := sm.Content[0].(TextContent)
	if tc.Text != "important system prompt" {
		t.Error("system prompt not preserved")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// SummarizeCompactor edge cases
// ═══════════════════════════════════════════════════════════════════════

func TestSummarizeCompactorFewMessages(t *testing.T) {
	// When there are very few messages (threshold < total, but keepLast covers almost all)
	compactor := NewSummarizeCompactor(2)
	provider := &mockProvider{response: "summary"}

	msgs := []Message{
		NewSystemMessage("sys"),
		NewUserMessage("one"),
		NewUserMessage("two"),
	}

	result, err := compactor.Compact(context.Background(), msgs, provider)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// keepLast = min(4, 2) = 2, toSummarize = msgs[1:1] = empty → return original
	if len(result) != 3 {
		t.Errorf("messages = %d, want 3 (empty toSummarize)", len(result))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Delta type verification
// ═══════════════════════════════════════════════════════════════════════

func TestAllDeltaTypesImplementInterface(t *testing.T) {
	// Verify all delta types satisfy the Delta interface
	deltas := []Delta{
		TextStartDelta{},
		TextContentDelta{Content: "test"},
		TextEndDelta{},
		ToolCallStartDelta{ID: "1", Name: "tool"},
		ToolCallArgumentDelta{Content: "{}"},
		ToolCallEndDelta{Arguments: map[string]any{}},
		ToolExecStartDelta{ToolCallID: "1", Name: "tool"},
		ToolExecDelta{ToolCallID: "1", Inner: TextContentDelta{Content: "inner"}},
		ToolExecEndDelta{ToolCallID: "1", Result: "ok"},
		ErrorDelta{Error: errors.New("err")},
		DoneDelta{},
	}

	for i, d := range deltas {
		if d == nil {
			t.Errorf("delta[%d] is nil", i)
		}
		// Just verify the interface is satisfied (compile-time check effectively)
		d.isDelta()
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Content type verification
// ═══════════════════════════════════════════════════════════════════════

func TestContentTypeRoleConstraints(t *testing.T) {
	// TextContent is valid in all roles
	var _ SystemContent = TextContent{Text: "hi"}
	var _ UserContent = TextContent{Text: "hi"}
	var _ AssistantContent = TextContent{Text: "hi"}

	// ToolUseContent is only for assistant
	var _ AssistantContent = ToolUseContent{ID: "1", Name: "tool"}

	// ToolResultContent is for system and user
	var _ SystemContent = ToolResultContent{ToolCallID: "1", Text: "result"}
	var _ UserContent = ToolResultContent{ToolCallID: "1", Text: "result"}
}

// ═══════════════════════════════════════════════════════════════════════
// NewID uniqueness
// ═══════════════════════════════════════════════════════════════════════

func TestNewIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with nil tools creates empty registry
// ═══════════════════════════════════════════════════════════════════════

func TestAgentNilToolsCreatesEmptyRegistry(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Provider:     &mockProvider{response: "hi"},
		SystemPrompt: "sys",
		Tools:        nil,
	})

	defs := agent.tools.Definitions()
	if len(defs) != 0 {
		t.Errorf("definitions = %d, want 0", len(defs))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Agent with tools passed as Tools field
// ═══════════════════════════════════════════════════════════════════════

func TestAgentWithToolsField(t *testing.T) {
	tool := &ToolFunc{
		Def: ToolDef{Name: "custom", Description: "custom tool"},
		Fn:  func(_ context.Context, _ map[string]any) (string, error) { return "custom result", nil },
	}

	reg := NewToolRegistry(tool)
	agent := NewAgent(AgentConfig{
		Provider:     &mockProvider{response: "hi"},
		SystemPrompt: "sys",
		Tools:        reg,
	})

	_, found := agent.tools.Get("custom")
	if !found {
		t.Error("custom tool not found in agent's registry")
	}
}

// ═══════════════════════════════════════════════════════════════════════
// SubAgent MaxIter respected
// ═══════════════════════════════════════════════════════════════════════

func TestSubAgentMaxIterRespected(t *testing.T) {
	// Child provider always wants to call a tool
	childProvider := &sequenceProvider{
		responses: make([]func(ch chan<- Delta), 100),
	}
	for i := range childProvider.responses {
		childProvider.responses[i] = func(ch chan<- Delta) {
			ch <- ToolCallStartDelta{ID: "call", Name: "child_tool"}
			ch <- ToolCallEndDelta{Arguments: map[string]any{}}
		}
	}

	childTool := &ToolFunc{
		Def: ToolDef{Name: "child_tool", Description: "child tool"},
		Fn:  func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
	}

	parentProvider := &toolCallProvider{
		toolName: "delegate_to_child",
		toolID:   "parent-call",
		toolArgs: map[string]any{"task": "loop forever"},
		response: "parent done",
	}

	agent := NewAgent(AgentConfig{
		Provider:     parentProvider,
		SystemPrompt: "parent",
		SubAgents: []SubAgentDef{
			{
				Name:         "child",
				Description:  "child that loops",
				SystemPrompt: "child",
				Provider:     childProvider,
				Tools:        NewToolRegistry(childTool),
				MaxIter:      2, // limit child iterations
			},
		},
	})

	stream := agent.Invoke(context.Background(), []Message{NewUserMessage("go")})

	done := make(chan struct{})
	go func() {
		collectDeltas(stream)
		stream.Wait()
		close(done)
	}()

	select {
	case <-done:
		// good — child respected MaxIter
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not complete — child MaxIter not respected")
	}
}
