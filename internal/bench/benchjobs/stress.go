package benchjobs

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cidekar/adele-queue/api"
)

// StressMode controls which job mix the harness dispatches.
type StressMode string

const (
	StressModeHello StressMode = "hello" // pure cheap work, measures dispatch throughput
	StressModeMixed StressMode = "mixed" // cheap + slow + fail + panic, measures backpressure
	StressModeFail  StressMode = "fail"  // retrying errors only, stresses re-enqueue path
)

// StressConfig parameterises a stress run.
type StressConfig struct {
	Count       int        // total jobs to dispatch
	Concurrency int        // dispatcher goroutines (upper bound on in-flight Dispatch calls)
	Mode        StressMode // job mix
}

// StressResult summarises a stress run.
type StressResult struct {
	Dispatched      int64
	DispatchErrors  int64
	ConstructErrors int64
	Elapsed         time.Duration
	StartGoroutines int
	PeakGoroutines  int
}

// Stress dispatches Count jobs through Concurrency goroutines, waits for all
// dispatches to return (not for the workers to finish consuming), and returns
// timing + goroutine-pressure stats.
//
// Note: on the memory backend, q.Jobs is unbuffered. Dispatcher goroutines
// above WorkerCount will block until a worker consumes. This is by design —
// dispatch throughput is gated by worker throughput.
func Stress(q QueueDispatcher, cfg StressConfig) (*StressResult, error) {
	if q == nil {
		return nil, errors.New("stress: nil queue dispatcher")
	}
	if cfg.Count <= 0 {
		return nil, errors.New("stress: count must be > 0")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 16
	}
	if cfg.Mode == "" {
		cfg.Mode = StressModeHello
	}

	result := &StressResult{StartGoroutines: runtime.NumGoroutine()}
	var peak int64
	atomic.StoreInt64(&peak, int64(result.StartGoroutines))

	fmt.Printf("stress: mode=%s count=%d concurrency=%d startGoroutines=%d\n",
		cfg.Mode, cfg.Count, cfg.Concurrency, result.StartGoroutines)

	// Goroutine-count sampler runs every 100ms; cheap and bounded by the run.
	stopSampler := make(chan struct{})
	var samplerWG sync.WaitGroup
	samplerWG.Add(1)
	go func() {
		defer samplerWG.Done()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopSampler:
				return
			case <-t.C:
				n := int64(runtime.NumGoroutine())
				for {
					cur := atomic.LoadInt64(&peak)
					if n <= cur || atomic.CompareAndSwapInt64(&peak, cur, n) {
						break
					}
				}
			}
		}
	}()

	start := time.Now()
	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

	for i := 0; i < cfg.Count; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()

			job, err := stressJobFor(cfg.Mode, i)
			if err != nil {
				atomic.AddInt64(&result.ConstructErrors, 1)
				return
			}
			if _, err := q.Dispatch(*job); err != nil {
				atomic.AddInt64(&result.DispatchErrors, 1)
				return
			}
			atomic.AddInt64(&result.Dispatched, 1)
		}(i)
	}

	wg.Wait()
	result.Elapsed = time.Since(start)
	// Keep the sampler running for a drain window so retries register.
	// For fail mode the worst case is MaxAttempts=3 with 1s * retryCounter
	// backoff per retry — 6s is enough for the last retry timers to fire.
	drainWindow := 100 * time.Millisecond
	if cfg.Mode == StressModeFail || cfg.Mode == StressModeMixed {
		drainWindow = 7 * time.Second
	}
	fmt.Printf("stress: waiting %s for workers to drain / retries to fire\n", drainWindow)
	time.Sleep(drainWindow)

	close(stopSampler)
	samplerWG.Wait()
	result.PeakGoroutines = int(atomic.LoadInt64(&peak))

	rate := float64(result.Dispatched) / result.Elapsed.Seconds()
	fmt.Printf("stress: dispatched=%d construct_err=%d dispatch_err=%d elapsed=%s rate=%.0f dispatch/s peakGoroutines=%d\n",
		result.Dispatched,
		result.ConstructErrors,
		result.DispatchErrors,
		result.Elapsed,
		rate,
		result.PeakGoroutines,
	)
	if cfg.Mode == StressModeFail || cfg.Mode == StressModeMixed {
		fmt.Printf("stress: alwaysError total handler invocations: %d (expect ~4x dispatched for fail mode: 1 initial + 3 retries)\n",
			AlwaysErrorAttempts.Load())
	}
	fmt.Printf("stress: final goroutine count: %d\n", runtime.NumGoroutine())

	return result, nil
}

// stressJobFor builds one job of the type indicated by the mode. Index i is
// used to make payloads unique so retry-counter state (in flakyAttempts)
// doesn't bleed between dispatches.
func stressJobFor(mode StressMode, i int) (*api.Job, error) {
	switch mode {
	case StressModeHello:
		return NewHelloJob(fmt.Sprintf("stress-%d", i))

	case StressModeFail:
		return NewAlwaysErrorJob(fmt.Sprintf("stress-fail-%d", i))

	case StressModeMixed:
		// 60% hello, 20% sleep, 10% fail-with-retry, 5% panic, 5% busy-loop.
		roll := rand.IntN(100)
		switch {
		case roll < 60:
			return NewHelloJob(fmt.Sprintf("stress-hello-%d", i))
		case roll < 80:
			return NewSleepJob(time.Duration(10+rand.IntN(40)) * time.Millisecond)
		case roll < 90:
			return NewAlwaysErrorJob(fmt.Sprintf("stress-err-%d", i))
		case roll < 95:
			return NewPanicJob(fmt.Sprintf("stress-panic-%d", i))
		default:
			return NewBusyLoopJob(50_000)
		}
	}
	return nil, fmt.Errorf("stress: unknown mode %q", mode)
}
