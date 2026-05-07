package benchjobs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cidekar/adele-queue/api"
)

type LargePayload struct {
	Data string `json:"data"`
}

func LargePayloadHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("large: payload not []byte, got %T", payload)
	}
	var p LargePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("large: unmarshal: %w", err)
	}
	fmt.Printf("large: received %d bytes of data\n", len(p.Data))
	return nil
}

func NewLargePayloadJob(sizeBytes int) (*api.Job, error) {
	raw, err := json.Marshal(LargePayload{Data: strings.Repeat("A", sizeBytes)})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "payload-large",
		Payload: raw,
		Handler: LargePayloadHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
