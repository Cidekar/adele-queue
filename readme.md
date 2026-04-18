<p align="center"><img src="https://github.com/user-attachments/assets/2347ad25-9a6e-4d5f-b55b-81ee062874a2" width="400" alt="Adele Logo"></p>

# Adele Queue

A first-party queue package for the Adele Go framework. Ships as an Adele `ServiceProvider` with an in-memory backend for single-process workloads and a Redis-backed backend for multi-process deployments, plus worker pools, retry with backoff, failure tracking, and durable job persistence to the framework database.

## Supported Backends

| Backend | Description | Use Case |
|---|---|---|
| `memory` | Single-process channel-based queue with sync.WaitGroup-backed worker pool. | Local development, single-node deployments, short-lived jobs. |
| `redis` | Distributed queue backed by Redis hashes; workers cooperatively scan the keyspace for pending jobs and atomically rename keys between pending, locked, completed, and failed states. | Multi-process deployments, horizontal scaling, jobs that must survive process restarts. |

## Requirements

Requires Go 1.25 or later. If your local toolchain is older, set `GOTOOLCHAIN=auto` (Go 1.21+) to let the toolchain auto-upgrade.

## Quick Start

```bash
go get github.com/cidekar/adele-queue
```

Blank-import the package to auto-register the `ServiceProvider`:

```go
import (
    _ "github.com/cidekar/adele-queue"
)
```

The `ServiceProvider` loads configuration from `config/queue.yml` (seeded on first run from the embedded default), starts the worker pool during `Boot`, and wires into the framework's database session for durable job storage. The queue registers no HTTP routes.

## Configuration

Configuration comes from one of two sources:

- `$ROOT/config/queue.yml` — the YAML file, auto-seeded from the embedded default on first run.
- `app.Provider.SetProviderConfig("queue", map[string]interface{}{...})` at app bootstrap, which is forwarded to the provider's `Configure` method.

The provider accepts the following `SetProviderConfig` keys:

```go
app.Provider.SetProviderConfig("queue", map[string]interface{}{
    "backend":               "redis",
    "worker_count":          4,
    "max_attempts":          5,
    "high_water_mark":       10000,
    "queue_channels":        []string{"job", "email"},
    "queue_channel_default": "job",
    "debug":                 false,
    "redis_prefix":          "myapp",
    "redis_scan_interval":   1,
})
```

## Documentation

For complete documentation including configuration reference, backend behavior, dispatch and retry semantics, and worker lifecycle, see the [full documentation](./docs/queue.md).

## License

Copyright 2025 Cidekar, LLC. All rights reserved.

[Apache License 2.0](./LICENSE)
