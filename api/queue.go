package api

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	adele "github.com/cidekar/adele-framework"
	"github.com/cidekar/adele-framework/cache/redisdriver"
	"github.com/gomodule/redigo/redis"
	"github.com/google/uuid"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v2"
)

// Bool to hold value to shut down cursor scan when the application is shutdown.
var systemShutdown bool

// time format to use when working with the queue system.
const queueTimeFormat = time.RFC3339

// WaitGroup to track worker goroutines.
var wg sync.WaitGroup

//go:embed templates
var templateFS embed.FS

// New creates a new Queue instance with the given Adele application. It loads
// configuration from config/queue.yml (or the embedded default if no on-disk
// copy exists), applies sensible defaults, and wires the framework's loggers,
// cache, and database session onto the queue.
//
// Example:
//
//	q, err := api.New(app)
//	if err != nil {
//	    return err
//	}
//	q.Listen()
func New(a *adele.Adele) (*Queue, error) {
	cfg, err := loadConfig(a)
	if err != nil {
		return nil, fmt.Errorf("queue: load config: %w", err)
	}
	return buildQueue(a, *cfg), nil
}

// NewWithConfig creates a new Queue instance with a caller-supplied
// configuration. Used primarily for testing or programmatic overrides.
//
// Example:
//
//	cfg := api.Configuration{Backend: "memory", WorkerCount: 2}
//	q, err := api.NewWithConfig(app, cfg)
//	if err != nil {
//	    return err
//	}
func NewWithConfig(a *adele.Adele, config Configuration) (*Queue, error) {
	return buildQueue(a, config), nil
}

// buildQueue assembles a *Queue from the given configuration and Adele
// application. It applies defaults, wires loggers, cache, DB session, and
// (when redis is selected as the backend) a redis connection pool.
func buildQueue(a *adele.Adele, cfg Configuration) *Queue {
	setConfigDefaults(&cfg)

	q := &Queue{
		Backend:             cfg.Backend,
		Debug:               cfg.Debug,
		WorkerCount:         cfg.WorkerCount,
		MaxAttempts:         cfg.MaxAttempts,
		HighWaterMark:       cfg.HighWaterMark,
		QueueChannels:       cfg.QueueChannels,
		QueueChannelDefault: cfg.QueueChannelDefault,
		LockTimeout:         cfg.LockTimeout,
		ReaperInterval:      cfg.ReaperInterval,
		Redis: Redis{
			Prefix:       cfg.RedisPrefix,
			ScanInterval: cfg.RedisScanInterval,
		},
	}

	// Wire framework loggers. The published adele-framework exposes a logrus
	// logger via a.Log; wrap it in stdlib log.Logger so the queue can use
	// plain Println/Printf calls without taking a logrus dependency.
	if a != nil && a.Log != nil {
		q.InfoLog = log.New(a.Log.Writer(), "", 0)
		q.ErrorLog = log.New(a.Log.Writer(), "", 0)
	} else {
		q.InfoLog = log.New(os.Stdout, "", log.LstdFlags)
		q.ErrorLog = log.New(os.Stderr, "", log.LstdFlags)
	}

	// Wire the cache so callers can share state with the app.
	if a != nil {
		q.Cache = a.Cache
	}

	// Wire the database session so completed/failed jobs can be persisted.
	if a != nil && a.DB != nil {
		q.DB = a.DB.NewSession()
	}

	// Resolve the redis pool when the redis backend is selected.
	if q.Backend == "redis" {
		if a != nil && a.Cache != nil {
			if rc, ok := a.Cache.(*redisdriver.RedisCache); ok && rc != nil {
				q.Redis.Pool = rc.Conn
				if q.Redis.Prefix == "" {
					q.Redis.Prefix = rc.Prefix
				}
			}
		}
	}

	q.Jobs = make(chan Job)

	// Pre-allocate the failed-jobs slice with the configured high water mark
	// to minimize reallocations under load.
	q.FailedJobs.data = make([]string, 0, q.HighWaterMark)

	// Initialize the name-keyed handler registry. Consumers populate this at
	// bootstrap via RegisterHandler; the redis backend looks up handlers here
	// because Job.Handler cannot survive redis serialization.
	q.handlers.m = make(map[string]func(payload interface{}) error)

	// Guarantee the default channel is present in the configured channel list.
	if !slices.Contains(q.QueueChannels, q.QueueChannelDefault) {
		q.QueueChannels = append(q.QueueChannels, q.QueueChannelDefault)
	}

	return q
}

