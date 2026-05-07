package benchjobs

import (
	"encoding/json"
	"fmt"

	"github.com/cidekar/adele-queue/api"
)

type CounterPayload struct {
	N int `json:"n"`
}

func CounterHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("counter: payload not []byte, got %T", payload)
	}
	var p CounterPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("counter: unmarshal: %w", err)
	}
	for i := 0; i <= p.N; i++ {
		fmt.Printf("counter: %d\n", i)
	}
	return nil
}

func NewCounterJob(n int) (*api.Job, error) {
	raw, err := json.Marshal(CounterPayload{N: n})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "simple-counter",
		Payload: raw,
		Handler: CounterHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
