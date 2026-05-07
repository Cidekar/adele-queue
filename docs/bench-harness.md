# Bench Harness

Audience: adele-queue maintainers.

## Why this exists

The default `go test ./...` suite covers the memory backend and the pressure
scenarios that can run without external services. It does **not** run against
a live redis — and the redis backend is where the reaper, the lock timeout,
and the distributed dispatch path live. The bench harness fills that gap: it
is an out-of-band CLI (gated behind the `queuebench` build tag) that boots
the full adele-framework, wires the queue provider to a real redis, registers
every bench job handler, dispatches a configurable workload, and then blocks
so the workers and reaper keep running.

This is deliberately not a unit-test harness. It is a maintainer tool for
chaos-engineering-style regressions (SIGKILL the worker, watch the reaper
recover; flood dispatch, watch backpressure; exhaust retries, watch the
permafail path).

## Job lifecycle — the big picture

Every job the queue dispatches passes through this keyspace state machine
in redis. `locked:` is the interesting state — it's where orphans happen
and where the reaper earns its keep.

```
                  ┌─────────────────────┐
                  │  consumer app       │
                  │  q.Dispatch(job)    │
                  └──────────┬──────────┘
                             │  HSET
                             ▼
         ┌─────────────────────────────────────────┐
         │  queues:<q>:pending:<id>                │
         │  status=pending                         │
         └──────────┬──────────────────────────────┘
                    │  scanner sees key,
                    │  RENAME pending → locked,
                    │  sets LockedAt
                    ▼
         ┌─────────────────────────────────────────┐
         │  queues:<q>:locked:<id>                 │
         │  status=processing                      │
         │  ◄── handler runs here                  │
         └────┬───────────────────────────┬────────┘
              │                           │
       handler │                           │ handler err
       returns nil                        │ or panic
              │                           │
              ▼                           ▼
  ┌──────────────────────┐   ┌───────────────────────────────┐
  │  completed:<id>      │   │  retry branch                 │
  │  terminal            │   │  (Retry && attempts<max)      │
  │  Exception cleared   │   │  → back to pending: with      │
  └──────────────────────┘   │    RetryAfter gate            │
                             │  OR                           │
                             │  permafail branch             │
                             │  → failed:<id>,               │
                             │    failed_jobs row            │
                             └───────────────────────────────┘

        ╔═══════════════════════════════════════════════════╗
        ║  and separately, every reaper_interval seconds:   ║
        ║                                                   ║
        ║  reaper scans queues:*:locked:*                   ║
        ║   │                                               ║
        ║   ├── LockedAt age < effective timeout → skip     ║
        ║   │                                               ║
        ║   └── age > timeout:                              ║
        ║        ├── pending:<id> already exists →          ║
        ║        │    DEL the leaked locked: (dupe)         ║
        ║        └── no pending sibling →                   ║
        ║             route through retry/permafail,        ║
        ║             Exception="lock expired after Ns"     ║
        ╚═══════════════════════════════════════════════════╝
```

Effective timeout per job = `Job.LockFor` if set (> 0), else
`q.LockTimeout` (queue default). This lets a 12-hour batch job override a
300s fleet-wide default without dragging the rest of the queue's recovery
lag up with it.

## Client flows — pick the one that matches what you're testing

The harness has three entry points. Each exercises a different slice of
the queue.

### `make bench:seed` — behavioral sweep

Dispatches exactly one of each of the 15 job types in `benchjobs/`.
Useful for "did I break any handler shape?" regressions.

```
  queuebench --seed-jobs
        │
        ▼
  RegisterAll(q)          ← wires 15 handlers by Name
        │
        ▼
  SeedAll(q) ──► 15 × q.Dispatch(job)        (100ms spacing)
        │                │
        ▼                ▼
  select{} blocks   redis fills with
  (reaper + workers  queues:queuebench:job:pending:*
   keep running)          │
                          ▼
                     workers claim, run, drain to
                     completed: / failed:
```

What to watch for in the logs: each handler's `fmt.Println` line
(e.g. `hello, <name>`, `slept Nms`, `alwaysError: N total...`). Any
`panic` lines should be followed by `reaper` or `handleRedisJob` picking
the job back up.

### `make bench:stress` — throughput + retry storm

Dispatches 1000 mixed jobs (60% hello, 20% sleep, 10% fail-w-retry,
5% panic, 5% busy-loop) through 16 dispatcher goroutines. Measures
dispatch rate and lets retries play out.

```
  queuebench --stress-jobs=1000 --stress-concurrency=16 --stress-mode=mixed
        │
        ▼
  16 dispatcher goroutines in parallel
        │
        ├──► fanout to redis queues:queuebench:job:pending:*
        │
        ▼
  4 worker goroutines pull locked keys, invoke handlers
        │
        ▼
  failing jobs (fail-error, panic) → retry branch → back to pending:
        │
        ▼
  after max_attempts=5 → failed: + failed_jobs row
        │
        ▼
  select{} blocks; final stats:
    - dispatched=1000, rate=N/s, peakGoroutines=X
    - alwaysError total handler invocations: ~500 (100 fail jobs × 5 attempts)
    - reaper ticks quietly: scanned=N recent=N dupes=0
```

What "healthy" looks like: `alwaysError total handler invocations` climbs
past `dispatched × fail_ratio × max_attempts`; reaper logs report `errs=0`;
no `permafailed` from the reaper (locks drain naturally within 10s).

### `make bench:reaper-test` — crash recovery

Orchestrates a SIGKILL and asserts the reaper rescues the orphan. This is
the regression test for the reaper itself.

