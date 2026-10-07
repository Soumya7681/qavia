package runs

import (
	"context"
	"log/slog"
	"time"

	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// The second half of reaping (BE-4.2.4).
//
// A defer cannot run in a process that was killed, so every cleanup this platform
// does on the happy path is done again here, on a timer, by whichever worker is
// alive:
//
//   - Containers carrying this platform's label and older than the sweep age are
//     removed, along with their networks and firewall chains.
//   - Runs left `running` with no worker behind them are marked errored with a
//     stated reason, rather than sitting in a spinner forever.
//
// It is safe to run in several workers at once: container removal is idempotent, and
// the run update only touches rows still in flight.

// Sweeper cleans up after crashed workers.
type Sweeper struct {
	runs     *Service
	driver   runner.Driver
	settings Settings
}

func NewSweeper(service *Service, driver runner.Driver, config Settings) *Sweeper {
	return &Sweeper{runs: service, driver: driver, settings: config}
}

// Run sweeps on a ticker until the context ends. Called as a goroutine from the
// worker's start-up, next to the queue.
func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Once(ctx)
		}
	}
}

// Once is one sweep. Exported so a readiness check or an operator command can force
// one without waiting for the ticker.
func (s *Sweeper) Once(ctx context.Context) {
	age, err := s.settings.Duration(ctx, "runner.stale_after", settings.Target{})
	if err != nil {
		slog.WarnContext(ctx, "read the sweep age", "error", err)
		return
	}

	if s.driver != nil {
		removed, err := s.driver.Sweep(ctx, age)
		switch {
		case err != nil:
			slog.WarnContext(ctx, "sweep runner containers", "error", err)
		case removed > 0:
			// Worth a line at info: containers surviving a run means a worker died
			// mid-run, which is not routine.
			slog.InfoContext(ctx, "reaped orphaned runner containers", "count", removed)
		}
	}

	stale, err := s.runs.Stale(ctx, age, 100)
	if err != nil {
		slog.WarnContext(ctx, "list stale runs", "error", err)
		return
	}

	for _, id := range stale {
		if err := s.runs.MarkFailed(ctx, id,
			"The worker executing this run stopped without finishing it. Trigger the run again."); err != nil {
			slog.WarnContext(ctx, "mark a stale run failed", "run_id", id, "error", err)
			continue
		}
		slog.InfoContext(ctx, "marked a stranded run as errored", "run_id", id)
	}
}
