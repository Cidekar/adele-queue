package benchjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cidekar/adele-queue/api"
)

// flakyAttempts maps a job ID → attempt counter. Shared across all flaky jobs
// in this package. Reusing an ID across unrelated dispatches leaks entries
// and will surprise handlers that rely on the counter starting at zero.
var flakyAttempts sync.Map

func loadOrCreateCounter(id string) *int64 {
	if v, ok := flakyAttempts.Load(id); ok {
		return v.(*int64)
	}
	var zero int64
	actual, _ := flakyAttempts.LoadOrStore(id, &zero)
	return actual.(*int64)
}

type FailThenSucceedPayload struct {
	ID        string `json:"id"`
	FailTimes int    `json:"fail_times"`
}

func FailThenSucceedHandler(payload any) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("failThenSucceed: payload not []byte, got %T", payload)
	}
	var p FailThenSucceedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("failThenSucceed: unmarshal: %w", err)
	}
	if p.ID == "" {
		return errors.New("failThenSucceed: empty ID")
	}
	attempt := atomic.AddInt64(loadOrCreateCounter(p.ID), 1)
	if attempt <= int64(p.FailTimes) {
		fmt.Printf("failThenSucceed[%s]: attempt %d — forcing failure\n", p.ID, attempt)
		return fmt.Errorf("planned failure on attempt %d", attempt)
	}
	fmt.Printf("failThenSucceed[%s]: attempt %d — success\n", p.ID, attempt)
	return nil
}

func NewFailThenSucceedJob(id string, failTimes int) (*api.Job, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	raw, err := json.Marshal(FailThenSucceedPayload{ID: id, FailTimes: failTimes})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:           "flaky-fail-then-succeed",
		Payload:        raw,
		Handler:        FailThenSucceedHandler,
		Queue:          "job",
		Retry:          true,
		RetryInSeconds: 1,
	}, nil
}
