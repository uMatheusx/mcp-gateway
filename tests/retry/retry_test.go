package retry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uMatheusx/mcp-gateway/internal/retry"
)

func TestExecute_SuccessOnFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := retry.Execute(context.Background(), http.DefaultClient, req, retry.Config{MaxAttempts: 3})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestExecute_RetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	cfg := retry.Config{
		MaxAttempts:  3,
		Backoff:      "fixed",
		InitialDelay: time.Millisecond,
	}
	resp, err := retry.Execute(context.Background(), http.DefaultClient, req, cfg)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.EqualValues(t, 3, calls.Load())
}

func TestExecute_ExhaustsAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	cfg := retry.Config{
		MaxAttempts:  3,
		Backoff:      "fixed",
		InitialDelay: time.Millisecond,
	}
	_, err := retry.Execute(context.Background(), http.DefaultClient, req, cfg)
	assert.Error(t, err)
	assert.EqualValues(t, 3, calls.Load())
}

func TestExecute_DoesNotRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(404)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := retry.Execute(context.Background(), http.DefaultClient, req, retry.Config{MaxAttempts: 3})
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
	assert.EqualValues(t, 1, calls.Load())
}

func TestExecute_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled immediately

	req, _ := http.NewRequest("GET", srv.URL, nil)
	cfg := retry.Config{
		MaxAttempts:  5,
		Backoff:      "fixed",
		InitialDelay: 100 * time.Millisecond,
	}
	_, err := retry.Execute(ctx, http.DefaultClient, req, cfg)
	assert.Error(t, err)
}

func TestExecute_DefaultsMaxAttemptsToOne(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	retry.Execute(context.Background(), http.DefaultClient, req, retry.Config{}) //nolint
	assert.EqualValues(t, 1, calls.Load())
}

func TestComputeDelay_Exponential(t *testing.T) {
	// verify through retries that delays grow: just check no panic and 3 attempts succeed
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	cfg := retry.Config{
		MaxAttempts:  3,
		Backoff:      "exponential",
		InitialDelay: time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
	}
	resp, err := retry.Execute(context.Background(), http.DefaultClient, req, cfg)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}
