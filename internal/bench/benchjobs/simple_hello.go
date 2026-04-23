package benchjobs

import (
	"encoding/json"
	"fmt"

	"github.com/cidekar/adele-queue/api"
)

type HelloPayload struct {
	Name string `json:"name"`
}

func HelloHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("hello: payload not []byte, got %T", payload)
	}
	var p HelloPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("hello: unmarshal: %w", err)
	}
	fmt.Printf("hello, %s\n", p.Name)
	return nil
}

func NewHelloJob(name string) (*api.Job, error) {
	raw, err := json.Marshal(HelloPayload{Name: name})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "hello-world",
		Payload: raw,
		Handler: HelloHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
