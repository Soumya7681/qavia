package runtrigger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// ScheduleID is the built-in scheduler's ID.
const ScheduleID = "schedule"

// Entry is one recurring job.
type Entry struct {
	// Cron is standard five-field cron, or one of the @every forms asynq accepts.
	Cron string

	// Type is the job type to enqueue, and Payload is what it receives. A
	// scheduled job is an ordinary job: same handler, same retries, same row.
	Type    string
	Payload any

	Queue string
}

// Scheduler runs recurring work.
//
// It is asynq's periodic scheduler rather than a goroutine with a ticker, and the
// difference matters on more than one process: a ticker in each API replica fires
// the same job N times, while the scheduler elects one owner and enqueues once.
//
// Recurring drift checks (BE-11.7) and retention (BE-0.18) are its first users.
type Scheduler struct {
	scheduler *asynq.Scheduler
	entries   []Entry
}

func NewScheduler(redis asynq.RedisConnOpt) *Scheduler {
	return &Scheduler{
		scheduler: asynq.NewScheduler(redis, &asynq.SchedulerOpts{
			// The platform stores UTC everywhere and displays in the user's zone
			// (NFR-7). A scheduler on local time would make "runs at 02:00" mean
			// something different on a server that moved.
			Location: time.UTC,
			Logger:   schedulerLogger{},
		}),
	}
}

func (s *Scheduler) ID() string { return ScheduleID }

func (s *Scheduler) Kind() Kind { return KindSchedule }

// Available is true once the scheduler has entries to run.
func (s *Scheduler) Available(_ context.Context) bool { return len(s.entries) > 0 }

// Register adds a recurring entry. Called during wiring, before Run.
func (s *Scheduler) Register(entry Entry) error {
	payload, err := json.Marshal(entry.Payload)
	if err != nil {
		return fmt.Errorf("runtrigger: encode scheduled payload for %q: %w", entry.Type, err)
	}

	queue := entry.Queue
	if queue == "" {
		queue = "low"
	}

	if _, err := s.scheduler.Register(entry.Cron,
		asynq.NewTask(entry.Type, payload),
		asynq.Queue(queue),
	); err != nil {
		return fmt.Errorf("runtrigger: schedule %q at %q: %w", entry.Type, entry.Cron, err)
	}

	s.entries = append(s.entries, entry)
	return nil
}

// Run drives the scheduler until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	if len(s.entries) == 0 {
		<-ctx.Done()
		return nil
	}

	if err := s.scheduler.Start(); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	<-ctx.Done()
	s.scheduler.Shutdown()
	return nil
}

// RedisReplayGuard refuses a webhook signature that has already been accepted.
//
// SET NX with an expiry is the whole mechanism: the first caller wins, and the key
// disappears once the delivery is too old to be accepted anyway, so nothing grows
// without bound. Redis is already required for the queue, so this adds no new
// dependency (tech-stack.md 6).
type RedisReplayGuard struct {
	client redis.UniversalClient
}

func NewRedisReplayGuard(client redis.UniversalClient) *RedisReplayGuard {
	return &RedisReplayGuard{client: client}
}

func (g *RedisReplayGuard) FirstUse(ctx context.Context, signature string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	stored, err := g.client.SetNX(ctx, "qavia:webhook:"+signature, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("record webhook signature: %w", err)
	}
	return stored, nil
}

// schedulerLogger routes asynq's scheduler logging into slog.
type schedulerLogger struct{}

func (schedulerLogger) Debug(args ...any) { slog.Debug(fmt.Sprint(args...), "source", "scheduler") }
func (schedulerLogger) Info(args ...any)  { slog.Info(fmt.Sprint(args...), "source", "scheduler") }
func (schedulerLogger) Warn(args ...any)  { slog.Warn(fmt.Sprint(args...), "source", "scheduler") }
func (schedulerLogger) Error(args ...any) { slog.Error(fmt.Sprint(args...), "source", "scheduler") }
func (schedulerLogger) Fatal(args ...any) { slog.Error(fmt.Sprint(args...), "source", "scheduler") }
