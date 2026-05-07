package api

// Benchmarks ported from the kicktires stress harness. Run with:
//
//   go test . -bench=. -benchmem -run=^$
//   go test . -bench=BenchmarkDispatch -benchtime=5s -benchmem -run=^$
//
// -run=^$ disables normal tests so only benchmarks execute. Benchmarks use
// the memory backend and no database.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// benchQueue builds a queue with N workers, suitable for pure throughput
// measurement. Reuses the package-level `ade` from setup_test.go.
func benchQueue(b *testing.B, workers int) *Queue {
	b.Helper()
	q, err := NewWithConfig(&ade, Configuration{
		Backend:             "memory",
		WorkerCount:         workers,
		MaxAttempts:         3,
		HighWaterMark:       100000,
		QueueChannelDefault: "job",
	})
	if err != nil {
		b.Fatalf("setup: %v", err)
	}
	q.Listen()
	b.Cleanup(func() { q.Close(nil) })
	return q
}

// BenchmarkDispatch measures raw dispatch throughput with a no-op handler.
// The channel hand-off cost is the metric here, not handler work.
func BenchmarkDispatch(b *testing.B) {
	q := benchQueue(b, 1)
	handler := func(payload any) error { return nil }

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := q.Dispatch(Job{Name: "noop", Handler: handler}); err != nil {
			b.Fatalf("dispatch: %v", err)
		}
	}
}

// BenchmarkDispatchParallel measures dispatch throughput under goroutine
// contention. Run with -cpu=1,2,4,8 to profile scaling.
func BenchmarkDispatchParallel(b *testing.B) {
	q := benchQueue(b, 4)
	handler := func(payload any) error { return nil }

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := q.Dispatch(Job{Name: "noop", Handler: handler}); err != nil {
				b.Fatalf("dispatch: %v", err)
			}
		}
	})
}

// BenchmarkDispatchWait measures dispatch-plus-handler round-trip by waiting
// on a WaitGroup per job. Slower than BenchmarkDispatch but closer to real
// end-to-end throughput.
func BenchmarkDispatchWait(b *testing.B) {
	q := benchQueue(b, 1)
	var wg sync.WaitGroup
	handler := func(payload any) error {
		wg.Done()
		return nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		if _, err := q.Dispatch(Job{Name: "wait", Handler: handler}); err != nil {
			b.Fatalf("dispatch: %v", err)
		}
	}
	wg.Wait()
}

// BenchmarkRetryStorm measures throughput under a failure-heavy workload
// where every job retries to exhaustion. Includes retry overhead + detached
// goroutine scheduling cost.
//
// Note: this benchmark's wall time is dominated by retry backoff sleeps
// (1s, 2s, 3s per job). Useful for profiling retry-path overhead and for
// asserting no deadlock; not useful for ns/op comparisons.
func BenchmarkRetryStorm(b *testing.B) {
	q := benchQueue(b, 1)
	var attempts atomic.Int64
	handler := func(payload any) error {
		attempts.Add(1)
		return errors.New("planned")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := q.Dispatch(Job{
			Name:           "fail",
			Handler:        handler,
			Retry:          true,
			RetryInSeconds: 1,
		}); err != nil {
			b.Fatalf("dispatch: %v", err)
		}
	}
	// Wait for the retry storm to drain so b.N iterations reflect real work.
	// MaxAttempts=3 backoff ceiling is 1+2+3 = 6s per job in the worst case.
	deadline := time.Now().Add(10 * time.Second)
	target := int64(b.N) * 4
	for time.Now().Before(deadline) && attempts.Load() < target {
		time.Sleep(50 * time.Millisecond)
	}
	b.ReportMetric(float64(attempts.Load()), "attempts")
}

// BenchmarkPanicRecovery measures overhead of the runHandler panic-recovery
// path on the hot loop.
func BenchmarkPanicRecovery(b *testing.B) {
	q := benchQueue(b, 1)
	var wg sync.WaitGroup
	handler := func(payload any) error {
		defer wg.Done()
		panic("benchmark panic")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		if _, err := q.Dispatch(Job{Name: "panic", Handler: handler}); err != nil {
			b.Fatalf("dispatch: %v", err)
		}
	}
	wg.Wait()
}
