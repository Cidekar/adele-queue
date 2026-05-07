package api

import (
	"log"
	"sync"
	"time"

	"github.com/cidekar/adele-framework/cache"
	"github.com/gomodule/redigo/redis"
	up "github.com/upper/db/v4"
)

// Configuration holds the queue server configuration loaded from queue.yml.
//
// Example (queue.yml):
//
//	Backend: memory
//	WorkerCount: 1
//	MaxAttempts: 3
//	HighWaterMark: 10000
//	QueueChannels:
//	  - job
//	QueueChannelDefault: job
//	Debug: false
type Configuration struct {
	Backend             string   `yaml:"Backend"`             // "memory" or "redis" — default "memory"
	WorkerCount         int      `yaml:"WorkerCount"`         // number of worker goroutines — default 1
	MaxAttempts         int      `yaml:"MaxAttempts"`         // total handler executions before permanent failure, counting the initial attempt — default 3 (initial + 2 retries)
	HighWaterMark       int      `yaml:"HighWaterMark"`       // in-memory failed-jobs cache size before reset — default 10000
	QueueChannels       []string `yaml:"QueueChannels"`       // named queue channels
	QueueChannelDefault string   `yaml:"QueueChannelDefault"` // default channel when Job.Queue is empty
	Debug               bool     `yaml:"Debug"`               // verbose logging
	RedisPrefix         string   `yaml:"RedisPrefix"`         // redis keyspace prefix
	RedisScanInterval   int      `yaml:"RedisScanInterval"`   // seconds between redis SCAN iterations — default 1
	LockTimeout         int      `yaml:"LockTimeout"`         // seconds; queue-wide default when Job.LockFor is 0
	ReaperInterval      int      `yaml:"ReaperInterval"`      // seconds; default 30
}

// Queue is used to hold the jobs that are being processed by the workers.
type Queue struct {
	Backend    string
	Cache      cache.Cache
	Debug      bool
	FailedJobs struct {
		mu   sync.RWMutex
		data []string
	}
	Jobs                chan Job
	Redis               Redis
	WorkerCount         int
	MaxAttempts         int
	HighWaterMark       int
	ErrorLog            *log.Logger
	InfoLog             *log.Logger
	QueueChannels       []string
	QueueChannelDefault string
	DB                  up.Session
	LockTimeout         int
	ReaperInterval      int
	// handlers is an in-process, name-keyed registry of handler functions.
	// Used by the redis backend, which cannot serialize the Job.Handler func
	// pointer. Not persisted; populated at application bootstrap via
	// RegisterHandler.
	handlers struct {
		mu sync.RWMutex
		m  map[string]func(payload interface{}) error
	}
}

// Redis holds the redis connection pool and keyspace-scan settings used by the
// redis backend.
type Redis struct {
	Pool         *redis.Pool
	Prefix       string
	ScanInterval int
}

// Job is the representation of a unit of work added to the queue. The payload
// is marshaled and passed between the main application and the queue.
type Job struct {
	ID             string                          `db:"job_id" json:"id"`
	Handler        func(payload interface{}) error `json:"-" redis:"-"`
	Name           string                          `db:"name" json:"name"`
	Payload        []byte                          `db:"payload,type=TEXT"`
	Retry          bool                            `json:"-"`
	RetryInSeconds int                             `json:"-"`
	RetryCounter   int                             `db:"attempts" json:"retryCounter"`
	ReservedAt     string                          `db:"-" json:"reservedAt"`
	RetryAfter     string                          `json:"-"`
	// DispatchAt schedules the earliest moment the job may run, in RFC3339
	// UTC format. A zero/empty value preserves immediate-dispatch behavior.
	// Honored by both the memory and redis backends; the redis scheduler
	// reuses the RetryAfter gate, the memory backend defers via goroutine.
	DispatchAt  string    `json:"dispatchAt,omitempty"`
	CompletedAt string    `json:"completedAt"`
	FailedAt    string    `json:"failedAt"`
	LockedAt    string    `json:"lockedAt,omitempty"`
	LockFor     int       `json:"lockFor,omitempty"`
	Queue       string    `json:"queue"`
	CreatedAt   time.Time `db:"created_at" redis:"-" json:"-"`
	UpdatedAt   time.Time `db:"updated_at" redis:"-" json:"-"`
	Exception   string    `db:"exception" json:"exception"`
	Status      string    `db:"-" json:"status"`
}