```
  ┌─────────── phase 1 ─────────────┐    ┌─────────── phase 2 ───────────┐
  │                                 │    │                               │
  │  queuebench --long-sleep=60 &   │    │  queuebench &                 │
  │        │                        │    │        │                      │
  │        ▼                        │    │        ▼                      │
  │  Dispatch one sleep job         │    │  Fresh process — no dispatch, │
  │        │                        │    │  just workers + reaper.       │
  │        ▼                        │    │        │                      │
  │  Worker claims, sleeps in       │    │  Reaper scans locked: every   │
  │  handler for 60s.               │    │  5s. After ~10s, LockedAt age │
  │        │                        │    │  exceeds lock_timeout. No     │
  │  Redis state:                   │    │  pending: sibling → reaper    │
  │  queues:queuebench:             │    │  routes it:                   │
  │    :locked:<id>    = 1          │    │                               │
  │    :pending:<id>   = 0          │    │  Retry:false (sleep job)      │
  │        │                        │    │  → permafail path → failed:   │
  │  kill -9 <pid>                  │    │                               │
  │        │                        │    │  reaper log:                  │
  │        ▼                        │    │  "scanned=1 permafailed=1"    │
  │  Process dies mid-handler.      │    │                               │
  │  Redis key stays in locked:     │    │  Make target asserts this log │
  │  because there's no graceful    │    │  line exists → exits 0.       │
  │  shutdown path to clean it.     │    │                               │
  │                                 │    │                               │
  │  Orphan count in redis:         │    │                               │
  │  locked: = 1 (unowned)          │    │                               │
  └─────────────────────────────────┘    └───────────────────────────────┘
```

The `bench:reaper-test` target wraps both phases in a script that captures
stdout/stderr to `/tmp/queuebench-phase1.log` and `.../phase2.log`, greps
phase 2 for `permafailed=[1-9]`, and exits nonzero if the signal never
fires. Run it after any change that touches `reaper.go`, `redis.go`
scanner, or the lock state machine.

## Prerequisites

- Go 1.25+ (or older with `GOTOOLCHAIN=auto`).
- A reachable redis instance. The Makefile defaults assume a local docker
  container named `adele_garbage-redis-1`; override via
  `REDIS_CONTAINER=my-redis make bench:seed`. If redis is on a host port
  rather than in a container, set `REDIS_HOST` / `REDIS_PORT` in the env
  file below — the `docker exec` calls are only used by `bench:reaper-test`
  and `bench:clean`.
- Optional: Postgres, if you want failed-jobs persistence. See the commented
  `DATABASE_*` block in `.env.queuebench.example`.

## Setup

```
cp .env.queuebench.example .env.queuebench
$EDITOR .env.queuebench
```

`.env.queuebench` is gitignored. Populate it before running any `bench:*`
target.

## Running

See `make bench:help` for the full target list. The short version:

- `make bench:build` — compile `bin/queuebench` with the `queuebench` tag.
- `make bench:seed` — dispatch one of each of the 15 job types; blocks.
- `make bench:stress` — dispatch 1000 mixed jobs (concurrency 16); blocks.
- `make bench:long-sleep` — dispatch one 60s sleep job; blocks. Useful for
  manual SIGKILL experiments.
- `make bench:reaper-test` — orchestrated reaper regression. Phase 1 starts
  the harness with a long-sleep job, waits until redis shows a locked key,
  then `kill -9`s the process. Phase 2 starts a fresh harness and asserts
  the reaper logs `permafailed=[1-9]` within 20 seconds.
- `make bench:clean` — remove `bin/` and flush all `queues:queuebench:*`
  keys from redis.

## How this differs from `make test:bench`

`make test:bench` runs `go test -bench` against the memory backend — in-
process, no redis, no reaper. Those numbers measure dispatch-loop
throughput. `make bench:*` runs the full redis-backed service end-to-end,
including key renames between pending/locked/completed/failed, reaper
sweeps of stale locks, and the retry-with-backoff state machine. Use
`test:bench` for code-change regression numbers; use `bench:*` for
behavior validation against real infrastructure.

## Harness defaults

The queuebench provider config sets `lock_timeout=10` and
`reaper_interval=5` (seconds). The adele-queue production defaults are
`300` and `30`. The short values let maintainers observe a reaper cycle in
seconds instead of minutes. Consumer applications should configure the
production values via `SetProviderConfig` — these are harness-only.

Other harness-pinned config:

- `backend: redis`
- `worker_count: 4`
- `max_attempts: 5`
- `high_water_mark: 10000`
- `queue_channels: [job, email]`, default `job`
- `redis_prefix: queuebench` (keeps harness keys isolated from other apps
  sharing the redis instance)
- `redis_scan_interval: 1`

## Troubleshooting

- **`docker exec: No such container`** — redis isn't in the default
  container. Either start it (`docker start adele_garbage-redis-1`) or
  override the name: `REDIS_CONTAINER=my-redis make bench:seed`.
- **`.env.queuebench not loaded`** — the harness logs a warning and
  continues. If you expected env vars from the file, confirm the file
  exists at repo root and that `REDIS_HOST` / `REDIS_PORT` are set.
- **`bench:reaper-test: no 'permafailed>=1' in reaper log`** — most likely
  the reaper didn't see the locked key within phase 1's 3-second window,
  or phase 2 exited before the 20-second reaper sweep. Re-run; if it keeps
  failing, inspect `/tmp/queuebench-phase1.log` and
  `/tmp/queuebench-phase2.log`.
- **`FAIL: no locked keys observed`** — phase 1's worker didn't pick up
  the sleep job before the kill fired. Confirm redis is empty before the
  run (`make bench:clean`) and that worker_count > 0 in the harness
  config.
- **Dispatch errors stuck on `connection refused`** — redis isn't
  reachable on the host/port in `.env.queuebench`. Test with
  `redis-cli -h $REDIS_HOST -p $REDIS_PORT ping`.
