package benchjobs

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cidekar/adele-queue/api"
)

type FanoutPayload struct {
	Workers int `json:"workers"`
	DelayMs int `json:"delay_ms"`
}

func FanoutHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("fanout: payload not []byte, got %T", payload)
	}
	var p FanoutPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("fanout: unmarshal: %w", err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(p.Workers)
	for i := 0; i < p.Workers; i++ {
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(p.DelayMs) * time.Millisecond)
		}()
	}
	wg.Wait()
	fmt.Printf("fanout: %d workers, %dms delay, elapsed=%s\n", p.Workers, p.DelayMs, time.Since(start))
	return nil
}

func NewFanoutJob(workers, delayMs int) (*api.Job, error) {
	raw, err := json.Marshal(FanoutPayload{Workers: workers, DelayMs: delayMs})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "slow-fanout",
		Payload: raw,
		Handler: FanoutHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
