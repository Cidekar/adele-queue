package benchjobs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cidekar/adele-queue/api"
)

type SleepPayload struct {
	Millis int64 `json:"millis"`
}

func SleepHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("sleep: payload not []byte, got %T", payload)
	}
	var p SleepPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("sleep: unmarshal: %w", err)
	}
	start := time.Now()
	time.Sleep(time.Duration(p.Millis) * time.Millisecond)
	fmt.Printf("slept %dms (actual %s)\n", p.Millis, time.Since(start))
	return nil
}

func NewSleepJob(d time.Duration) (*api.Job, error) {
	raw, err := json.Marshal(SleepPayload{Millis: d.Milliseconds()})
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "slow-sleep",
		Payload: raw,
		Handler: SleepHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
