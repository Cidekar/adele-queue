package api

import (
	"errors"
	"testing"
)

type Payload struct {
	Email         string
	Message       string
	TransactionID int
	Type          string
}

// ProcessTransaction is the handler used by the dispatch tests. It returns an
// error whenever the payload's Type field is "Error" so the error path can be
// exercised.
func ProcessTransaction(payload any) error {
	p, ok := payload.(Payload)
	if !ok {
		// When the queue persists a job it re-marshals payload into []byte,
		// so bail out silently in that case rather than panicking.
		return nil
	}
	if p.Type == "Error" {
		return errors.New("unexpected error occurred while processing the transaction")
	}
	return nil
}

func TestQueue_Dispatch_Job(t *testing.T) {
	runMigrations(t)
	defer tearDownDatabase(t)

	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()

	instance := Job{
		Name:    "Process Transaction",
		Handler: ProcessTransaction,
	}

	id, err := q.Dispatch(instance)
	if err != nil {
		t.Fatalf("dispatch returned error: %v", err)
	}

	q.Close(nil)

	jobs, err := q.GetCompletedJobs()
	if err != nil {
		t.Fatalf("GetCompletedJobs returned error: %v", err)
	}
	if len(*jobs) == 0 || (*jobs)[0].ID != id {
		t.Errorf("expected completed job with id %s but it was not found", id)
	}
}

func TestQueue_Dispatch_Failed_Job(t *testing.T) {
	runMigrations(t)
	defer tearDownDatabase(t)

	q, err := NewWithConfig(&ade, Configuration{Backend: "memory"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()

	instance := Job{
		Name: "Process Transaction",
		Handler: func(payload any) error {
			return errors.New("forced failure")
		},
	}

	id, err := q.Dispatch(instance)
	if err != nil {
		t.Fatalf("dispatch returned error: %v", err)
	}

	q.Close(nil)

	failJobsInMemory := q.GetFailedJobsFromMemory()
	if len(failJobsInMemory) != 1 {
		t.Errorf("failed jobs in memory expected %d but got %d", 1, len(failJobsInMemory))
	}

	jobs, err := q.GetFailedJobs()
	if err != nil {
		t.Fatalf("GetFailedJobs returned error: %v", err)
	}
	if len(*jobs) == 0 || (*jobs)[0].ID != id {
		t.Errorf("expected failed job with id %s but it was not found", id)
	}
}

func TestQueue_Can_Empty_Failed_Job_Cache(t *testing.T) {
	runMigrations(t)
	defer tearDownDatabase(t)

	q, err := NewWithConfig(&ade, Configuration{
		Backend:       "memory",
		HighWaterMark: 1,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q.Listen()

	instance := Job{
		Name: "Process Transaction",
		Handler: func(payload any) error {
			return errors.New("forced failure")
		},
	}

	// Dispatch jobs exceeding the high water mark of 1 so the cache resets
	for range 2 {
		if _, err := q.Dispatch(instance); err != nil {
			t.Fatalf("dispatch returned error: %v", err)
		}
	}

	q.Close(nil)

	failJobsInMemory := q.GetFailedJobsFromMemory()
	if len(failJobsInMemory) != 1 {
		t.Errorf("failed jobs cache should have reset and hold %d but got %d", 1, len(failJobsInMemory))
	}
}
