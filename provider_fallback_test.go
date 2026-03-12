package agentsdk

import (
	"context"
	"errors"
	"testing"
)

func TestFallbackProvider_FirstSucceeds(t *testing.T) {
	p1 := &mockProvider{response: "from-primary"}
	p2 := &mockProvider{response: "from-backup"}

	fb := NewFallbackProvider(p1, p2)
	ch, err := fb.ChatStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var text string
	for d := range ch {
		if tc, ok := d.(TextContentDelta); ok {
			text += tc.Content
		}
	}
	if text != "from-primary" {
		t.Errorf("got %q, want %q", text, "from-primary")
	}
}

func TestFallbackProvider_FallsBackOnError(t *testing.T) {
	failing := &errorProviderSimple{err: &ProviderError{
		Provider: "bad",
		Kind:     ErrorKindTransient,
		Err:      errors.New("connection refused"),
	}}
	good := &mockProvider{response: "from-backup"}

	fb := NewFallbackProvider(failing, good)
	ch, err := fb.ChatStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var text string
	for d := range ch {
		if tc, ok := d.(TextContentDelta); ok {
			text += tc.Content
		}
	}
	if text != "from-backup" {
		t.Errorf("got %q, want %q", text, "from-backup")
	}
}

func TestFallbackProvider_AllFail(t *testing.T) {
	p1 := &errorProviderSimple{err: &ProviderError{Provider: "a", Kind: ErrorKindTransient, Err: errors.New("fail-a")}}
	p2 := &errorProviderSimple{err: &ProviderError{Provider: "b", Kind: ErrorKindTransient, Err: errors.New("fail-b")}}

	fb := NewFallbackProvider(p1, p2)
	_, err := fb.ChatStream(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	var fe *FallbackError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FallbackError, got %T", err)
	}
	if len(fe.Errors) != 2 {
		t.Errorf("errors = %d, want 2", len(fe.Errors))
	}
	if !errors.Is(err, ErrProviderFailed) {
		t.Error("FallbackError should match ErrProviderFailed")
	}
}

func TestFallbackProvider_StopsOnPermanentWhenConfigured(t *testing.T) {
	perm := &errorProviderSimple{err: &ProviderError{Provider: "auth-fail", Kind: ErrorKindPermanent, Err: errors.New("unauthorized")}}
	good := &mockProvider{response: "should not reach"}

	fb := &FallbackProvider{
		Providers:  []Provider{perm, good},
		FallbackOn: IsTransient, // only fallback on transient
	}

	_, err := fb.ChatStream(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	var fe *FallbackError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FallbackError, got %T", err)
	}
	if len(fe.Errors) != 1 {
		t.Errorf("errors = %d, want 1 (should not have tried second provider)", len(fe.Errors))
	}
}

func TestFallbackProvider_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p1 := &errorProviderSimple{err: &ProviderError{Provider: "a", Kind: ErrorKindTransient, Err: errors.New("fail")}}
	p2 := &mockProvider{response: "should not reach"}

	fb := NewFallbackProvider(p1, p2)
	_, err := fb.ChatStream(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	var fe *FallbackError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FallbackError, got %T", err)
	}
	if len(fe.Errors) != 1 {
		t.Errorf("errors = %d, want 1 (should stop after context cancel)", len(fe.Errors))
	}
}

func TestFallbackProvider_Name(t *testing.T) {
	fb := NewFallbackProvider()
	if fb.Name() != "fallback" {
		t.Errorf("Name() = %q, want %q", fb.Name(), "fallback")
	}
}

// errorProviderSimple returns a fixed error (used in provider tests).
type errorProviderSimple struct {
	err error
}

func (p *errorProviderSimple) ChatStream(_ context.Context, _ []Message, _ []ToolDef) (<-chan Delta, error) {
	return nil, p.err
}
