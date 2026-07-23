package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/buildkite/lifecycled"
	"github.com/sirupsen/logrus"
)

// fakeNotice is a TerminationNotice whose Handle returns a preset error,
// ignoring the handler, so handleNotice's exit-code wiring can be tested.
type fakeNotice struct{ err error }

func (fakeNotice) Type() string { return "fake" }

func (n fakeNotice) Handle(context.Context, lifecycled.Handler, *logrus.Entry) error {
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
		ctxErr     error
		want       drainOutcome
	}{
		{"success", nil, nil, drainSucceeded},
		// A handler that returns nil is a success even if ctx was cancelled.
		{"success despite cancellation", nil, context.Canceled, drainSucceeded},
		{"failure", handlerErr, nil, drainFailed},
		{"interrupted by shutdown", handlerErr, context.Canceled, drainInterrupted},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDrain(tc.handlerErr, tc.ctxErr); got != tc.want {
				t.Errorf("classifyDrain(%v, %v) = %d, want %d", tc.handlerErr, tc.ctxErr, got, tc.want)
			}
		})
	}
}
