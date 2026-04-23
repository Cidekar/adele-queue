package benchjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"

	"github.com/cidekar/adele-queue/api"
)

type IntermittentPayload struct {
	ID string  `json:"id"`
	P  float64 `json:"p"`
}

func IntermittentHandler(payload any) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("intermittent: payload not []byte, got %T", payload)
	}
	var p IntermittentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("intermittent: unmarshal: %w", err)
	}
	if p.ID == "" {
		return errors.New("intermittent: empty ID")
	}
	attempt := atomic.AddInt64(loadOrCreateCounter(p.ID), 1)
	roll := rand.Float64()
	if roll >= p.P {
		fmt.Printf("intermittent[%s]: attempt %d, roll=%.3f >= p=%.3f — fail\n", p.ID, attempt, roll, p.P)
		return fmt.Errorf("RNG failure on attempt %d", attempt)
	}
	fmt.Printf("intermittent[%s]: attempt %d, roll=%.3f < p=%.3f — success\n", p.ID, attempt, roll, p.P)
	return nil
}

func NewIntermittentJob(id string, successProbability float64) (*api.Job, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	p := successProbability
	switch {
	case p < 0:
		p = 0
	case p > 1:
		p = 1
	}
	raw, err := json.Marshal(IntermittentPayload{ID: id, P: p})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:           "flaky-intermittent",
		Payload:        raw,
		Handler:        IntermittentHandler,
		Queue:          "job",
		Retry:          true,
		RetryInSeconds: 1,
	}, nil
}
