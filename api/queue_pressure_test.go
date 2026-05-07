package api

// Pressure-style correctness tests ported from the kicktires stress harness.
// These exercise failure-mode behavior end-to-end without requiring a live
// database, so they run in any `go test` invocation.
//
//   go test . -run TestPressure
//   go test . -race -run TestPressure

import (
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// newTestQueue builds a Queue suitable for pressure tests: memory backend,
// N workers, fast retries. Reuses the package-level `ade` adele.Adele zero
// value from setup_test.go. No DB interaction is needed for these tests.
func newTestQueue(tb testing.TB, workers int) *Queue {
	tb.Helper()
	q, err := NewWithConfig(&ade, Configuration{
		Backend:             "memory",
		WorkerCount:         workers,
		MaxAttempts:         3,
		HighWaterMark:       10000,
		QueueChannelDefault: "job",
	})
	if err != nil {
		tb.Fatalf("setup: %v", err)
	}
	q.Listen()
	tb.Cleanup(func() { q.Close(nil) })
	return q
}

// TestPressure_PanicRecovery proves a panicking handler does not crash the
// worker goroutine. Regression guard for the queue.go runHandler patch.
func TestPressure_PanicRecovery(t *testing.T) {
	q := newTestQueue(t, 1)

	var hellos atomic.Int64

	// Dispatch one panic + one normal job. If the panic killed the worker,
	// the second job never runs and the test hangs (covered by -timeout).
	if _, err := q.Dispatch(Job{
		Name: "panic",
		Handler: func(payload any) error {
			panic("boom")
		},
	}); err != nil {
		t.Fatalf("dispatch panic: %v", err)
	}
	if _, err := q.Dispatch(Job{
		Name: "hello",
		Handler: func(payload any) error {
			hellos.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatalf("dispatch hello: %v", err)
	}

	// Poll up to 2s for the follow-up job to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hellos.Load() == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hello job never ran after panic — worker likely died (hellos=%d)", hellos.Load())
}

// TestPressure_RetryExhaustionCount proves MaxAttempts drives exactly N+1
// handler invocations (initial + N retries) for an always-failing job.
func TestPressure_RetryExhaustionCount(t *testing.T) {
	q := newTestQueue(t, 1)

	var attempts atomic.Int64
	if _, err := q.Dispatch(Job{
		Name:           "always-fail",
		Retry:          true,
		RetryInSeconds: 1,
		Handler: func(payload any) error {
			attempts.Add(1)
			return errors.New("planned")
		},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// MaxAttempts=3 with 1s * retryCounter backoff. Worst case: 1 + 2 + 3 = 6s
	// of scheduled sleeps plus handler time. Allow 8s for slack.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify attempts settled at exactly 4 (initial + 3 retries) and did not
	// continue firing afterward.
	time.Sleep(200 * time.Millisecond)
	got := attempts.Load()
	if got != 4 {
		t.Fatalf("expected exactly 4 invocations (initial + 3 retries), got %d", got)
	}
}

// TestPressure_RetryNoDeadlock proves a failing job's retry re-enqueue does
// not deadlock the single worker. Regression guard for the memory.go
// detached-goroutine patch.
func TestPressure_RetryNoDeadlock(t *testing.T) {
	q := newTestQueue(t, 1)

	// Dispatch 50 always-failing jobs back to back. Pre-patch, even one
	// would have deadlocked (worker blocks on unbuffered channel send).
	var attempts atomic.Int64
	for i := 0; i < 50; i++ {
		if _, err := q.Dispatch(Job{
			Name:           "always-fail",
			Retry:          true,
			RetryInSeconds: 1,
			Handler: func(payload any) error {
				attempts.Add(1)
				return errors.New("planned")
			},
		}); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	// 50 jobs × 4 invocations = 200. Give 10s for retry fanout to finish.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 200 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := attempts.Load()
	if got < 200 {
		t.Fatalf("expected >=200 invocations (50 jobs * 4), got %d — likely deadlock", got)
	}
}

// TestPressure_NoGoroutineLeak dispatches a burst of quick jobs and asserts
// the process returns to its baseline goroutine count within a reasonable
// window. Retry goroutines should be time-bounded, not sticky.
func TestPressure_NoGoroutineLeak(t *testing.T) {
	q := newTestQueue(t, 1)

	// Let the worker settle.
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	var done atomic.Int64
	const N = 500
	for i := 0; i < N; i++ {
		if _, err := q.Dispatch(Job{
			Name: "quick",
			Handler: func(payload any) error {
				done.Add(1)
				return nil
			},
		}); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	// Wait for all jobs to complete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done.Load() == N {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() != N {
		t.Fatalf("expected %d completions, got %d", N, done.Load())
	}

	// Goroutines from the runtime (sweeper, GC, finalizers) may fluctuate by
	// a handful. Allow a generous margin; leak would be hundreds.
	time.Sleep(100 * time.Millisecond)
	final := runtime.NumGoroutine()
	if final > baseline+20 {
		t.Fatalf("goroutine leak suspected: baseline=%d final=%d", baseline, final)
	}
}
