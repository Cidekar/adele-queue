//go:build queuebench

// Package main is the queuebench harness — a redis-backed, end-to-end
// exerciser for the adele-queue service. It bootstraps the adele-framework,
// loads the queue provider against a real redis instance, registers the
// benchjobs handlers, optionally dispatches a seed/stress/long-sleep batch,
// and then blocks forever so the worker pool and reaper keep running.
//
// Build and run via the Makefile (see `make bench:help`). The harness is
// gated behind the `queuebench` build tag so it never enters the default
// `go build ./...` graph consumers rely on.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cidekar/adele-queue/internal/bench/benchjobs"

	adele "github.com/cidekar/adele-framework"
	"github.com/cidekar/adele-framework/provider"
	"github.com/cidekar/adele-framework/rpcserver"
	queue "github.com/cidekar/adele-queue"

	"github.com/joho/godotenv"
)

func main() {
	seedJobs := flag.Bool("seed-jobs", false, "dispatch all test jobs and keep running")
	stressN := flag.Int("stress-jobs", 0, "dispatch N stress jobs concurrently then keep running")
	stressC := flag.Int("stress-concurrency", 16, "dispatcher goroutines for --stress-jobs")
	stressMode := flag.String("stress-mode", "hello", "stress mode: hello | mixed | fail")
	longSleep := flag.Int("long-sleep", 0, "dispatch one long-sleep job of N seconds then keep running (for reaper SIGKILL testing)")
	flag.Parse()

	// Load .env.queuebench before bootstrap so REDIS_* / DATABASE_* are in
	// the environment when adele-framework reads them. Missing file is not
	// fatal — operators may export env vars directly.
	if err := godotenv.Load(".env.queuebench"); err != nil {
		log.Printf("queuebench: warning: .env.queuebench not loaded: %v (continuing with existing environment)", err)
	}

	a := bootstrapApplication()

	// Graceful shutdown: SIGINT / SIGTERM -> Stop rpc -> close the queue
	// (its Close() does a bounded-wait so the reaper's next tick finishes
	// cleanly) -> exit.
	go listenForShutdown(a)

	if err := rpcserver.Start(a); err != nil {
		log.Fatalf("queuebench: failed to start rpc: %s", err)
	}

	q := lookupQueueService()
	if q == nil {
		log.Fatal("queuebench: queue service not available; is the adele-queue provider registered?")
	}

	// Register every benchjob handler. Errors are collected but non-fatal
	// so a single duplicate / bad handler does not prevent dispatch.
	for _, err := range benchjobs.RegisterAll(q) {
		a.Log.Errorf("queuebench: RegisterAll: %v", err)
	}

	if *longSleep > 0 {
		job, err := benchjobs.NewSleepJob(time.Duration(*longSleep) * time.Second)
		if err != nil {
			a.Log.Errorf("--long-sleep: build job: %v", err)
		} else if _, err := q.Dispatch(*job); err != nil {
			a.Log.Errorf("--long-sleep: dispatch: %v", err)
		} else {
			a.Log.Infof("--long-sleep: dispatched one %ds sleep job; SIGKILL me within that window to orphan it", *longSleep)
		}
	}

	if *seedJobs {
		if err := benchjobs.SeedAll(q); err != nil {
			a.Log.Errorf("--seed-jobs: SeedAll reported errors: %v", err)
		} else {
			a.Log.Info("--seed-jobs: all test jobs dispatched")
		}
	}

	if *stressN > 0 {
		cfg := benchjobs.StressConfig{
			Count:       *stressN,
			Concurrency: *stressC,
			Mode:        benchjobs.StressMode(*stressMode),
		}
		if _, err := benchjobs.Stress(q, cfg); err != nil {
			a.Log.Errorf("--stress-jobs: Stress reported error: %v", err)
		}
	}

	log.Println("queuebench: dispatch complete; workers + reaper running. Ctrl-C to exit.")
	select {}
}

// lookupQueueService walks the registered provider list, finds the queue
// provider, and returns the concrete *queue.Queue so callers can reach
// RegisterHandler / Dispatch / Close. The framework's Provider has no
// Get(name) accessor — hence the walk-and-assert pattern (kept verbatim
// from the kicktires reference implementation).
func lookupQueueService() *queue.Queue {
	for _, sp := range provider.GetRegisteredProviders() {
		if sp.Name() != "queue" {
			continue
		}
		if qp, ok := sp.(*queue.ServiceProvider); ok {
			svc := qp.Service()
			if svc == nil {
				return nil
			}
			return svc
		}
	}
	return nil
}

// bootstrapApplication builds the adele-framework application, wires the
// queue provider with harness-appropriate config (lock_timeout=10,
// reaper_interval=5 for fast feedback), and runs LoadProviders so the
// service is ready before we register handlers / dispatch.
func bootstrapApplication() *adele.Adele {
	path, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	a := &adele.Adele{}
	if err := a.New(path); err != nil {
		log.Fatal(err)
	}

	a.AppName = "queuebench"

	p := &provider.Provider{
		EnabledProviders: make(map[string]bool),
		ProviderConfigs:  make(map[string]map[string]interface{}),
	}
	a.Provider = p

	a.Provider.SetProviderConfig("queue", map[string]interface{}{
		"backend":               "redis",
		"worker_count":          4,
		"max_attempts":          5,
		"high_water_mark":       10000,
		"queue_channels":        []string{"job", "email"},
		"queue_channel_default": "job",
		"debug":                 false,
		"redis_prefix":          "queuebench",
		"redis_scan_interval":   1,
		"lock_timeout":          10,
		"reaper_interval":       5,
	})

	if err := a.Provider.LoadProviders(a); err != nil {
		a.Log.Error(err)
		os.Exit(1)
	}

	return a
}

// listenForShutdown stops the rpc server and closes the queue on SIGINT /
// SIGTERM. Close(nil) on the queue performs a bounded-wait so in-flight
// handlers (and the reaper tick) finish before exit.
func listenForShutdown(a *adele.Adele) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	s := <-quit

	a.Log.Info("queuebench: received signal ", s.String())

	if err := rpcserver.Stop(a); err != nil {
		a.Log.Errorf("queuebench: rpc stop: %v", err)
	}

	if q := lookupQueueService(); q != nil {
		q.Close(nil)
	}

	a.Log.Info("queuebench: goodbye")
	os.Exit(0)
}
