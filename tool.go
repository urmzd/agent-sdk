package agentsdk

import (
	"context"
	"fmt"
)

// ToolDef describes a tool's schema for the LLM.
type ToolDef struct {
	Name        string
	Description string
	Parameters  ParameterSchema
}

// ParameterSchema is a JSON-Schema-like definition for tool parameters.
type ParameterSchema struct {
	Type       string
	Required   []string
	Properties map[string]PropertyDef
}

// PropertyDef describes a single parameter property.
type PropertyDef struct {
	Type        string
	Description string
}

// Tool is the base interface all tools implement.
type Tool interface {
	Definition() ToolDef
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// ToolFunc adapts a plain function into a Tool.
type ToolFunc struct {
	Def ToolDef
	Fn  func(ctx context.Context, args map[string]any) (string, error)
}

func (t *ToolFunc) Definition() ToolDef {
	return t.Def
}

func (t *ToolFunc) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.Fn(ctx, args)
}

// subAgentTool wraps a sub-agent as a tool. It implements both Tool and
// SubAgentInvoker so the agent loop can forward child deltas.
type subAgentTool struct {
	def     ToolDef
	factory func() *Agent
}

func (t *subAgentTool) Definition() ToolDef { return t.def }

// Execute provides a blocking fallback — runs the child agent and returns
// the concatenated text. The agent loop prefers InvokeAgent for streaming.
func (t *subAgentTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	task, _ := args["task"].(string)
	stream := t.InvokeAgent(ctx, task)
	var result string
	for d := range stream.Deltas() {
		if tc, ok := d.(TextContentDelta); ok {
			result += tc.Content
		}
	}
	return result, stream.Wait()
}

// InvokeAgent creates a fresh child agent and invokes it, returning its stream.
func (t *subAgentTool) InvokeAgent(ctx context.Context, task string) *EventStream {
	child := t.factory()
	return child.Invoke(ctx, []Message{NewUserMessage(task)})
}

// ToolRegistry holds named tools.
type ToolRegistry struct {
	tools map[string]Tool
}

// NewToolRegistry creates a registry from the given tools.
func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Definition().Name] = t
	}
	return r
}

// Get returns a tool by name.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Register adds a tool to the registry.
func (r *ToolRegistry) Register(t Tool) {
	r.tools[t.Definition().Name] = t
}

// Definitions returns all tool definitions.
func (r *ToolRegistry) Definitions() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Execute runs a tool by name.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
	return t.Execute(ctx, args)
}
