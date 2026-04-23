package benchjobs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cidekar/adele-queue/api"
)

type NestedLevel5 struct {
	Leaf string `json:"leaf,omitempty"`
}

type NestedLevel4 struct {
	Level5 *NestedLevel5 `json:"level5,omitempty"`
}

type NestedLevel3 struct {
	Level4 NestedLevel4   `json:"level4"`
	Tags   []string       `json:"tags,omitempty"`
	Metric map[string]int `json:"metric,omitempty"`
}

type NestedLevel2 struct {
	Level3 NestedLevel3      `json:"level3"`
	Rows   []map[string]int  `json:"rows,omitempty"`
	IntMap map[string]string `json:"int_map,omitempty"` // JSON keys must be strings; callers stringify int keys
}

type NestedLevel1 struct {
	Level2    NestedLevel2 `json:"level2"`
	Timestamp time.Time    `json:"timestamp"`
	NullPtr   *int         `json:"null_ptr,omitempty"`
}

type NestedPayload struct {
	Level1 NestedLevel1 `json:"level1"`
}

func NestedHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("nested: payload not []byte, got %T", payload)
	}
	var p NestedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("nested: unmarshal: %w", err)
	}
	leaf := ""
	if p.Level1.Level2.Level3.Level4.Level5 != nil {
		leaf = p.Level1.Level2.Level3.Level4.Level5.Leaf
	}
	fmt.Printf("nested: ts=%s leaf=%q rows=%d intMapLen=%d tags=%v\n",
		p.Level1.Timestamp.Format(time.RFC3339),
		leaf,
		len(p.Level1.Level2.Rows),
		len(p.Level1.Level2.IntMap),
		p.Level1.Level2.Level3.Tags,
	)
	return nil
}

func NewNestedPayloadJob() (*api.Job, error) {
	// JSON has no int-keyed maps; stringify keys on the way in.
	intMap := map[string]string{}
	for i := 1; i <= 3; i++ {
		intMap[strconv.Itoa(i)] = fmt.Sprintf("val-%d", i)
	}
	p := NestedPayload{
		Level1: NestedLevel1{
			Timestamp: time.Now().UTC(),
			Level2: NestedLevel2{
				Rows: []map[string]int{
					{"a": 1, "b": 2},
					{"c": 3, "d": 4},
				},
				IntMap: intMap,
				Level3: NestedLevel3{
					Tags:   []string{"alpha", "beta", "gamma"},
					Metric: map[string]int{"hits": 42, "misses": 7},
					Level4: NestedLevel4{
						Level5: &NestedLevel5{Leaf: "bottom"},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "payload-nested",
		Payload: raw,
		Handler: NestedHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
