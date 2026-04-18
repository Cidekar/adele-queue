package api

import (
	"encoding/json"
	"time"
)

// GetFailedJobsFromMemory returns the ids of failed jobs currently held in
// the in-memory failure cache.
func (q *Queue) GetFailedJobsFromMemory() []string {
	q.FailedJobs.mu.RLock()
	defer q.FailedJobs.mu.RUnlock()
	return q.FailedJobs.data
}

// GetCompletedJobs returns every completed job persisted to storage, ordered
// by ascending id.
func (q *Queue) GetCompletedJobs() (*[]Job, error) {
	var completedJobs []Job
	if q.DB == nil {
		return &completedJobs, nil
	}
	collection := q.DB.Collection("jobs")
	res := collection.Find().OrderBy("id")
	if err := res.All(&completedJobs); err != nil {
		return nil, err
	}
	return &completedJobs, nil
}

// addFailedJob records a failed job: it is either retried (with backoff) or
// appended to the in-memory failure cache and persisted to the failed_jobs
// table.
func (q *Queue) addFailedJob(job Job, workerID int) {
	q.FailedJobs.mu.Lock()
	defer q.FailedJobs.mu.Unlock()

	// Reset the slice once it reaches the high water mark to prevent an
	// unbounded memory leak.
	if len(q.FailedJobs.data) >= q.HighWaterMark {
		q.FailedJobs.data = nil
	}

	q.FailedJobs.data = append(q.FailedJobs.data, job.ID)

	if job.Retry && job.RetryCounter < q.MaxAttempts {
		// Calculate backoff with a minimum or simply wait a defined number
		// of seconds before retrying the job.
		var delay time.Duration
		if job.RetryInSeconds != 0 {
			backoff := job.RetryInSeconds * job.RetryCounter
			if backoff == 0 {
				backoff = job.RetryInSeconds
			}
			delay = time.Duration(backoff) * time.Second
		} else {
			delay = time.Duration(job.RetryCounter) * time.Second
		}

		time.Sleep(delay)

		job.RetryCounter++
		q.Jobs <- job
		return
	}

	// Persist the failed job record.
	failedJob := Job{
		Name: job.Name,
		ID:   job.ID,
	}

	if job.Payload != nil {
		bytes, _ := json.Marshal(job.Payload)
		failedJob.Payload = bytes
	}

	if q.DB != nil {
		collection := q.DB.Collection("failed_jobs")
		if _, err := collection.Insert(failedJob); err != nil {
			q.ErrorLog.Printf("error when inserting failed job %s with the following message: %s", job.ID, err)
		}
	}

	if q.Debug {
		q.InfoLog.Printf("worker %d: failed job ID: %s\n", workerID, job.ID)
	}
}

// addCompletedJob persists a successfully processed job to the jobs table.
func (q *Queue) addCompletedJob(job Job, workerID int) {
	if job.Payload != nil {
		bytes, _ := json.Marshal(job.Payload)
		job.Payload = bytes
	}

	if q.DB != nil {
		collection := q.DB.Collection("jobs")
		if _, err := collection.Insert(job); err != nil {
			q.ErrorLog.Printf("error when inserting completed job %s with the following message: %s", job.ID, err)
		}
	}

	if q.Debug {
		q.InfoLog.Printf("worker %d: completed job ID: %s\n", workerID, job.ID)
	}
}

// GetFailedJobs returns every failed job persisted to storage, ordered by
// ascending id.
func (q *Queue) GetFailedJobs() (*[]Job, error) {
	var failedJobs []Job
	if q.DB == nil {
		return &failedJobs, nil
	}
	collection := q.DB.Collection("failed_jobs")
	res := collection.Find().OrderBy("id")
	if err := res.All(&failedJobs); err != nil {
		return nil, err
	}
	return &failedJobs, nil
}
