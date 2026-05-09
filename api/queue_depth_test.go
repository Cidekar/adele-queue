package api

// Tests for the public Queue.Depth() accessor.
//
//   go test . -run TestDepth
//   go test . -race -run TestDepth_Concurrent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDepth_Memory_ReportsChannelOccupancy stands up a memory queue with N
// workers and a handler that blocks on a release channel. It dispatches N
// jobs and asserts Depth() observes a non-zero pending count while every
// worker is occupied. The post-handler decrement does not fire until the
// release channel is closed, so the in-flight count is stable.
//
// This pins the contract that Depth() reports the count of dispatched-but-
// not-yet-completed jobs, not (the now-removed) channel buffer occupancy.
func TestDepth_Memory_ReportsChannelOccupancy(t *testing.T) {
	const N = 5
	q := newTestQueue(t, N)

	release := make(chan struct{})
	var started atomic.Int64

	for i := 0; i < N; i++ {
		if _, err := q.Dispatch(Job{
			Name: "block",
			Handler: func(payload any) error {
				started.Add(1)
				<-release
				return nil
			},
		}); err != nil {
			close(release)
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	// Wait until every worker has picked up its job. Only at that point is
	// the in-flight count stable: handlers are blocked on `release`, so the
	// post-handler decrement has not yet fired for any of them.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() == N {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if started.Load() != N {
		close(release)
		t.Fatalf("expected %d handlers to start, got %d", N, started.Load())
	}

	got, err := q.Depth()
	if err != nil {
		close(release)
		t.Fatalf("Depth() error: %v", err)
	}
	if got != N {
		close(release)
		t.Fatalf("expected depth %d while handlers blocked, got %d", N, got)
	}

	// Release the workers so t.Cleanup can drain cleanly.
	close(release)
}

// TestDepth_Memory_DrainsToZero registers a handler that blocks on a channel
// the test holds, dispatches N jobs, asserts Depth() == N while the handlers
// are blocked, releases the handlers, waits for completion, and asserts
// Depth() == 0 once everything has drained.
func TestDepth_Memory_DrainsToZero(t *testing.T) {
	const N = 5
	q := newTestQueue(t, N)

	release := make(chan struct{})
	var started atomic.Int64
	var done atomic.Int64

	for i := 0; i < N; i++ {
		if _, err := q.Dispatch(Job{
			Name: "block",
			Handler: func(payload any) error {
				started.Add(1)
				<-release
				done.Add(1)
				return nil
			},
		}); err != nil {
			close(release)
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	// Wait until every worker has picked up its job. With WorkerCount == N,
	// all N jobs are in-flight (handlers blocked on `release`), so the
	// post-Dispatch increment is fully observable.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if started.Load() == N {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if started.Load() != N {
		close(release)
		t.Fatalf("expected %d handlers to start, got %d", N, started.Load())
	}

	// All N pending — handlers blocked, decrement has not yet fired.
	got, err := q.Depth()
	if err != nil {
		close(release)
		t.Fatalf("Depth() error: %v", err)
	}
	if got != N {
		close(release)
		t.Fatalf("expected depth %d while handlers blocked, got %d", N, got)
	}

	// Release the handlers and wait for full drain.
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done.Load() == N {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if done.Load() != N {
		t.Fatalf("expected %d completions, got %d", N, done.Load())
	}

	// Drain assertion: Depth() must return to zero once every handler has
	// returned and the worker loop has decremented.
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := q.Depth(); got == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, err = q.Depth()
	if err != nil {
		t.Fatalf("Depth() error: %v", err)
	}
	if got != 0 {
		t.Fatalf("expected depth 0 after drain, got %d", got)
	}
}

// TestDepth_UnknownBackend asserts the dispatch returns a clear error when
// the backend string is neither "memory" nor "redis". setConfigDefaults
// normalizes invalid values to "memory", so we mutate the field on a built
// queue to exercise the unreachable switch arm.
func TestDepth_UnknownBackend(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Backend = "bogus"
	_, derr := q.Depth()
	if derr == nil {
		t.Fatalf("expected error for unknown backend, got nil")
	}
}

// TestDepth_Redis_PoolUnavailable proves Depth on a redis-backed queue
// returns an error when the pool is nil (the live-redis path is exercised
// only when a redis test harness is available; this test guards the
// non-network failure mode).
func TestDepth_Redis_PoolUnavailable(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "redis"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	// buildQueue only wires Pool when an Adele cache is wired; the test
	// adele.Adele has none, so q.Redis.Pool is nil.
	if q.Redis.Pool != nil {
		t.Skip("redis pool is wired in this environment; skipping nil-pool branch")
	}
	if _, derr := q.Depth(); derr == nil {
		t.Fatalf("expected error when redis pool is unavailable, got nil")
	}
}

// TestDepth_Concurrent_NoRace runs Depth concurrently with Dispatch under
// the race detector to prove the call is safe to make alongside live
// dispatches. Run with `go test -race`.
//
// Shutdown-safety: this test does NOT loop until a stop channel. The
// previous shape used `select { case <-stop: return; default: q.Dispatch(...) }`
// which racily allowed Dispatch to run after t.Cleanup → q.Close(nil) had
// closed q.Jobs, panicking on send. Instead, we kick off a fixed N
// dispatches and a fixed M Depth() reads, wait for both via WaitGroup,
// and let the race detector be the load-bearing assertion. No path lets
// Dispatch run after Close.
func TestDepth_Concurrent_NoRace(t *testing.T) {
	q := newTestQueue(t, 4)

	const (
		dispatchers = 4
		perWorker   = 250 // 4 * 250 = 1000 dispatches
		readers     = 4
		readsEach   = 1000
	)

	var wg sync.WaitGroup

	// Dispatchers: each performs a fixed iteration count, then exits. No
	// stop channel — by construction we cannot dispatch after the test
	// cleanup closes q.Jobs because we wait for every dispatcher below.
	for i := 0; i < dispatchers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				_, _ = q.Dispatch(Job{
					Name: "noop",
					Handler: func(payload any) error {
						return nil
					},
				})
			}
		}()
	}

	// Readers: each performs a fixed iteration count of Depth() calls.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < readsEach; j++ {
				if _, err := q.Depth(); err != nil {
					t.Errorf("Depth() error during concurrent run: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	// t.Cleanup will Close. By the time cleanup fires, every dispatcher
	// has returned, so no further send onto q.Jobs is possible.
}
