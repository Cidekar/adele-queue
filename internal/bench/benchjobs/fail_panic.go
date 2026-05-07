package benchjobs

import (
	"encoding/json"
	"fmt"

	"github.com/cidekar/adele-queue/api"
)

type PanicPayload struct {
	Msg string `json:"msg"`
}

func PanicHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("panic: payload not []byte, got %T", payload)
	}
	var p PanicPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("panic: unmarshal: %w", err)
	}
	panic(p.Msg)
}

func NewPanicJob(msg string) (*api.Job, error) {
	raw, err := json.Marshal(PanicPayload{Msg: msg})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "fail-panic",
		Payload: raw,
		Handler: PanicHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
