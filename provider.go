package queue

import (
	adele "github.com/cidekar/adele-framework"
	"github.com/cidekar/adele-framework/provider"
	"github.com/cidekar/adele-queue/api"
)

// ServiceProvider is the compiled Adele framework provider for the queue.
// It wires up the queue service and starts the worker pool during Boot.
//
// Example:
//
//	// Registered automatically via init().
//	// Construct a queue directly inside your application:
//	q := queue.New(app)
type ServiceProvider struct {
	service   *api.Queue
	config    api.Configuration
	hasConfig bool
}

// Name returns the unique identifier for this provider.
func (p *ServiceProvider) Name() string {
	return "queue"
}

// Priority returns 30, placing this provider in the core-services tier per
// Adele conventions.
func (p *ServiceProvider) Priority() int {
	return 30
}

// Service returns the underlying *api.Queue after Register has been called.
// May be nil if the provider has not yet been registered.
func (p *ServiceProvider) Service() *api.Queue {
	return p.service
}

// Configure maps a config map to the Configuration struct fields and stores
// it for use during Register.
func (p *ServiceProvider) Configure(config map[string]interface{}) error {
	if backend, ok := config["backend"].(string); ok {
		p.config.Backend = backend
	}
	if workerCount, ok := config["worker_count"].(int); ok {
		p.config.WorkerCount = workerCount
	}
	if maxAttempts, ok := config["max_attempts"].(int); ok {
		p.config.MaxAttempts = maxAttempts
	}
	if highWaterMark, ok := config["high_water_mark"].(int); ok {
		p.config.HighWaterMark = highWaterMark
	}
	if channels, ok := config["queue_channels"].([]string); ok {
		p.config.QueueChannels = channels
	}
	if defaultChannel, ok := config["queue_channel_default"].(string); ok {
		p.config.QueueChannelDefault = defaultChannel
	}
	if debug, ok := config["debug"].(bool); ok {
		p.config.Debug = debug
	}
	if redisPrefix, ok := config["redis_prefix"].(string); ok {
		p.config.RedisPrefix = redisPrefix
	}
	if redisScanInterval, ok := config["redis_scan_interval"].(int); ok {
		p.config.RedisScanInterval = redisScanInterval
	}
	if v, ok := config["lock_timeout"].(int); ok {
		p.config.LockTimeout = v
	}
	if v, ok := config["reaper_interval"].(int); ok {
		p.config.ReaperInterval = v
	}
	p.hasConfig = true
	return nil
}

// Register initializes the queue service. The queue registers no routes; it
// runs entirely through the worker pool started in Boot.
func (p *ServiceProvider) Register(app interface{}) error {
	a := app.(*adele.Adele)

	if p.hasConfig {
		p.service = api.NewWithConfig(a, p.config)
	} else {
		p.service = api.New(a)
	}

	return nil
}

// Boot starts the queue worker pool. Called after every provider has been
// registered so the queue can safely rely on any earlier-booted dependencies.
func (p *ServiceProvider) Boot(app interface{}) error {
	if p.service == nil {
		return nil
	}
	p.service.Listen()
	return nil
}

func init() {
	provider.RegisterGlobalProvider(&ServiceProvider{})
}
