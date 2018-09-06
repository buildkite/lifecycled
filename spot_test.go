package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGettingTerminationTimesWhenAvailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("2015-01-05T18:02:00Z"))
	}))
	defer ts.Close()

	termTime, hasTermTime, err := getTerminationTime(context.TODO(), ts.URL)

	if !hasTermTime {
		t.Fatalf("Expected termination time")
	}

	if err != nil {
		t.Fatal(err)
	}

	expectedTime := time.Date(2015, time.January, 5, 18, 2, 0, 0, time.UTC)

	if !termTime.Equal(expectedTime) {
		t.Fatalf("Expected %v, got %v", expectedTime, termTime)
	}
}

func TestGettingTerminationTimesWhenUnavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	termTime, hasTermTime, err := getTerminationTime(context.TODO(), ts.URL)

	if hasTermTime {
		t.Fatalf("Expected no termination time when unavailable")
	}

	if err != nil {
		t.Fatalf("Expected no error when unavailable: %#v", err)
	}

	if !termTime.IsZero() {
		t.Fatalf("Expected zero time, got %v", termTime)
	}
}

func TestGettingTerminationTimesWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Microsecond*10)
	defer cancel()

	termTime, hasTermTime, err := getTerminationTime(ctx, `https://httpbin.org/delay/3`)

	if hasTermTime {
		t.Fatalf("Expected no termination time when cancelled")
	}

	if err == nil {
		t.Fatalf("Expected an error when cancelled")
	}

	if !termTime.IsZero() {
		t.Fatalf("Expected zero time, got %v", termTime)
	}
}
