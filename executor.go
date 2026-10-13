package main

import (
	"context"
	"errors"
	"fmt"
)

// SubmitRequest is one video request, already validated by the API layer.
type SubmitRequest struct {
	Prompt   string
	Ratio    string
	Model    string
	Duration int
	// ReferenceImages are optional source images for image-to-video.
	ReferenceImages [][]byte
}

// Executor turns a request into an upstream conversation id.
//
// It is an interface because the submit half of the pipeline is the one part
// that cannot be done with a plain HTTP client today: the page signs its submit
// with an a_bogus parameter, and producing a valid one is unsolved. Everything
// after the submit (polling, retrieval) needs neither a browser nor a
// signature, so the split lets the working half ship while the other is
// settled.
//
// The measured behaviour of the gate, for whoever replaces this: a submit with
// no a_bogus answers 710022004 (slide captcha), a submit with any well-formed
// a_bogus answers 710022002, and a garbage signature is indistinguishable from a
// valid one. So the layer consulted first only checks that something shaped
// like an a_bogus is present. If a later layer turns out to validate it, only
// the implementation below has to change.
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
