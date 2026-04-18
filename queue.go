package queue

import (
	adele "github.com/cidekar/adele-framework"
	"github.com/cidekar/adele-queue/api"
)

type (
	Queue         = api.Queue
	Job           = api.Job
	Configuration = api.Configuration
	Redis         = api.Redis
)

// New creates a new Queue instance wired to the given Adele application.
//
// Example:
//
//	q := queue.New(app)
//	q.Listen()
func New(a *adele.Adele) *Queue {
	return api.New(a)
}

// NewWithConfig creates a new Queue instance with a caller-supplied
// Configuration, bypassing any on-disk config/queue.yml. Intended for tests
// and programmatic setup.
func NewWithConfig(a *adele.Adele, config Configuration) *Queue {
	return api.NewWithConfig(a, config)
}
