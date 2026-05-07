package benchjobs

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/cidekar/adele-queue/api"
)

type BusyLoopPayload struct {
	Iterations int `json:"iterations"`
}

func BusyLoopHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("busyLoop: payload not []byte, got %T", payload)
	}
	var p BusyLoopPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("busyLoop: unmarshal: %w", err)
	}
	start := time.Now()
	var sink uint64
	for i := 0; i < p.Iterations; i++ {
		sink = sink*1664525 + uint64(i) + 1013904223
	}
	runtime.KeepAlive(sink)
	fmt.Printf("busyLoop: %d iters, sink=%d, elapsed=%s\n", p.Iterations, sink, time.Since(start))
	return nil
}

func NewBusyLoopJob(iterations int) (*api.Job, error) {
	raw, err := json.Marshal(BusyLoopPayload{Iterations: iterations})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "slow-busy-loop",
		Payload: raw,
		Handler: BusyLoopHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