// setConfigDefaults applies sensible defaults to a Configuration. Any field
// left at its zero value is filled in with the documented default.
func setConfigDefaults(c *Configuration) {
	if c.Backend == "" {
		c.Backend = "memory"
	} else if c.Backend != "memory" && c.Backend != "redis" {
		c.Backend = "memory"
	}
	if c.WorkerCount == 0 {
		c.WorkerCount = 1
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.HighWaterMark == 0 {
		c.HighWaterMark = 10000
	}
	if c.RedisScanInterval == 0 {
		c.RedisScanInterval = 1
	}
	if c.QueueChannelDefault == "" {
		c.QueueChannelDefault = "job"
	}
	if c.LockTimeout <= 0 {
		c.LockTimeout = 300
	}
	if c.ReaperInterval <= 0 {
		c.ReaperInterval = 30
	}
	if c.ReaperInterval >= c.LockTimeout {
		c.ReaperInterval = c.LockTimeout / 2
		if c.ReaperInterval < 1 {
			c.ReaperInterval = 1
		}
	}
}

// loadConfig reads queue.yml from the consumer's $ROOT/config directory,
// falling back to — and seeding from — the embedded default when no on-disk
// copy exists. Returns the parsed Configuration.
func loadConfig(a *adele.Adele) (*Configuration, error) {
	if a == nil {
		var cfg Configuration
		data, err := templateFS.ReadFile("templates/queue.yml")
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded queue config: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse embedded queue config: %w", err)
		}
		return &cfg, nil
	}

	configPath := a.RootPath + "/config/queue.yml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		data, err := templateFS.ReadFile("templates/queue.yml")
		if err != nil {
			return nil, fmt.Errorf("failed to read package queue config from embedded file system: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			return nil, fmt.Errorf("queue: create config dir: %w", err)
		}
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to write package queue config to disk: %w", err)
		}
	}

	configFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read queue config file: %w", err)
	}

	var cfg Configuration
	if err := yaml.Unmarshal(configFile, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse queue config file: %w", err)
	}

	return &cfg, nil
}

// Close closes the queue's job channel so that no more jobs can be added to
// it and waits for workers to complete before returning. When called with a
// non-nil waitgroup (redis backend shutdown case) it signals Done on behalf of
// the caller.
func (q *Queue) Close(mWG *sync.WaitGroup) {
	if q.Backend == "redis" {
		if mWG != nil {
			defer mWG.Done()
		}

		// Cancel the cursor scan
		systemShutdown = true

		// Wait for workers + reaper to drain, bounded by one reaper tick plus
		// a small grace period. A worker blocked on redis I/O will trigger
		// the timeout log so operators can investigate hung connections.
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Duration(q.ReaperInterval+5) * time.Second):
			q.ErrorLog.Println("reaper: shutdown timeout; goroutines may still be running")
		}

		if q.Debug {
			q.InfoLog.Printf("queue gracefully shutdown\n")
		}
	}

	if q.Backend == "memory" {
		// Do not allow any new jobs to be added to the queue
		close(q.Jobs)

		// Wait for workers to process any jobs that were picked up
		wg.Wait()
	}
}

// Listen starts the configured number of worker goroutines, each of which
// processes jobs until the queue is closed.
func (q *Queue) Listen() {
	for i := 1; i <= q.WorkerCount; i++ {
		wg.Add(1)
		go q.worker(i, q.Jobs, &wg)
	}

	if q.Backend == "redis" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.reapStaleLocks()
		}()
	}
}

