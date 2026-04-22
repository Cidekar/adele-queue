package api

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gomodule/redigo/redis"
)

// Reaper observability counters. Package-level, atomic, zero-cost for memory
// backend (never incremented). Exposed via ReaperStats() for operators and
// tests; also logged at Info level at the end of every tick.
var (
	reaperTicks           atomic.Uint64 // total reaper ticks completed
	reaperScannedKeys     atomic.Uint64 // cumulative locked keys inspected
	reaperRequeued        atomic.Uint64 // cumulative requeues to pending:
	reaperPermafailed     atomic.Uint64 // cumulative moves to failed:
	reaperSkippedRecent   atomic.Uint64 // inspected but within timeout
	reaperSkippedDupePend atomic.Uint64 // skipped because pending: copy already existed (concurrent re-entry guard)
	reaperErrors          atomic.Uint64 // errors during scan or transition
)

// ReaperStats returns a snapshot of reaper counters. Intended for tests,
// operator debugging, and metrics export.
type ReaperStats struct {
	Ticks              uint64
	ScannedKeys        uint64
	Requeued           uint64
	Permafailed        uint64
	SkippedRecent      uint64
	SkippedDupePending uint64
	Errors             uint64
}

func (q *Queue) ReaperStats() ReaperStats {
	return ReaperStats{
		Ticks:              reaperTicks.Load(),
		ScannedKeys:        reaperScannedKeys.Load(),
		Requeued:           reaperRequeued.Load(),
		Permafailed:        reaperPermafailed.Load(),
		SkippedRecent:      reaperSkippedRecent.Load(),
		SkippedDupePending: reaperSkippedDupePend.Load(),
		Errors:             reaperErrors.Load(),
	}
}

// reapStaleLocks periodically scans queues:*:locked:* and requeues or fails
// jobs whose lock age exceeds Job.LockFor (or q.LockTimeout as fallback).
// Exits on systemShutdown. Only called for the redis backend (guarded at
// the Listen() call site).
//
// Connection strategy: pulls a fresh connection per tick and releases it at
// tick end. Never holds a connection across ticker waits. Fixes the
// slow-call-deadlocks-reaper risk.
func (q *Queue) reapStaleLocks() {
	ticker := time.NewTicker(time.Duration(q.ReaperInterval) * time.Second)
	defer ticker.Stop()

	for {
		if systemShutdown {
			return
		}
		<-ticker.C
		if systemShutdown {
			return
		}

		q.runReaperTick()
	}
}

