package api

import (
	"context"
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
	// bootstrap via RegisterHandler or RegisterHandlerCtx; the redis backend
	// looks up handlers here because Job.Handler cannot survive redis
	// serialization.
	q.handlers.m = make(map[string]registeredHandler)

	// Initialize the queue lifecycle context. Cancelled by Close so any
	// in-flight context-aware handlers can observe shutdown via ctx.Done().
	q.lifecycleCtx, q.lifecycleCancel = context.WithCancel(context.Background())

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
//
// Close also cancels the queue lifecycle context, which is the context passed
// to handlers registered via RegisterHandlerCtx. Context-aware handlers that
// honor ctx.Done() can therefore unblock immediately on shutdown instead of
// running to completion against a torn-down system.
func (q *Queue) Close(mWG *sync.WaitGroup) {
	// Signal lifecycle shutdown to context-aware handlers as the very first
	// step so any in-flight handler observing ctx.Done() can begin to unwind
	// before the worker drain logic below starts waiting for it.
	if q.lifecycleCancel != nil {
		q.lifecycleCancel()
	}

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
	// Always-on boot log so consumers can confirm the queue is alive without
	// having to enable Debug. Single line, backend + worker count.
	q.InfoLog.Printf("queue: listening backend=%s workers=%d", q.Backend, q.WorkerCount)

	if q.Debug {
		switch q.Backend {
		case "redis":
			q.InfoLog.Printf("queue: redis pool resolved prefix=%s scan_interval=%ds", q.Redis.Prefix, q.Redis.ScanInterval)
		case "memory":
			q.InfoLog.Printf("queue: memory channel ready high_water_mark=%d", q.HighWaterMark)
		}
	}

	for i := 1; i <= q.WorkerCount; i++ {
		wg.Add(1)
		go q.worker(i, q.Jobs, &wg)
		if q.Debug {
			q.InfoLog.Printf("queue: worker %d started", i)
		}
	}

	if q.Backend == "redis" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.reapStaleLocks()
		}()
		if q.Debug {
			q.InfoLog.Printf("queue: reaper started lock_timeout=%ds reaper_interval=%ds", q.LockTimeout, q.ReaperInterval)
		}
	}
}

// worker is the processing loop that pulls jobs from jobChannel (memory
// backend) or scans redis (redis backend) and signals completion via the
// WaitGroup.
func (q *Queue) worker(id int, jobChannel chan Job, wg *sync.WaitGroup) {
	defer wg.Done()

	if q.Backend == "memory" {
		for job := range jobChannel {
			// A job is runnable if it ships a Job.Handler inline OR has a
			// matching entry (plain or ctx) in the registry. The registry
			// path is the only way to surface ctx-aware handlers, since
			// Job.Handler is the legacy plain shape.
			if job.Handler != nil || q.hasRegisteredHandler(job.Name) {
				q.runHandler(job, id)
			}
			// Decrement after the handler returns (success, failure, or
			// skipped-no-handler). This mirrors the redis backend's
			// :pending:* keyspace, which is removed only once a job moves
			// out of the pending state.
			q.pendingCount.Add(-1)
		}
	}

	if q.Backend == "redis" {
		c := q.Redis.Pool.Get()
		defer c.Close()

		q.cursorScanRedisDb(c, id)
	}
}

// hasRegisteredHandler reports whether a non-empty registry entry exists
// for the given job name. Used by the memory worker loop to admit jobs
// that rely on registry dispatch (notably ctx-aware handlers, which are
// only reachable through the registry).
func (q *Queue) hasRegisteredHandler(name string) bool {
	q.handlers.mu.RLock()
	defer q.handlers.mu.RUnlock()
	reg, ok := q.handlers.m[name]
	if !ok {
		return false
	}
	return reg.plain != nil || reg.ctx != nil
}

