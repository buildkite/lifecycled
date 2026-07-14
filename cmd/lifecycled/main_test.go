package main

import (
	"context"
	"errors"
	"testing"
)

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
