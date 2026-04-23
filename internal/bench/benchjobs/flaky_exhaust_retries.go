package benchjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/cidekar/adele-queue/api"
)

// maxAttemptsForExhaust mirrors config/queue.yml's MaxAttempts. Semantics:
// total handler executions including the initial attempt. Keep in sync with
// config/queue.yml.
const maxAttemptsForExhaust = 3

type ExhaustRetriesPayload struct {
	ID string `json:"id"`
}

func ExhaustRetriesHandler(payload any) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("exhaustRetries: payload not []byte, got %T", payload)
	}
	var p ExhaustRetriesPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("exhaustRetries: unmarshal: %w", err)
	}
	if p.ID == "" {
		return errors.New("exhaustRetries: empty ID")
	}
	attempt := atomic.AddInt64(loadOrCreateCounter(p.ID), 1)
	// Final handler invocation runs at attempt == MaxAttempts + 1
	// (initial + MaxAttempts-1 retries + the final retry that also fires).
	finalAttempt := int(attempt) > maxAttemptsForExhaust
	if finalAttempt {
		fmt.Printf("exhaustRetries[%s]: attempt %d — giving up (exceeded MaxAttempts=%d)\n", p.ID, attempt, maxAttemptsForExhaust)
	} else {
		fmt.Printf("exhaustRetries[%s]: attempt %d/%d\n", p.ID, attempt, maxAttemptsForExhaust)
	}
	return fmt.Errorf("exhaustRetries: permanent failure on attempt %d", attempt)
}

func NewExhaustRetriesJob(id string) (*api.Job, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	raw, err := json.Marshal(ExhaustRetriesPayload{ID: id})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:           "flaky-exhaust-retries",
		Payload:        raw,
		Handler:        ExhaustRetriesHandler,
		Queue:          "job",
		Retry:          true,
		RetryInSeconds: 1,
	}, nil
}