// worker is the processing loop that pulls jobs from jobChannel (memory
// backend) or scans redis (redis backend) and signals completion via the
// WaitGroup.
func (q *Queue) worker(id int, jobChannel chan Job, wg *sync.WaitGroup) {
	defer wg.Done()

	if q.Backend == "memory" {
		for job := range jobChannel {
			// Skip jobs that did not register a handler
			if job.Handler != nil {
				q.runHandler(job, id)
			}
		}
	}

	if q.Backend == "redis" {
		c := q.Redis.Pool.Get()
		defer c.Close()

		q.cursorScanRedisDb(c, id)
	}
}

// runHandler invokes a job's handler with panic recovery so a single bad
// job cannot crash the worker goroutine. A recovered panic is recorded as
// a failure on the job's exception field.
func (q *Queue) runHandler(job Job, id int) {
	defer func() {
		if r := recover(); r != nil {
			job.Exception = fmt.Sprintf("panic: %v", r)
			q.addFailedJob(job, id)
		}
	}()
	if err := job.Handler(job.Payload); err != nil {
		job.Exception = err.Error()
		q.addFailedJob(job, id)
		return
	}
	q.addCompletedJob(job, id)
}

// RegisterHandler binds a handler function to a job name for the in-process
// dispatch registry used by the redis backend. Consumers call this at
// application bootstrap, once per unique Job.Name they will dispatch.
//
// The memory backend does not use the registry — it invokes Job.Handler
// directly — so registration is only required when the redis backend is
// active. Registering defensively in both cases is safe and recommended.
//
// Returns an error on empty name, nil fn, or duplicate registration.
//
// Example:
//
//	q.RegisterHandler("hello", helloHandler)
func (q *Queue) RegisterHandler(name string, fn func(payload interface{}) error) error {
	if name == "" {
		return fmt.Errorf("queue: RegisterHandler requires a non-empty name")
	}
	if fn == nil {
		return fmt.Errorf("queue: RegisterHandler requires a non-nil function for %q", name)
	}
	q.handlers.mu.Lock()
	defer q.handlers.mu.Unlock()
	if q.handlers.m == nil {
		q.handlers.m = make(map[string]func(payload interface{}) error)
	}
	if _, exists := q.handlers.m[name]; exists {
		return fmt.Errorf("queue: handler %q already registered", name)
	}
	q.handlers.m[name] = fn
	return nil
}

// Dispatch adds a job to the queue and returns the id of the job.
func (q *Queue) Dispatch(job Job) (string, error) {
	job.ID = uuid.Must(uuid.NewRandom()).String()
	now := time.Now().UTC().Format(queueTimeFormat)
	job.ReservedAt = now
	job.RetryAfter = now
	job.Status = "pending"

	switch q.Backend {
	case "memory":
		if job.Queue == "" {
			job.Queue = q.QueueChannelDefault
		}
		q.Jobs <- job
		return job.ID, nil

	case "redis":
		conn := q.Redis.Pool.Get()
		defer conn.Close()

		if job.Queue == "" {
			job.Queue = q.QueueChannelDefault
		} else {
			channel := q.RedisKeyspaceFormater(job.Queue)
			if slices.Contains(q.QueueChannels, channel) {
				job.Queue = channel
			} else {
				job.Queue = q.QueueChannelDefault
			}
		}
		_, err := conn.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatPending, job.Queue, job.ID)).AddFlat(&job)...)
		if err != nil {
			q.ErrorLog.Println("unexpected error adding job to queue:", err)
			return job.ID, err
		}

		if q.Debug {
			q.InfoLog.Printf("job %s added to %s queue\n", job.ID, job.Queue)
		}

		return job.ID, nil
	}

	return "", errors.New("unknown queue backend configuration")
}

// UnmarshalPayload unmarshals a job from redis into a Job value suitable for
// processing by the adele RPC client.
func (q *Queue) UnmarshalPayload(cachedJob []byte) (*Job, error) {
	var job Job
	if err := json.Unmarshal(cachedJob, &job); err != nil {
		return nil, err
	}
	return &job, nil
}
