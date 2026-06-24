package main

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
)

func TestResolveRegion(t *testing.T) {
	t.Run("keeps existing region without lookup", func(t *testing.T) {
		sess := session.Must(session.NewSession())
		sess.Config.Region = aws.String("us-west-2")

		called := false
		err := resolveRegion(sess, func() (string, error) {
			called = true
			return "ap-southeast-2", nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if called {
			t.Error("metadata lookup ran despite region already being set")
		}
		if got := aws.StringValue(sess.Config.Region); got != "us-west-2" {
			t.Errorf("region = %q, want us-west-2", got)
		}
	})

	t.Run("falls back to metadata when region is empty", func(t *testing.T) {
		sess := session.Must(session.NewSession())
		sess.Config.Region = aws.String("")

		err := resolveRegion(sess, func() (string, error) {
			return "ap-southeast-2", nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got := aws.StringValue(sess.Config.Region); got != "ap-southeast-2" {
			t.Errorf("region = %q, want ap-southeast-2", got)
		}
	})

	t.Run("wraps the lookup error", func(t *testing.T) {
		sess := session.Must(session.NewSession())
		sess.Config.Region = aws.String("")

		want := errors.New("metadata unavailable")
		err := resolveRegion(sess, func() (string, error) {
			return "", want
		})
		if !errors.Is(err, want) {
			t.Errorf("error = %v, want it to wrap %v", err, want)
		}
		if got := aws.StringValue(sess.Config.Region); got != "" {
			t.Errorf("region = %q, want empty after failed lookup", got)
		}
	})
}