// runHandler invokes a job's handler with panic recovery so a single bad
// job cannot crash the worker goroutine. A recovered panic is recorded as
// a failure on the job's exception field.
//
// The memory backend stores handlers directly on Job.Handler (the legacy
// plain shape). When a job name has also been registered via
// RegisterHandlerCtx, the registered ctx variant takes precedence and is
// invoked with the queue lifecycle context so it can short-circuit on
// shutdown. Per-job timeouts are the consumer's responsibility via
// context.WithTimeout inside the handler.
func (q *Queue) runHandler(job Job, id int) {
	defer func() {
		if r := recover(); r != nil {
			job.Exception = fmt.Sprintf("panic: %v", r)
			q.addFailedJob(job, id)
		}
	}()

	var handlerErr error

	// Prefer a context-aware registration when one exists for this job
	// name. Falling back to the plain registry (or to Job.Handler in the
	// memory dispatch path) preserves existing behavior for handlers that
	// have not opted in.
	q.handlers.mu.RLock()
	reg, ok := q.handlers.m[job.Name]
	q.handlers.mu.RUnlock()

	switch {
	case ok && reg.ctx != nil:
		handlerErr = reg.ctx(q.lifecycleCtx, job.Payload)
	case ok && reg.plain != nil:
		handlerErr = reg.plain(job.Payload)
	default:
		// No registry entry: fall back to the inline Job.Handler set on
		// the dispatched job. This is the historical memory-backend path.
		handlerErr = job.Handler(job.Payload)
	}

	if handlerErr != nil {
		job.Exception = handlerErr.Error()
		q.addFailedJob(job, id)
		return
	}
	q.addCompletedJob(job, id)
}

// RegisterHandler binds a handler function to a job name for the in-process
// dispatch registry used by the redis backend. Consumers call this at
// application bootstrap, once per unique Job.Name they will dispatch.
//
// The memory backend does not use the registry for jobs that ship a
// Job.Handler inline; for those, registration is only required when the
// redis backend is active. Registering defensively in both cases is safe
// and recommended. When a name is registered, the registry takes precedence
// over Job.Handler in the memory dispatch path as well.
//
// Returns an error on empty name, nil fn, or duplicate registration. A name
// previously registered via RegisterHandlerCtx is also rejected so the
// registry remains a single source of truth (one handler per name).
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
		q.handlers.m = make(map[string]registeredHandler)
	}
	if _, exists := q.handlers.m[name]; exists {
		return fmt.Errorf("queue: handler %q already registered", name)
	}
	q.handlers.m[name] = registeredHandler{plain: fn}
	return nil
}

// RegisterHandlerCtx is the context-aware variant of RegisterHandler. The
// handler receives a context that is cancelled when the queue is shutting
// down (Close called), so handlers can short-circuit long downstream calls
// instead of running to completion against a torn-down system.
//
// Per-job timeouts can be applied via context.WithTimeout inside the handler.
//
// Both RegisterHandler and RegisterHandlerCtx may coexist on the same Queue;
// each name registers exactly one handler regardless of which method was used.
func (q *Queue) RegisterHandlerCtx(name string, fn func(ctx context.Context, payload interface{}) error) error {
	if name == "" {
		return fmt.Errorf("queue: RegisterHandlerCtx requires a non-empty name")
	}
	if fn == nil {
		return fmt.Errorf("queue: RegisterHandlerCtx requires a non-nil function for %q", name)
	}
	q.handlers.mu.Lock()
	defer q.handlers.mu.Unlock()
	if q.handlers.m == nil {
		q.handlers.m = make(map[string]registeredHandler)
	}
	if _, exists := q.handlers.m[name]; exists {
		return fmt.Errorf("queue: handler %q already registered", name)
	}
	q.handlers.m[name] = registeredHandler{ctx: fn}
	return nil
}

