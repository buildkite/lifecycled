package backoff

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

var (
	// ErrContextDone returned when context is canceled
	ErrContextDone = errors.New("context done")
)

const (
	factor = 2
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// Retry the given function n times jittering between max and min time.Duration
func Retry(ctx context.Context, attempts int, base, max time.Duration, f func() error) (err error) {

	for attempt := 1; attempt <= attempts; attempt++ {

		select {
		case <-ctx.Done():
			return ErrContextDone
		default:
		}

		if err = f(); err == nil {
			return nil
		}

		jitterSleep(attempt, base, max)
	}

	return err
}

// Until is like retry but retries until success
func Until(ctx context.Context, base, max time.Duration, f func() error) (err error) {

	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return ErrContextDone
		default:
		}

		if err := f(); err == nil {
			return nil
		}

		jitterSleep(attempt, base, max)

		if attempt == math.MaxInt64 {
			attempt = 1
		}
	}
}

func jitterSleep(attempt int, base, max time.Duration) {
	mx := float64(max)
	mn := float64(base)

	dur := mn * math.Pow(factor, float64(attempt))
	if dur > mx {
		dur = mx
	}
	j := time.Duration(rand.Float64()*(dur-mn) + mn)
	time.Sleep(j)
}