// runReaperTick performs one scan pass. Isolated for testability and to
// bound connection lifetime to a single tick.
func (q *Queue) runReaperTick() {
	c := q.Redis.Pool.Get()
	defer c.Close()

	var (
		cursor   int = 0
		scanned  int
		requeued int
		failed   int
		recent   int
		dupes    int
		errs     int
	)

	for {
		if systemShutdown {
			break
		}

		reply, err := c.Do("SCAN", cursor, "MATCH", "queues:*:locked:*", "COUNT", 100)
		if err != nil {
			q.ErrorLog.Printf("reaper: scan error: %v", err)
			errs++
			break
		}
		parts, ok := reply.([]any)
		if !ok || len(parts) != 2 {
			q.ErrorLog.Printf("reaper: unexpected SCAN reply shape: %+v", reply)
			errs++
			break
		}

		cursorBytes, ok := parts[0].([]uint8)
		if !ok {
			q.ErrorLog.Printf("reaper: cursor cast failed: %+v", parts[0])
			errs++
			break
		}
		nextCursor, perr := strconv.Atoi(string(cursorBytes))
		if perr != nil {
			q.ErrorLog.Printf("reaper: cursor parse failed: %v", perr)
			errs++
			break
		}

		data, ok := parts[1].([]any)
		if !ok {
			q.ErrorLog.Printf("reaper: data cast failed: %+v", parts[1])
			errs++
			break
		}

		for _, k := range data {
			kb, _ := k.([]uint8)
			key := string(kb)
			keyParts := strings.Split(key, ":")
			if len(keyParts) < 4 {
				continue
			}
			jobID := keyParts[len(keyParts)-1]
			queue := keyParts[len(keyParts)-3]
			scanned++

			job, err := getRedisValue(&c, queue, jobID, formatLocked)
			if err != nil {
				if !strings.Contains(err.Error(), "no such key") {
					q.ErrorLog.Printf("reaper: HGETALL %s: %v", key, err)
					errs++
				}
				continue
			}

			lockedAt, parseErr := time.Parse(time.RFC3339, job.LockedAt)
			if parseErr != nil || job.LockedAt == "" {
				// Legacy fallback for locked keys written before LockedAt existed.
				reserved, rerr := time.Parse(time.RFC3339, job.ReservedAt)
				if rerr != nil {
					continue
				}
				lockedAt = reserved
			}

			timeout := q.LockTimeout
			if job.LockFor > 0 {
				timeout = job.LockFor
			}
			if time.Since(lockedAt) < time.Duration(timeout)*time.Second {
				recent++
				continue
			}

			// Concurrent re-entry guard: if a pending: copy of the same job
			// already exists, the handleRedisJob retry branch leaked the locked
			// key; the pending copy owns the retry state. Skip to avoid
			// double-retry and duplicate failed_jobs rows.
			pendingKey := fmt.Sprintf(formatPending, queue, jobID)
			exists, exErr := redis.Int(c.Do("EXISTS", pendingKey))
			if exErr != nil {
				q.ErrorLog.Printf("reaper: EXISTS %s: %v", pendingKey, exErr)
				errs++
				continue
			}
			if exists == 1 {
				dupes++
				// Clean up the leaked locked key so it doesn't keep tripping
				// the reaper on every tick. Swallow no-such-key race.
				if _, delErr := c.Do("DEL", fmt.Sprintf(formatLocked, queue, jobID)); delErr != nil {
					q.ErrorLog.Printf("reaper: DEL leaked locked %s: %v", jobID, delErr)
					errs++
				}
				continue
			}

			outcome, err := q.requeueExpiredLock(c, queue, jobID, job, timeout)
			if err != nil {
				errs++
				continue
			}
			switch outcome {
			case reapRequeued:
				requeued++
			case reapPermafailed:
				failed++
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	reaperTicks.Add(1)
	reaperScannedKeys.Add(uint64(scanned))
	reaperRequeued.Add(uint64(requeued))
	reaperPermafailed.Add(uint64(failed))
	reaperSkippedRecent.Add(uint64(recent))
	reaperSkippedDupePend.Add(uint64(dupes))
	reaperErrors.Add(uint64(errs))

	// Quiet when idle; informative when busy. Operators see activity without
	// log spam from empty scans.
	if scanned > 0 || errs > 0 {
		q.InfoLog.Printf("reaper: scanned=%d requeued=%d permafailed=%d recent=%d dupes=%d errs=%d",
			scanned, requeued, failed, recent, dupes, errs)
	}
}

type reapOutcome int

const (
	reapRequeued reapOutcome = iota
	reapPermafailed
)

// requeueExpiredLock routes an expired-lock job through the same retry /
// permafail state machine handleRedisJob uses. `timeout` is the effective
// timeout (per-job LockFor override or queue default) so the Exception
// message is accurate.
//
// Returns the outcome plus any error so the caller can update counters.
func (q *Queue) requeueExpiredLock(c redis.Conn, queue, jobID string, j *Job, timeout int) (reapOutcome, error) {
	j.RetryCounter = j.RetryCounter + 1
	j.Exception = fmt.Sprintf("lock expired after %ds", timeout)

	if j.Retry && j.RetryCounter < q.MaxAttempts {
		// IMPORTANT: clear RetryAfter so handleRedisJob's first-retry branch
		// (RetryAfter == "") computes fresh 5s backoff when the handler next
		// fails. If we populated RetryAfter here, handleRedisJob would see
		// the reaper's value and apply the "subsequent retry" multiplication
		// path, compounding delay inaccurately. The reaper's own
		// retry-gate below gives the job breathing room before re-dispatch.
		j.RetryAfter = time.Now().Add(time.Duration(j.RetryCounter*5) * time.Second).Format(time.RFC3339)
		j.Status = statusPending
		j.LockedAt = ""
		if _, err := c.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatLocked, queue, jobID)).AddFlat(j)...); err != nil {
			q.ErrorLog.Printf("reaper: HSET on locked before requeue %s: %v", jobID, err)
			return reapRequeued, err
		}
		if err := setRedisKey(&c, queue, jobID, formatLocked, formatPending); err != nil {
			if strings.Contains(err.Error(), "no such key") {
				return reapRequeued, nil // benign race with worker completing normally
			}
			q.ErrorLog.Printf("reaper: RENAME locked->pending %s: %v", jobID, err)
			return reapRequeued, err
		}
		if q.Debug {
			q.InfoLog.Printf("reaper: requeued %s (attempt %d, timeout %ds)", jobID, j.RetryCounter, timeout)
		}
		return reapRequeued, nil
	}

	// Permafail path
	now := time.Now().UTC()
	j.FailedAt = now.Format(time.RFC3339)
	j.CreatedAt = time.Now()
	j.UpdatedAt = time.Now()
	j.Status = statusFailed
	j.LockedAt = "" // clear so the failed: hash doesn't carry stale lock timestamp

	if _, err := c.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatLocked, queue, jobID)).AddFlat(j)...); err != nil {
		q.ErrorLog.Printf("reaper: HSET on locked before failing %s: %v", jobID, err)
		return reapPermafailed, err
	}
	if err := setRedisKey(&c, queue, jobID, formatLocked, formatFailed); err != nil {
		if strings.Contains(err.Error(), "no such key") {
			return reapPermafailed, nil
		}
		q.ErrorLog.Printf("reaper: RENAME locked->failed %s: %v", jobID, err)
		return reapPermafailed, err
	}
	if q.DB != nil {
		collection := q.DB.Collection("failed_jobs")
		if _, err := collection.Insert(*j); err != nil {
			q.ErrorLog.Printf("reaper: failed_jobs insert %s: %v", jobID, err)
			// Terminal state already set in redis; don't fail the outcome.
		}
	}
	if q.Debug {
		q.InfoLog.Printf("reaper: permafailed %s after %d attempts (lock expired after %ds)", jobID, j.RetryCounter, timeout)
	}
	return reapPermafailed, nil
}
