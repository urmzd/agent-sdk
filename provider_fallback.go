package agentsdk

import "context"

// FallbackProvider tries providers in order, falling back on failure.
// By default it falls back on any error. Set FallbackOn to control
// which errors trigger fallback (e.g. IsTransient for transient-only).
type FallbackProvider struct {
	Providers  []Provider
	FallbackOn func(error) bool // nil = fallback on any error
}

// NewFallbackProvider creates a provider that tries each in order.
func NewFallbackProvider(providers ...Provider) *FallbackProvider {
	return &FallbackProvider{Providers: providers}
}

func (f *FallbackProvider) Name() string { return "fallback" }

func (f *FallbackProvider) ChatStream(ctx context.Context, messages []Message, tools []ToolDef) (<-chan Delta, error) {
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

	return nil, &FallbackError{Errors: errs}
}
