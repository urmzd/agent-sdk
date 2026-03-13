package fallback

import (
	"context"

	"github.com/urmzd/agent-sdk/core"
)

// Provider tries providers in order, falling back on failure.
// By default it falls back on any error. Set FallbackOn to control
// which errors trigger fallback (e.g. core.IsTransient for transient-only).
type Provider struct {
	Providers  []core.Provider
	FallbackOn func(error) bool // nil = fallback on any error
}

// New creates a provider that tries each in order.
func New(providers ...core.Provider) *Provider {
	return &Provider{Providers: providers}
}

func (f *Provider) Name() string { return "fallback" }

func (f *Provider) ChatStream(ctx context.Context, messages []core.Message, tools []core.ToolDef) (<-chan core.Delta, error) {
	shouldFallback := f.FallbackOn
	if shouldFallback == nil {
		shouldFallback = func(error) bool { return true }
	}

	var errs []error
	for _, p := range f.Providers {
		ch, err := p.ChatStream(ctx, messages, tools)
		if err == nil {
			return ch, nil
		}
		errs = append(errs, err)

		if ctx.Err() != nil {
			break
		}
		if !shouldFallback(err) {
			break
		}
	}

	return nil, &core.FallbackError{Errors: errs}
}
