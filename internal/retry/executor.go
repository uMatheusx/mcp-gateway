package retry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Doer executes HTTP requests (satisfied by *http.Client).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Config controls retry behaviour for a single request sequence.
type Config struct {
	MaxAttempts  int
	Backoff      string // "exponential" | "linear" | "fixed"
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// Execute sends req via client, retrying on network errors and 5xx responses
// according to cfg. The request body is buffered so it can be replayed on each
// attempt without the caller having to worry about it.
func Execute(ctx context.Context, client Doer, req *http.Request, cfg Config) (*http.Response, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}

	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("buffering request body: %w", err)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			delay := computeDelay(cfg, attempt-1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		clone := req.Clone(ctx)
		if bodyBytes != nil {
			clone.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			clone.ContentLength = int64(len(bodyBytes))
		}

		resp, err := client.Do(clone)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("server error %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("after %d attempt(s): %w", cfg.MaxAttempts, lastErr)
}

func computeDelay(cfg Config, attempt int) time.Duration {
	initial := cfg.InitialDelay
	if initial == 0 {
		initial = 500 * time.Millisecond
	}
	maxDelay := cfg.MaxDelay
	if maxDelay == 0 {
		maxDelay = 30 * time.Second
	}

	var d time.Duration
	switch cfg.Backoff {
	case "linear":
		d = initial * time.Duration(attempt)
	case "fixed":
		d = initial
	default: // exponential
		d = time.Duration(float64(initial) * math.Pow(2, float64(attempt-1)))
	}

	if d > maxDelay {
		return maxDelay
	}
	return d
}
