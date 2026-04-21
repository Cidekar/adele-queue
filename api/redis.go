package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
)

// Hash key formats and status constants used by the redis backend.
const (
	formatComplete = "queues:%s:completed:%s"
	formatFailed   = "queues:%s:failed:%s"
	formatLocked   = "queues:%s:locked:%s"
	formatPending  = "queues:%s:pending:%s"
	statusComplete = "completed"
	statusFailed   = "failed"
	statusActive   = "processing"
	statusPending  = "pending"
)

// cursorScanRedisDb performs a recursive cursor-based iteration over redis,
// picking up pending jobs across all queues. The scan restarts at cursor 0
// when the keyspace has been fully traversed.
func (q *Queue) cursorScanRedisDb(c redis.Conn, workerID int) {
	var cursor int = 0

	for {
		// Break if system is shutting down
		if systemShutdown {
			break
		}

		time.Sleep(time.Second * time.Duration(q.Redis.ScanInterval))

		// Scan the redis database for pending jobs across all queues
		reply, err := c.Do("SCAN", cursor, "MATCH", "queues:*:pending*", "COUNT", 10)
		if err != nil {
			q.ErrorLog.Println("error: unexpected err from scan:", err)
		}

		// Extract cursor and data from the redis multi-bulk response
		responseParts, ok := reply.([]any)
		if !ok || len(responseParts) != 2 {
			q.ErrorLog.Printf("error: unexpected response format from redis server. Received: %+v\n", reply)
			return
		}

		data, ok := responseParts[1].([]any)
		if !ok {
			q.ErrorLog.Printf("error: unexpected data format in redis response. Data part: %+v\n", responseParts[1])
			return
		}

		cursorBit, ok := responseParts[0].([]uint8)
		if !ok {
			q.ErrorLog.Println("error: unexpected keys format:", responseParts[0])
		}

		nextCursor, err := strconv.Atoi(string(cursorBit))
		if err != nil {
			q.ErrorLog.Println("error: unable to convert redis cursor to int for next scan iteration.")
		}

		cursor = nextCursor

		for _, keys := range data {
			key, ok := keys.([]uint8)
			if !ok {
				q.ErrorLog.Println("error: unexpected key format:", string(key))
			}

			keyParts := strings.Split(string(key), ":")

			jobID := keyParts[len(keyParts)-1]
			queue := keyParts[len(keyParts)-3]

			if q.Debug {
				q.InfoLog.Println("queue: ", queue)
				q.InfoLog.Println("jobID: ", jobID)
			}

			err = setRedisKey(&c, queue, jobID, formatPending, formatLocked)
			if err != nil {
				if strings.Contains(err.Error(), "no such key") {
					continue
				}
				q.ErrorLog.Printf("error: unexpected error renaming job %s in redis from pending to locked. Redis server error: %+v\n", jobID, err)
				continue
			} else {
				cachedJob, err := getRedisValue(&c, queue, jobID, formatLocked)
				if err != nil {
					q.ErrorLog.Printf("error: unexpected response from redis getting value for %s. Response error: %+v\n", jobID, err)
					return
				}

				// Ensure retry is expired
				retryAfterTime, err := time.Parse(time.RFC3339, cachedJob.RetryAfter)
				if err != nil {
					q.ErrorLog.Printf("error parsing retry after for job %s: %v", jobID, err)
					return
				}

				currentTime := time.Now()
				if !currentTime.After(retryAfterTime) {
					err := setRedisKey(&c, queue, jobID, formatLocked, formatPending)
					if err != nil {
						q.ErrorLog.Printf("error setting redis key for job %s: %v", jobID, err)
						return
					}
				} else {
					q.handleRedisJob(queue, *cachedJob, jobID, c)
				}
			}
		}

		// Restart the cursor scan when the end of the redis keyspace is reached
		if cursor == 0 {
			q.cursorScanRedisDb(c, workerID)
		}
	}
}

// getRedisValue retrieves a job from redis using the provided connection, id
// and key format.
func getRedisValue(c *redis.Conn, queue, id, formattedKey string) (*Job, error) {
	if c == nil {
		return nil, fmt.Errorf("nil redis connection provided")
	}

	conn := *c
	key := fmt.Sprintf(formattedKey, queue, id)
	value, err := redis.Values(conn.Do("HGETALL", key))
	if err != nil {
		return nil, fmt.Errorf("failed to get value from redis for key %s: %w", key, err)
	}

	var job Job
	if err := redis.ScanStruct(value, &job); err != nil {
		return nil, fmt.Errorf("failed to scan value to struct %s: %w", key, err)
	}
	return &job, nil
}

