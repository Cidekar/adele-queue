package benchjobs

import (
	"fmt"

	"github.com/cidekar/adele-queue/api"
)

func NilDerefHandler(payload interface{}) error {
	var p *int
	fmt.Println(*p)
	return nil
}

func NewNilDerefJob() (*api.Job, error) {
	return &api.Job{
		Name:    "fail-nil-deref",
		Payload: []byte(`{}`),
		Handler: NilDerefHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
