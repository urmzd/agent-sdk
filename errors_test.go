package agentsdk

import (
	"errors"
	"testing"
)

func TestProviderError_Is(t *testing.T) {
	err := &ProviderError{Provider: "test", Kind: ErrorKindTransient, Err: errors.New("timeout")}
	if !errors.Is(err, ErrProviderFailed) {
		t.Error("ProviderError should match ErrProviderFailed")
	}
}

func TestProviderError_Unwrap(t *testing.T) {
	inner := errors.New("connection refused")
	err := &ProviderError{Provider: "test", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("should unwrap to inner error")
	}
}

func TestProviderError_ErrorString(t *testing.T) {
	err := &ProviderError{Provider: "ollama", Model: "llama3", Code: 500, Err: errors.New("server error")}
	s := err.Error()
	if s != "provider ollama (model llama3, status 500): server error" {
		t.Errorf("Error() = %q", s)
	}

	err2 := &ProviderError{Provider: "ollama", Model: "llama3", Err: errors.New("timeout")}
	s2 := err2.Error()
	if s2 != "provider ollama (model llama3): timeout" {
		t.Errorf("Error() = %q", s2)
	}
}

func TestFallbackError_Is(t *testing.T) {
	err := &FallbackError{Errors: []error{errors.New("a"), errors.New("b")}}
	if !errors.Is(err, ErrProviderFailed) {
		t.Error("FallbackError should match ErrProviderFailed")
	}
}

func TestFallbackError_Unwrap(t *testing.T) {
	inner := errors.New("specific")
	err := &FallbackError{Errors: []error{inner, errors.New("other")}}
	if !errors.Is(err, inner) {
		t.Error("FallbackError should unwrap to find inner errors")
	}
}

func TestRetryError_Unwrap(t *testing.T) {
	inner := &ProviderError{Provider: "test", Kind: ErrorKindTransient, Err: errors.New("timeout")}
	err := &RetryError{Attempts: 3, Last: inner}
	if !errors.Is(err, ErrProviderFailed) {
		t.Error("RetryError should unwrap through ProviderError to match ErrProviderFailed")
	}
}

func TestIsTransient(t *testing.T) {
	transient := &ProviderError{Kind: ErrorKindTransient, Err: errors.New("timeout")}
	permanent := &ProviderError{Kind: ErrorKindPermanent, Err: errors.New("unauthorized")}
	plain := errors.New("something")

	if !IsTransient(transient) {
		t.Error("expected transient")
	}
	if IsTransient(permanent) {
		t.Error("expected not transient")
	}
	if IsTransient(plain) {
		t.Error("expected not transient for plain error")
	}
}

func TestIsTransient_FallbackError(t *testing.T) {
	transient := &ProviderError{Kind: ErrorKindTransient, Err: errors.New("timeout")}
	fe := &FallbackError{Errors: []error{transient}}
	if !IsTransient(fe) {
		t.Error("FallbackError with transient last error should be transient")
	}

	permanent := &ProviderError{Kind: ErrorKindPermanent, Err: errors.New("auth")}
	fe2 := &FallbackError{Errors: []error{permanent}}
	if IsTransient(fe2) {
		t.Error("FallbackError with permanent last error should not be transient")
	}
}

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		code int
		want ErrorKind
	}{
		{200, ErrorKindPermanent},
		{400, ErrorKindPermanent},
		{401, ErrorKindPermanent},
		{403, ErrorKindPermanent},
		{404, ErrorKindPermanent},
		{408, ErrorKindTransient},
		{429, ErrorKindTransient},
		{500, ErrorKindTransient},
		{502, ErrorKindTransient},
		{503, ErrorKindTransient},
	}
	for _, tt := range tests {
		got := ClassifyHTTPStatus(tt.code)
		if got != tt.want {
			t.Errorf("ClassifyHTTPStatus(%d) = %d, want %d", tt.code, got, tt.want)
		}
	}
}