// setRedisKey renames an existing key in redis to a new key name.
func setRedisKey(c *redis.Conn, queue, id, oldKey, newKey string) error {
	if len(oldKey) == 0 {
		return fmt.Errorf("invalid key provided")
	}
	if len(newKey) == 0 {
		return fmt.Errorf("invalid key provided")
	}

	keyOld := fmt.Sprintf(oldKey, queue, id)
	keyNew := fmt.Sprintf(newKey, queue, id)

	if c == nil {
		return fmt.Errorf("nil redis connection provided")
	}
	conn := *c
	_, err := conn.Do("RENAME", keyOld, keyNew)
	if err != nil {
		return fmt.Errorf("failed to rename key in redis from %s to %s: %w", oldKey, newKey, err)
	}

	return nil
}

// handleRedisJob invokes the RPC server with the locked job payload and
// transitions the redis key based on the result: success moves the job to
// completed; failure either re-queues with backoff or marks it failed.
func (q *Queue) handleRedisJob(queue string, cachedJob Job, jobID string, c redis.Conn) {
	_, err := c.Do("HSET", fmt.Sprintf(formatLocked, queue, cachedJob.ID), "Status", statusActive)
	if err != nil {
		return
	}

	rawValueFromRedis, err := json.Marshal(cachedJob)
	if err != nil {
		q.ErrorLog.Println("error: unexpected error marshaling payload:", err)
		return
	}

	res, rpcError := q.CallRPCServer(rawValueFromRedis)
	if res == 0 {
		now := time.Now().UTC()
		cachedJob.CompletedAt = now.Format(time.RFC3339)
		cachedJob.Status = statusComplete
		_, err = c.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatLocked, queue, jobID)).AddFlat(&cachedJob)...)
		if err != nil {
			q.ErrorLog.Println("error: unexpected error adding job to queue:", err)
			return
		}

		if err := setRedisKey(&c, queue, jobID, formatLocked, formatComplete); err != nil {
			q.ErrorLog.Printf("error: unexpected error renaming job %s with lock", jobID)
			return
		}
		if q.Debug {
			q.InfoLog.Printf("job %s completed successfully", jobID)
		}
		return
	}

	if res == 1 {
		if cachedJob.Retry && cachedJob.RetryCounter < q.MaxAttempts {
			// Setup retry-after gate — either initial 5-second backoff or
			// a growing backoff proportional to the retry counter.
			if cachedJob.RetryAfter == "" {
				delay := time.Now().Add(5 * time.Second).UTC()
				cachedJob.RetryAfter = delay.Format(time.RFC3339)
			} else {
				delay := time.Duration(cachedJob.RetryCounter*5) * time.Second
				cachedJob.RetryAfter = time.Now().Add(delay).Format(time.RFC3339)
			}

			cachedJob.RetryCounter = cachedJob.RetryCounter + 1

			now := time.Now().UTC()
			cachedJob.CompletedAt = now.Format(time.RFC3339)
			cachedJob.Status = statusPending
			_, err = c.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatPending, queue, jobID)).AddFlat(&cachedJob)...)
			if err != nil {
				q.ErrorLog.Println("error: unexpected error adding job to queue:", err)
				return
			}

			if q.Debug {
				q.InfoLog.Printf("job %s failed and added back to queue\n", cachedJob.ID)
			}
			return
		}

		// Permanent failure: update metadata and move to the failed keyspace.
		now := time.Now().UTC()
		cachedJob.FailedAt = now.Format(time.RFC3339)
		if rpcError != nil {
			cachedJob.Exception = rpcError.Error()
		}
		cachedJob.CreatedAt = time.Now()
		cachedJob.UpdatedAt = time.Now()
		cachedJob.Status = statusFailed

		_, err = c.Do("HSET", redis.Args{}.Add(fmt.Sprintf(formatLocked, queue, jobID)).AddFlat(&cachedJob)...)
		if err != nil {
			q.ErrorLog.Println("error: unexpected error adding job to queue:", err)
			return
		}

		if err := setRedisKey(&c, queue, jobID, formatLocked, formatFailed); err != nil {
			q.ErrorLog.Printf("error: unable to rename job id %s to a failed state: %s\n", jobID, err)
			return
		}

		if q.Debug {
			q.InfoLog.Printf("job %s has permanently failed after %d retries\n", jobID, cachedJob.RetryCounter)
		}

		// Store the failed job in the database
		if q.DB != nil {
			collection := q.DB.Collection("failed_jobs")
			if _, err := collection.Insert(cachedJob); err != nil {
				q.ErrorLog.Printf("error: unable to save failed job %s in db: %s", cachedJob.ID, err)
			}
			if q.Debug {
				q.InfoLog.Printf("job %s added to the failed_jobs table in db\n", jobID)
			}
		}
	}
}

// RedisKeyspaceFormater normalizes a channel name for use in a redis keyspace.
func (q *Queue) RedisKeyspaceFormater(keyspace string) string {
	replacer := strings.NewReplacer(":", "", " ", "-", ",", "")
	return strings.ToLower(replacer.Replace(keyspace))
}
