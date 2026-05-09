package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// newQueueForRegistryTest returns a freshly built *Queue with the handler
// registry initialized. It avoids the full test fixture because
// RegisterHandler does not require a running backend or database.
func newQueueForRegistryTest(t *testing.T) *Queue {
	t.Helper()
	q, err := NewWithConfig(nil, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return q
}

func TestRegisterHandler_Happy(t *testing.T) {
	q := newQueueForRegistryTest(t)

	called := false
	fn := func(payload interface{}) error {
		called = true
		return nil
	}

	if err := q.RegisterHandler("hello", fn); err != nil {
		t.Fatalf("RegisterHandler returned unexpected error: %v", err)
	}

	q.handlers.mu.RLock()
	got, ok := q.handlers.m["hello"]
	q.handlers.mu.RUnlock()
	if !ok {
		t.Fatal("handler not stored in registry after RegisterHandler")
	}
	if got.plain == nil {
		t.Fatal("stored entry has no plain handler set")
	}
	if got.ctx != nil {
		t.Fatal("stored entry should not have a ctx handler set when registered via RegisterHandler")
	}
	if err := got.plain(nil); err != nil {
		t.Fatalf("stored handler returned unexpected error: %v", err)
	}
	if !called {
		t.Fatal("stored handler did not appear to be invoked")
	}
}

func TestRegisterHandler_EmptyName(t *testing.T) {
	q := newQueueForRegistryTest(t)

	err := q.RegisterHandler("", func(payload interface{}) error { return nil })
	if err == nil {
		t.Fatal("RegisterHandler with empty name should have returned an error")
	}
	if !strings.Contains(err.Error(), "non-empty name") {
		t.Errorf("error message did not mention the empty-name requirement: %v", err)
	}
}

func TestRegisterHandler_NilFunc(t *testing.T) {
	q := newQueueForRegistryTest(t)

	err := q.RegisterHandler("hello", nil)
	if err == nil {
		t.Fatal("RegisterHandler with nil fn should have returned an error")
	}
	if !strings.Contains(err.Error(), "non-nil function") {
		t.Errorf("error message did not mention the non-nil fn requirement: %v", err)
	}
}

func TestRegisterHandler_Duplicate(t *testing.T) {
	q := newQueueForRegistryTest(t)

	fn := func(payload interface{}) error { return nil }
	if err := q.RegisterHandler("hello", fn); err != nil {
		t.Fatalf("first RegisterHandler returned unexpected error: %v", err)
	}

	err := q.RegisterHandler("hello", fn)
	if err == nil {
		t.Fatal("duplicate RegisterHandler should have returned an error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error message did not mention duplicate registration: %v", err)
	}
}

func TestRegisterHandlerCtx_Happy(t *testing.T) {
	q := newQueueForRegistryTest(t)

	fn := func(ctx context.Context, payload interface{}) error { return nil }
	if err := q.RegisterHandlerCtx("hello", fn); err != nil {
		t.Fatalf("RegisterHandlerCtx returned unexpected error: %v", err)
	}

	q.handlers.mu.RLock()
	got, ok := q.handlers.m["hello"]
	q.handlers.mu.RUnlock()
	if !ok {
		t.Fatal("handler not stored in registry after RegisterHandlerCtx")
	}
	if got.ctx == nil {
		t.Fatal("stored entry has no ctx handler set")
	}
	if got.plain != nil {
		t.Fatal("stored entry should not have a plain handler set when registered via RegisterHandlerCtx")
	}
}

func TestRegisterHandlerCtx_EmptyName(t *testing.T) {
	q := newQueueForRegistryTest(t)

	err := q.RegisterHandlerCtx("", func(ctx context.Context, payload interface{}) error { return nil })
	if err == nil {
		t.Fatal("RegisterHandlerCtx with empty name should have returned an error")
	}
	if !strings.Contains(err.Error(), "non-empty name") {
		t.Errorf("error message did not mention the empty-name requirement: %v", err)
	}
}

func TestRegisterHandlerCtx_NilFunc(t *testing.T) {
	q := newQueueForRegistryTest(t)

	err := q.RegisterHandlerCtx("hello", nil)
	if err == nil {
		t.Fatal("RegisterHandlerCtx with nil fn should have returned an error")
	}
	if !strings.Contains(err.Error(), "non-nil function") {
		t.Errorf("error message did not mention the non-nil fn requirement: %v", err)
	}
}

// TestRegisterHandler_RejectsCtxName verifies the cross-shape duplicate
// guard: registering a name via RegisterHandlerCtx blocks a subsequent
// RegisterHandler under the same name.
func TestRegisterHandler_RejectsCtxName(t *testing.T) {
	q := newQueueForRegistryTest(t)

	if err := q.RegisterHandlerCtx("hello", func(ctx context.Context, payload interface{}) error { return nil }); err != nil {
		t.Fatalf("RegisterHandlerCtx returned unexpected error: %v", err)
	}

	err := q.RegisterHandler("hello", func(payload interface{}) error { return nil })
	if err == nil {
		t.Fatal("RegisterHandler over an existing ctx registration should have errored")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error message did not mention duplicate registration: %v", err)
	}
}

// TestRegisterHandlerCtx_RejectsPlainName verifies the inverse cross-shape
// duplicate guard: registering a name via RegisterHandler blocks a
// subsequent RegisterHandlerCtx under the same name.
func TestRegisterHandlerCtx_RejectsPlainName(t *testing.T) {
	q := newQueueForRegistryTest(t)

	if err := q.RegisterHandler("hello", func(payload interface{}) error { return nil }); err != nil {
		t.Fatalf("RegisterHandler returned unexpected error: %v", err)
	}

	err := q.RegisterHandlerCtx("hello", func(ctx context.Context, payload interface{}) error { return nil })
	if err == nil {
		t.Fatal("RegisterHandlerCtx over an existing plain registration should have errored")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error message did not mention duplicate registration: %v", err)
	}
}

// TestRegisterHandlerCtx_DispatchPassesContext exercises the full
// memory-backend dispatch path with a context-aware handler. Asserts that
// the handler is invoked with a non-nil, not-yet-cancelled context.
func TestRegisterHandlerCtx_DispatchPassesContext(t *testing.T) {
	q, err := NewWithConfig(nil, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()

	gotCtx := make(chan context.Context, 1)
	handler := func(ctx context.Context, payload interface{}) error {
		gotCtx <- ctx
		return nil
	}
	if err := q.RegisterHandlerCtx("ctx-job", handler); err != nil {
		t.Fatalf("RegisterHandlerCtx: %v", err)
	}

	if _, err := q.Dispatch(Job{Name: "ctx-job"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case ctx := <-gotCtx:
		if ctx == nil {
			t.Fatal("handler observed a nil context")
		}
		select {
		case <-ctx.Done():
			t.Fatal("context should not be cancelled while queue is running")
		default:
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler to run")
	}

	q.Close(nil)
}

// TestRegisterHandlerCtx_CancelOnClose verifies that Close cancels the
// lifecycle context observed by an in-flight ctx-handler.
func TestRegisterHandlerCtx_CancelOnClose(t *testing.T) {
	q, err := NewWithConfig(nil, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()

	entered := make(chan struct{})
	observedDone := make(chan struct{})
	handler := func(ctx context.Context, payload interface{}) error {
		close(entered)
		<-ctx.Done()
		close(observedDone)
		return ctx.Err()
	}
	if err := q.RegisterHandlerCtx("blocker", handler); err != nil {
		t.Fatalf("RegisterHandlerCtx: %v", err)
	}

	if _, err := q.Dispatch(Job{Name: "blocker"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not enter within timeout")
	}

	// Close runs in a goroutine because the memory backend's Close waits
	// on the worker drain, and the worker is still blocked inside the
	// handler — which only unblocks once Close cancels the lifecycle
	// context. Without parallelism here we deadlock on ourselves.
	closeDone := make(chan struct{})
	var closeWG sync.WaitGroup
	closeWG.Add(1)
	go func() {
		defer closeWG.Done()
		q.Close(nil)
		close(closeDone)
	}()

	select {
	case <-observedDone:
	case <-time.After(1 * time.Second):
		t.Fatal("handler did not observe ctx.Done within 1s of Close")
	}

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after handler unblocked")
	}
	closeWG.Wait()
}
