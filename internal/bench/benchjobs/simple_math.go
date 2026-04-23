package benchjobs

import (
	"encoding/json"
	"fmt"

	"github.com/cidekar/adele-queue/api"
)

type SumPayload struct {
	A int `json:"a"`
	B int `json:"b"`
}

func SumHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("sum: payload not []byte, got %T", payload)
	}
	var p SumPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("sum: unmarshal: %w", err)
	}
	fmt.Printf("sum: %d + %d = %d\n", p.A, p.B, p.A+p.B)
	return nil
}

func NewSumJob(a, b int) (*api.Job, error) {
	raw, err := json.Marshal(SumPayload{A: a, B: b})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "simple-sum",
		Payload: raw,
		Handler: SumHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
