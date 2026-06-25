package main

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
)

func TestResolveRegion(t *testing.T) {
	sentinel := errors.New("metadata unavailable")
	tests := []struct {
		name       string
		region     string
		lookup     string
		lookupErr  error
		wantLookup bool
		wantErr    bool
		wantErrIs  error
		wantRegion string
	}{
		{
			name:       "keeps existing region without lookup",
			region:     "us-west-2",
			lookup:     "ap-southeast-2",
			wantLookup: false,
			wantRegion: "us-west-2",
		},
		{
			name:       "falls back to metadata when region is empty",
			region:     "",
			lookup:     "ap-southeast-2",
			wantLookup: true,
			wantRegion: "ap-southeast-2",
		},
		{
			name:       "fails when lookup returns an empty region",
			region:     "",
			lookup:     "",
			wantLookup: true,
			wantErr:    true,
			wantRegion: "",
		},
		{
			name:       "wraps the lookup error",
			region:     "",
			lookupErr:  sentinel,
			wantLookup: true,
			wantErr:    true,
			wantErrIs:  sentinel,
			wantRegion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := session.Must(session.NewSession())
			sess.Config.Region = aws.String(tt.region)

			called := false
			err := resolveRegion(sess, func() (string, error) {
				called = true
				return tt.lookup, tt.lookupErr
			})

			if called != tt.wantLookup {
				t.Errorf("lookup called = %v, want %v", called, tt.wantLookup)
			}
			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Errorf("error = %v, want it to wrap %v", err, tt.wantErrIs)
			}
			if got := aws.StringValue(sess.Config.Region); got != tt.wantRegion {
				t.Errorf("region = %q, want %q", got, tt.wantRegion)
			}
		})
	}
}
