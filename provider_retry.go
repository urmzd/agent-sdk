package agentsdk

import (
	"context"
	"math"
	"time"
)

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxAttempts int           // total attempts (1 = no retry)
	BaseDelay   time.Duration // initial delay between retries
	MaxDelay    time.Duration // cap on delay
	Multiplier  float64       // backoff multiplier (default 2.0)
	ShouldRetry func(error) bool // nil = retry on IsTransient errors
}

// DefaultRetryConfig returns sensible defaults: 3 attempts, 500ms base,
// 10s cap, 2x exponential backoff, transient-only.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Multiplier:  2.0,
	}
}

// RetryProvider wraps a Provider with retry logic and exponential backoff.
type RetryProvider struct {
	Inner  Provider
	Config RetryConfig
}

// NewRetryProvider wraps a provider with the given retry config.
func NewRetryProvider(inner Provider, cfg RetryConfig) *RetryProvider {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.Multiplier <= 0 {
		cfg.Multiplier = 2.0
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 500 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 10 * time.Second
	}
	return &RetryProvider{Inner: inner, Config: cfg}
}

func (r *RetryProvider) Name() string {
	return "retry(" + providerName(r.Inner) + ")"
}

func (r *RetryProvider) ChatStream(ctx context.Context, messages []Message, tools []ToolDef, model string) (<-chan Delta, error) {
	shouldRetry := r.Config.ShouldRetry
	if shouldRetry == nil {
		shouldRetry = IsTransient
	}

	var lastErr error
	for attempt := range r.Config.MaxAttempts {
		ch, err := r.Inner.ChatStream(ctx, messages, tools, model)
		if err == nil {
			return ch, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return nil, lastErr
		}
		if !shouldRetry(err) {
			return nil, lastErr
		}

		// Backoff before next attempt (skip after last attempt).
		if attempt < r.Config.MaxAttempts-1 {
			delay := time.Duration(float64(r.Config.BaseDelay) * math.Pow(r.Config.Multiplier, float64(attempt)))
			if delay > r.Config.MaxDelay {
				delay = r.Config.MaxDelay
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, &RetryError{Attempts: r.Config.MaxAttempts, Last: lastErr}
}
