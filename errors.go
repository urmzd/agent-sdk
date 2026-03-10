package agentsdk

import "errors"

var (
	ErrToolNotFound    = errors.New("tool not found")
	ErrMaxIterations   = errors.New("max iterations reached")
	ErrStreamCancelled = errors.New("stream cancelled")
	ErrProviderFailed  = errors.New("provider failed")
)
