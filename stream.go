package agentsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// EventStream is the consumer handle for streaming agent deltas.
type EventStream struct {
	deltas chan Delta
	done   chan struct{}
	err    error
	cancel context.CancelFunc
	once   sync.Once
}

func newEventStream(cancel context.CancelFunc) *EventStream {
	return &EventStream{
		deltas: make(chan Delta, 128),
		done:   make(chan struct{}),
		cancel: cancel,
	}
}

// Deltas returns a channel that yields deltas. Closed on completion.
func (s *EventStream) Deltas() <-chan Delta {
	return s.deltas
}

// Wait blocks until the stream is done and returns any error.
func (s *EventStream) Wait() error {
	<-s.done
	return s.err
}

// Cancel stops the stream.
func (s *EventStream) Cancel() {
	s.once.Do(func() {
		s.cancel()
	})
}

func (s *EventStream) send(d Delta) {
	select {
	case s.deltas <- d:
	default:
	}
}

func (s *EventStream) close(err error) {
	s.err = err
	close(s.deltas)
	close(s.done)
}

// ── SSE helper ──────────────────────────────────────────────────────

// sseEvent matches Zoro's SSE JSON format.
type sseEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// WriteSSE bridges an EventStream to an HTTP response as SSE.
// Converts typed deltas to Zoro-compatible SSE JSON events.
func WriteSSE(w http.ResponseWriter, flusher http.Flusher, stream *EventStream) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for delta := range stream.Deltas() {
		evt := deltaToSSE(delta)
		if evt == nil {
			continue
		}
		data, err := json.Marshal(evt)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	return stream.err
}

func deltaToSSE(d Delta) *sseEvent {
	switch v := d.(type) {
	case TextContentDelta:
		return &sseEvent{Type: "text_delta", Data: map[string]string{"content": v.Content}}
	case ToolCallStartDelta:
		return &sseEvent{Type: "tool_call_start", Data: map[string]string{"id": v.ID, "name": v.Name}}
	case ToolCallEndDelta:
		return &sseEvent{Type: "tool_call_result", Data: map[string]any{"id": "", "result": v.Arguments}}
	case ErrorDelta:
		msg := "unknown error"
		if v.Error != nil {
			msg = v.Error.Error()
		}
		return &sseEvent{Type: "error", Data: map[string]string{"message": msg}}
	case DoneDelta:
		return &sseEvent{Type: "done", Data: nil}
	default:
		return nil
	}
}
