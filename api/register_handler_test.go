package api

import (
	"strings"
	"testing"
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
	if err := got(nil); err != nil {
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
