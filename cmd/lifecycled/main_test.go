package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/buildkite/lifecycled"
	"github.com/sirupsen/logrus"
)

// fakeNotice is a TerminationNotice whose Handle returns a preset error, ignoring
// the handler, so handleNotice's exit-code wiring can be tested. It mirrors the
// real Handle contract: a handler error observed while ctx is cancelled is marked
// ErrDrainInterrupted.
type fakeNotice struct{ err error }

func (fakeNotice) Type() string { return "fake" }

func (n fakeNotice) Handle(ctx context.Context, _ lifecycled.Handler, _ *logrus.Entry) error {
	if n.err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w: %w", lifecycled.ErrDrainInterrupted, n.err)
	}
	return n.err
}

func TestHandleNotice(t *testing.T) {
	handlerErr := errors.New("handler failed")
	tests := []struct {
		name      string
		noticeErr error
		cancel    bool
		wantErr   bool
	}{
		{"success returns nil", nil, false, false},
		{"failure returns error for non-zero exit", handlerErr, false, true},
		// A drain cancelled by our own shutdown is clean, so it must not exit non-zero.
		{"shutdown-cancelled drain returns nil", handlerErr, true, false},
	}

	logger := logrus.New()
	logger.Out = io.Discard

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}

			err := handleNotice(ctx, fakeNotice{err: tc.noticeErr}, nil, logrus.NewEntry(logger))
			if tc.wantErr {
				if !errors.Is(err, handlerErr) {
					t.Errorf("handleNotice() = %v, want %v", err, handlerErr)
				}
			} else if err != nil {
				t.Errorf("handleNotice() = %v, want nil", err)
			}
		})
	}
}

func TestClassifyDrain(t *testing.T) {
	handlerErr := errors.New("handler failed")
	tests := []struct {
		name       string
		handlerErr error
		want       drainOutcome
	}{
		{"success", nil, drainSucceeded},
		{"failure", handlerErr, drainFailed},
		// Only an error Handle marked as interrupted is a clean shutdown; a bare
		// handler error, even one wrapping context.Canceled, is a genuine failure.
		{"unmarked cancellation is failure", fmt.Errorf("%w: %w", context.Canceled, handlerErr), drainFailed},
		{"interrupted by shutdown", fmt.Errorf("%w: %w", lifecycled.ErrDrainInterrupted, handlerErr), drainInterrupted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDrain(tc.handlerErr); got != tc.want {
				t.Errorf("classifyDrain(%v) = %d, want %d", tc.handlerErr, got, tc.want)
			}
		})
	}
}
