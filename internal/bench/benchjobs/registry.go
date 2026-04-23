// Package jobs wires the test-job constructors (written by Wave 1 implementers
// #1-5) into a single one-shot dispatcher. SeedAll is invoked from main.go
// when the application is launched with the --seed-jobs CLI flag.
package benchjobs

import (
	"errors"
	"fmt"
	"time"

	"github.com/cidekar/adele-queue/api"
)

// QueueDispatcher is the minimum surface of *api.Queue that SeedAll needs.
// Declaring it here keeps jobs/registry.go testable with a fake dispatcher
// and free of a hard dependency on the concrete queue implementation.
type QueueDispatcher interface {
	Dispatch(api.Job) (string, error)
}

// logger is the tiny subset of log.Logger / logrus.Logger we rely on.
type logger interface {
	Printf(format string, args ...interface{})
}

// stdPrintfLogger is a fallback logger that writes to fmt.Printf so the
// registry never panics if a nil logger is provided.
type stdPrintfLogger struct{}

func (stdPrintfLogger) Printf(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// seedEntry pairs a human-readable name with a constructor thunk. The thunk
// returns the built Job plus any constructor error so SeedAll can accumulate
// failures without aborting mid-way.
type seedEntry struct {
	name string
	ctor func() (*api.Job, error)
}

// SeedAll builds every Wave 1 test job via its constructor and dispatches it
// onto the supplied queue. Errors from constructors and from Dispatch are
// accumulated and returned joined so a single bad job does not hide the rest.
//
// A 100ms sleep between dispatches keeps log output readable when the memory
// backend drains quickly.
func SeedAll(queue QueueDispatcher) error {
	return seedAll(queue, stdPrintfLogger{})
}

// seedAll is the testable form of SeedAll; it accepts an injected logger.
func seedAll(queue QueueDispatcher, log logger) error {
	if queue == nil {
		return errors.New("jobs.SeedAll: queue dispatcher is nil")
	}

	entries := []seedEntry{
		// Simple success (impl #1)
		{"HelloJob", func() (*api.Job, error) { return NewHelloJob("world") }},
		{"SumJob", func() (*api.Job, error) { return NewSumJob(3, 4) }},
		{"CounterJob", func() (*api.Job, error) { return NewCounterJob(10) }},

		// Slow (impl #2)
		{"SleepJob", func() (*api.Job, error) { return NewSleepJob(500 * time.Millisecond) }},
		{"BusyLoopJob", func() (*api.Job, error) { return NewBusyLoopJob(1_000_000) }},
		{"FanoutJob", func() (*api.Job, error) { return NewFanoutJob(5, 100) }},

		// Always-fail (impl #3)
		{"AlwaysErrorJob", func() (*api.Job, error) { return NewAlwaysErrorJob("expected failure") }},
		{"PanicJob", func() (*api.Job, error) { return NewPanicJob("expected panic") }},
		{"NilDerefJob", func() (*api.Job, error) { return NewNilDerefJob() }},

		// Flaky (impl #4)
		{"FailThenSucceedJob", func() (*api.Job, error) { return NewFailThenSucceedJob("flaky-1", 2) }},
		{"IntermittentJob", func() (*api.Job, error) { return NewIntermittentJob("flaky-2", 0.5) }},
		{"ExhaustRetriesJob", func() (*api.Job, error) { return NewExhaustRetriesJob("flaky-3") }},

		// Payload edge (impl #5)
		{"LargePayloadJob", func() (*api.Job, error) { return NewLargePayloadJob(1 << 20) }},
		{"UnicodeJob", func() (*api.Job, error) { return NewUnicodeJob() }},
		{"NestedPayloadJob", func() (*api.Job, error) { return NewNestedPayloadJob() }},
	}

	var errs []error
	log.Printf("jobs.SeedAll: dispatching %d test jobs\n", len(entries))

	for i, entry := range entries {
		job, err := entry.ctor()
		if err != nil {
			log.Printf("jobs.SeedAll[%d] %s: constructor error: %v\n", i, entry.name, err)
			errs = append(errs, fmt.Errorf("%s: construct: %w", entry.name, err))
			continue
		}

		if job == nil {
			errs = append(errs, fmt.Errorf("%s: constructor returned nil job", entry.name))
			continue
		}
		id, err := queue.Dispatch(*job)
		if err != nil {
			log.Printf("jobs.SeedAll[%d] %s: dispatch error: %v\n", i, entry.name, err)
			errs = append(errs, fmt.Errorf("%s: dispatch: %w", entry.name, err))
			continue
		}

		log.Printf("jobs.SeedAll[%d] dispatched %s id=%s\n", i, entry.name, id)

		// Space out dispatches so the worker-pool log output stays readable.
		if i < len(entries)-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	log.Printf("jobs.SeedAll: finished; %d error(s)\n", len(errs))
	return errors.Join(errs...)
}

// RegisterAll wires every benchjob handler into the queue's in-process
// registry by the name each New*Job constructor uses. Returns collected
// errors so callers can log without aborting.
func RegisterAll(q *api.Queue) []error {
	var errs []error
	handlers := []struct {
		name string
		fn   func(payload interface{}) error
	}{
		{"hello-world", HelloHandler},
		{"simple-sum", SumHandler},
		{"simple-counter", CounterHandler},
		{"slow-sleep", SleepHandler},
		{"slow-busy-loop", BusyLoopHandler},
		{"slow-fanout", FanoutHandler},
		{"fail-error", AlwaysErrorHandler},
		{"fail-panic", PanicHandler},
		{"fail-nil-deref", NilDerefHandler},
		{"flaky-fail-then-succeed", FailThenSucceedHandler},
		{"flaky-intermittent", IntermittentHandler},
		{"flaky-exhaust-retries", ExhaustRetriesHandler},
		{"payload-large", LargePayloadHandler},
		{"payload-unicode", UnicodeHandler},
		{"payload-nested", NestedHandler},
	}
	for _, h := range handlers {
		if err := q.RegisterHandler(h.name, h.fn); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", h.name, err))
		}
	}
	return errs
}
