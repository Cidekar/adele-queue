# Adele Queue

Adele's Queue package provides a pluggable job queue for Adele applications. It ships as an Adele `ServiceProvider` — a blank import auto-registers it with the framework, loads configuration from `config/queue.yml`, and boots the worker pool. The package supports two backends: an in-process memory queue and a Redis-backed distributed queue.

## Installation

```bash
go get github.com/cidekar/adele-queue
```

Add a blank import to your application's entry point to auto-register the `ServiceProvider`:

```go
import _ "github.com/cidekar/adele-queue"
```

The provider's `Register` step constructs the queue and its `Boot` step calls `Listen`, starting the configured number of worker goroutines. The queue registers no HTTP routes — dispatch and retrieval is API-only.

## Configuration

Configuration comes from one of two sources:

- `$ROOT/config/queue.yml` — the YAML file, auto-seeded from the embedded default template on first run.
- `app.Provider.SetProviderConfig("queue", map[string]interface{}{...})` at app bootstrap, which is forwarded to the provider's `Configure` method.

The YAML file uses PascalCase keys; the `SetProviderConfig` map uses snake_case keys. Both map to the same underlying fields:

| YAML key (`queue.yml`) | `SetProviderConfig` key | Type | Default | Description |
|---|---|---|---|---|
| `Backend` | `backend` | string | `memory` | `memory` or `redis`. Any other value falls back to `memory`. |
| `WorkerCount` | `worker_count` | int | `1` | Number of worker goroutines the queue starts on `Listen`. |
| `MaxAttempts` | `max_attempts` | int | `3` | Maximum retry attempts before a job is marked permanently failed. |
| `HighWaterMark` | `high_water_mark` | int | `10000` | In-memory failed-jobs cache size before the slice is reset to prevent unbounded growth. |
| `QueueChannels` | `queue_channels` | []string | `[job]` | Named channels jobs may be dispatched to. The default channel is appended automatically if missing. |
| `QueueChannelDefault` | `queue_channel_default` | string | `job` | Channel used when `Job.Queue` is empty or unknown. |
| `Debug` | `debug` | bool | `false` | Verbose logging via the framework's logger. |
| `RedisPrefix` | `redis_prefix` | string | `""` | Redis keyspace prefix; inherited from the framework's redis cache when not set. |
| `RedisScanInterval` | `redis_scan_interval` | int | `1` | Seconds between redis `SCAN` iterations per worker. |

### Redis backend / RPC worker

When `Backend` is `redis`, workers deliver the marshaled job to the Adele RPC server. The queue resolves the port from `RPC_SERVER_PORT` (the canonical key shared with `adele-framework`), falling back to `RPC_PORT` for compatibility, and finally defaulting to `4040` when neither is set. Workers dial `127.0.0.1:<port>` for each dispatch. This is the only environment variable the queue consumes directly; set it in your process environment alongside whatever the framework's RPC server is bound to.

## Backends

### memory

The memory backend is a channel-based queue intended for single-process deployments. Workers receive `Job` values directly from the `Jobs` channel and invoke `Job.Handler(Payload)` in-process. Completed jobs are persisted to the `jobs` table; failed jobs that exhaust `MaxAttempts` are persisted to `failed_jobs`.

Trade-offs: zero dependencies, zero network round-trips, but jobs pending in the channel are lost if the process exits before the worker drains them.

### redis

The redis backend uses redis hashes keyed as `queues:<channel>:<state>:<job-id>` where `<state>` is one of `pending`, `locked`, `completed`, `failed`. Workers cooperatively `SCAN` the pending keyspace and `RENAME` matching keys to `locked` to claim a job. Payload delivery uses the Adele RPC server — each worker dials `127.0.0.1:$RPC_PORT` and pushes the marshaled job for the application to execute. On success the key moves to `completed`; on failure it either returns to `pending` with updated `RetryAfter` or moves to `failed` once `MaxAttempts` is reached.

Trade-offs: jobs survive process restarts, multiple processes can share the queue, but every dispatch and state transition is a redis round-trip.

## Dispatching a Job

```go
import (
    "github.com/cidekar/adele-queue"
    "github.com/cidekar/adele-queue/api"
)

type EmailPayload struct {
    To      string
    Subject string
    Body    string
}

func SendEmail(payload interface{}) error {
    p := payload.(EmailPayload)
    // ... send the email
    return nil
}

// Construct a queue directly (the ServiceProvider also maintains one
// accessible via *queue.ServiceProvider.Service()).
q := queue.New(app)

id, err := q.Dispatch(api.Job{
    Name:           "SendEmail",
    Handler:        SendEmail,
    Queue:          "email",
    Retry:          true,
    RetryInSeconds: 5,
})
```

`Dispatch` returns the generated UUID for the job. On the memory backend the job is pushed onto the channel synchronously and picked up by the next available worker; on the redis backend the job is persisted as a pending hash and picked up on the next scan cycle.

## Retry and Failure Behavior

A job participates in retry only when `Job.Retry` is `true`. When a handler returns an error:

1. The queue increments `RetryCounter`.
2. If `RetryCounter < MaxAttempts`, the job is re-queued after a backoff. On the memory backend the backoff is `RetryInSeconds * RetryCounter` (falling back to `RetryCounter` seconds when `RetryInSeconds` is unset). On the redis backend the backoff is encoded in `RetryAfter` and enforced by the scanner.
3. Once `RetryCounter` reaches `MaxAttempts`, the job is marked permanently failed. Its id is appended to the in-memory failure cache (bounded by `HighWaterMark`) and a row is inserted into the `failed_jobs` table.

Callers can introspect failures via `Queue.GetFailedJobsFromMemory()` (recent ids held in RAM) and `Queue.GetFailedJobs()` (durable records from the database).

## Worker Lifecycle

- `Listen()` — starts `WorkerCount` goroutines; called automatically by the provider's `Boot`.
- `Close(*sync.WaitGroup)` — stops new dispatches and waits for in-flight jobs to drain. Pass a non-nil `WaitGroup` when orchestrating shutdown across multiple subsystems; the queue will call `Done()` on your behalf. Closing the memory backend closes the jobs channel; closing the redis backend flips a process-wide shutdown flag that the scanner observes on its next iteration.

The queue does not install shutdown hooks. Applications are expected to call `Close` during graceful termination (for example, from the same `signal.Notify` handler that shuts down the HTTP server).

## Database Schema

The queue persists completed and failed jobs to two Postgres tables defined in `migrations/queue_tables.postgres.sql`. Run that migration against your database before dispatching jobs with a DB-backed workflow. Both tables include a `trigger_set_timestamp` trigger that keeps `updated_at` current, and an index on `job_id` for lookup by the UUID returned from `Dispatch`.
