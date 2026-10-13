package main

import (
	"context"
	"errors"
	"fmt"
)

// SubmitRequest lives in dola.go, next to the code that renders it onto the
// wire, because the phrasing of the prompt is what selects the output.

// Executor turns a request into an upstream conversation id.
//
// It is an interface because the submit half of the pipeline was for a long time
// the part that could not be done with a plain HTTP client: the page signs its
// submit with an a_bogus parameter. That turned out to be a red herring —
// measured on the live endpoint, a submit with no a_bogus is accepted, while one
// carrying a well-formed but meaningless a_bogus is refused with 710022002, the
// same code an invalid signature gets. The parameter is only validated when it
// is present, so the way to satisfy it is to omit it. The interface stays
// because a second implementation is still plausible (a sidecar, or a different
// upstream), not because this one is a placeholder.
type Executor interface {
	// Name identifies the implementation in logs and /health.
	Name() string
	// Submit performs the upstream request and returns the conversation id.
	Submit(ctx context.Context, cookie string, req SubmitRequest) (string, error)
}

// ErrExecutorUnavailable means the configured executor cannot submit.
var ErrExecutorUnavailable = errors.New("executor: submit is unavailable")

// NewExecutor builds the executor named by DOLA_EXECUTOR.
func NewExecutor(cfg Config, upstream *Upstream) (Executor, error) {
	switch cfg.Executor {
	case "protocol":
		if upstream == nil {
			return nil, errors.New("executor: protocol needs an upstream client")
		}
		return &protocolExecutor{upstream: upstream}, nil
	case "sidecar":
		// The sidecar path was rejected for this deployment: it puts a browser
		// on a PC, which is exactly what the port exists to avoid.
		return nil, fmt.Errorf("executor: %q is not implemented in this build", cfg.Executor)
	default:
		return nil, fmt.Errorf("executor: unknown executor %q", cfg.Executor)
	}
}

// protocolExecutor submits over plain HTTP.
type protocolExecutor struct {
	upstream *Upstream
}

func (e *protocolExecutor) Name() string { return "protocol" }

func (e *protocolExecutor) Submit(ctx context.Context, cookie string, req SubmitRequest) (string, error) {
	return e.upstream.Submit(ctx, cookie, req)
}
