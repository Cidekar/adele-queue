package benchjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/cidekar/adele-queue/api"
)

// AlwaysErrorAttempts counts every handler invocation across all AlwaysError
// jobs. Exported so stress runs can read the total at the end.
var AlwaysErrorAttempts atomic.Int64

type AlwaysErrorPayload struct {
	Msg string `json:"msg"`
}

func AlwaysErrorHandler(payload any) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("alwaysError: payload not []byte, got %T", payload)
	}
	var p AlwaysErrorPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("alwaysError: unmarshal: %w", err)
	}
	n := AlwaysErrorAttempts.Add(1)
	// Log every 100th attempt to keep the retry storm observable without
	// flooding stdout. Individual failures are silent; the counter is truth.
	if n%100 == 0 {
		fmt.Printf("alwaysError: %d total handler invocations so far\n", n)
	}
	return errors.New(p.Msg)
}

func NewAlwaysErrorJob(msg string) (*api.Job, error) {
	raw, err := json.Marshal(AlwaysErrorPayload{Msg: msg})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:           "fail-error",
		Payload:        raw,
		Handler:        AlwaysErrorHandler,
		Queue:          "job",
		Retry:          true,
		RetryInSeconds: 1,
	}, nil
}