// Dispatch adds a job to the queue and returns the id of the job.
//
// When Job.DispatchAt is a non-empty RFC3339 timestamp, dispatch is deferred
// until that moment: the redis backend seeds RetryAfter so the scanner gates
// the job, and the memory backend dispatches via a detached goroutine that
// sleeps until the target time. An unparseable DispatchAt returns an error.
func (q *Queue) Dispatch(job Job) (string, error) {
	job.ID = uuid.Must(uuid.NewRandom()).String()
	now := time.Now().UTC().Format(queueTimeFormat)
	job.ReservedAt = now
	if job.DispatchAt != "" {
		job.RetryAfter = job.DispatchAt
	} else {
		job.RetryAfter = now
	}
	job.Status = "pending"

	switch q.Backend {
	case "memory":
		if job.Queue == "" {
			job.Queue = q.QueueChannelDefault
		}
		if job.DispatchAt != "" {
			when, err := time.Parse(queueTimeFormat, job.DispatchAt)
			if err != nil {
				return "", fmt.Errorf("queue: invalid DispatchAt %q: %w", job.DispatchAt, err)
			}
			delay := time.Until(when)
			if delay > 0 {
				// Detached send so a future-scheduled job does not block the
				// caller or stall other dispatchers on the unbuffered channel.
				// pendingCount is incremented just before the send — Depth()
				// must observe the job as pending the moment a worker can
				// receive it, otherwise the worker's post-handler decrement
				// could race the increment and drive the count negative.
				go func(j Job, d time.Duration) {
					time.Sleep(d)
					q.pendingCount.Add(1)
					q.Jobs <- j
				}(job, delay)
				return job.ID, nil
			}
		}
		q.pendingCount.Add(1)
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

// DispatchIn schedules a job to run no sooner than `delay` from now.
// Equivalent to setting Job.DispatchAt = now+delay (RFC3339) and calling
// Dispatch. A non-positive delay falls through to immediate dispatch.
func (q *Queue) DispatchIn(job Job, delay time.Duration) (string, error) {
	if delay > 0 {
		job.DispatchAt = time.Now().UTC().Add(delay).Format(queueTimeFormat)
	}
	return q.Dispatch(job)
}

// Depth returns the total number of jobs in pending state across every
// configured QueueChannel. Used by consumers that want to gate dispatch on
// queue backpressure (return 503 + Retry-After when the worker pool is
// over a high-water mark).
//
// For the memory backend, returns the count of jobs that have been
// Dispatched but not yet fully processed by a worker (incremented on
// Dispatch, decremented after the handler returns). The Jobs channel is
// unbuffered, so len(q.Jobs) cannot be used as a backpressure signal —
// the queue maintains an atomic counter instead.
//
// For the redis backend, SCANs queues:<channel>:pending:* across every
// channel in q.QueueChannels. The SCAN is cursor-based and bounded; on a
// healthy queue the call returns in single-digit milliseconds.
//
// Returns an error if the redis pool is unavailable or the SCAN itself
// errors. Safe to call concurrently with Dispatch and Listen.
//
// Note: the redis match pattern is built from the literal "queues:" prefix
// used by formatPending. q.Redis.Prefix is not currently woven into the
// pending-key format strings; if that wiring changes in the future, this
// method must follow.
func (q *Queue) Depth() (int, error) {
	switch q.Backend {
	case "memory":
		return int(q.pendingCount.Load()), nil

	case "redis":
		if q.Redis.Pool == nil {
			return 0, errors.New("queue: redis pool is unavailable")
		}

		conn := q.Redis.Pool.Get()
		defer conn.Close()

		if err := conn.Err(); err != nil {
			return 0, fmt.Errorf("queue: redis connection unavailable: %w", err)
		}

		total := 0
		for _, channel := range q.QueueChannels {
			match := fmt.Sprintf("queues:%s:pending:*", channel)
			cursor := "0"
			for {
				reply, err := redis.Values(conn.Do("SCAN", cursor, "MATCH", match, "COUNT", 100))
				if err != nil {
					return 0, fmt.Errorf("queue: redis SCAN failed for channel %q: %w", channel, err)
				}
				if len(reply) != 2 {
					return 0, fmt.Errorf("queue: unexpected SCAN reply shape for channel %q", channel)
				}

				nextCursor, err := redis.String(reply[0], nil)
				if err != nil {
					return 0, fmt.Errorf("queue: parse SCAN cursor for channel %q: %w", channel, err)
				}
				keys, err := redis.Strings(reply[1], nil)
				if err != nil {
					return 0, fmt.Errorf("queue: parse SCAN keys for channel %q: %w", channel, err)
				}

				total += len(keys)

				if nextCursor == "0" {
					break
				}
				cursor = nextCursor
			}
		}
		return total, nil
	}

	return 0, errors.New("queue: unknown backend")
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
