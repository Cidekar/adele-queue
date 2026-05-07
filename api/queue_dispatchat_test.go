package api

import (
	"sync/atomic"
	"testing"
	"time"
)

// dispatchAtHandler returns a handler that increments the supplied counter
// and signals the channel when the job runs.
func dispatchAtHandler(counter *int32, ran chan<- struct{}) func(payload any) error {
	return func(payload any) error {
		atomic.AddInt32(counter, 1)
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}
}

func TestDispatchIn_Delays_Memory_Execution(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	// DispatchIn formats the wake time as RFC3339, which truncates to seconds.
	// A sub-second delay can therefore round down to "now" and fire
	// immediately. Use a 1.5s delay so a real future timestamp survives
	// truncation; the test budget tolerates the longer wait.
	if _, err := q.DispatchIn(Job{
		Name:    "Delayed",
		Handler: dispatchAtHandler(&counter, ran),
	}, 1500*time.Millisecond); err != nil {
		t.Fatalf("DispatchIn returned error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&counter); got != 0 {
		t.Fatalf("handler ran too early: counter=%d (expected 0 within 200ms)", got)
	}

	select {
	case <-ran:
		// success
	case <-time.After(2500 * time.Millisecond):
		t.Fatalf("handler never ran within deadline")
	}

	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Fatalf("expected counter=1 after run, got %d", got)
	}
}

func TestDispatch_DispatchAt_Future_Memory(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	// RFC3339 has second resolution, so a sub-second offset can truncate to
	// "now" and dispatch immediately. Use a 2s offset so the formatted
	// timestamp is unambiguously in the future.
	job := Job{
		Name:       "Future",
		Handler:    dispatchAtHandler(&counter, ran),
		DispatchAt: time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339),
	}
	if _, err := q.Dispatch(job); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&counter); got != 0 {
		t.Fatalf("handler ran too early: counter=%d (expected 0 within 50ms)", got)
	}

	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatalf("handler never ran within deadline")
	}

	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Fatalf("expected counter=1 after run, got %d", got)
	}
}

func TestDispatch_DispatchAt_Empty_Preserves_Behavior(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	if _, err := q.Dispatch(Job{
		Name:    "Immediate",
		Handler: dispatchAtHandler(&counter, ran),
	}); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("handler did not run within 50ms (expected immediate execution)")
	}

	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Fatalf("expected counter=1 after run, got %d", got)
	}
}

func TestDispatch_DispatchAt_Past_Runs_Immediately(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	job := Job{
		Name:       "Past",
		Handler:    dispatchAtHandler(&counter, ran),
		DispatchAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}
	if _, err := q.Dispatch(job); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("handler did not run within 50ms (past DispatchAt should run immediately)")
	}

	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Fatalf("expected counter=1 after run, got %d", got)
	}
}

func TestDispatchIn_Zero_Delay_Runs_Immediately(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	if _, err := q.DispatchIn(Job{
		Name:    "ZeroDelay",
		Handler: dispatchAtHandler(&counter, ran),
	}, 0); err != nil {
		t.Fatalf("DispatchIn returned error: %v", err)
	}

	select {
	case <-ran:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("handler did not run within 50ms (zero delay should run immediately)")
	}

	if got := atomic.LoadInt32(&counter); got != 1 {
		t.Fatalf("expected counter=1 after run, got %d", got)
	}
}

func TestDispatch_DispatchAt_Invalid_Format_Errors(t *testing.T) {
	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()
	defer q.Close(nil)

	var counter int32
	ran := make(chan struct{}, 1)

	job := Job{
		Name:       "Invalid",
		Handler:    dispatchAtHandler(&counter, ran),
		DispatchAt: "not-a-timestamp",
	}
	id, err := q.Dispatch(job)
	if err == nil {
		t.Fatalf("expected error for invalid DispatchAt, got id=%q err=nil", id)
	}

	// Wait briefly to confirm the handler never runs.
	select {
	case <-ran:
		t.Fatalf("handler ran despite invalid DispatchAt")
	case <-time.After(75 * time.Millisecond):
		// expected: nothing happened
	}

	if got := atomic.LoadInt32(&counter); got != 0 {
		t.Fatalf("expected counter=0 after invalid dispatch, got %d", got)
	}
}
