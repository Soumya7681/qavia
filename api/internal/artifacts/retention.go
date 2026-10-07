package artifacts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// RetentionType is the job type the scheduler enqueues.
const RetentionType = "artifacts.retention"

// retentionBatch bounds one sweep. A retention run on a platform that has never
// had one must not hold a connection for an hour or delete a hundred thousand
// objects in a single transaction; it takes a batch and the schedule brings it
// back tomorrow.
const retentionBatch = 500

// RetentionPayload is what the scheduled task carries.
//
// Empty by design: the cutoff comes from settings when the job runs, so changing
// the retention period takes effect on the next sweep rather than on the next
// deploy of whatever enqueued it.
type RetentionPayload struct{}

// Sweeper is another owner of stored objects, asked to expire its own past the same
// cutoff (BE-7.5.3).
//
// Declared here as the one method this handler needs rather than by importing the
// packages that own runs or discoveries: retention is one schedule and one setting, and
// a second sweep job per kind of file would be a second thing to configure and forget.
type Sweeper interface {
	SweepArtifacts(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

// RetentionHandler deletes artifacts past the retention period (F-17.5).
//
// Both halves are deleted, the stored object and the row, and in that order: a
// stored file with no row is invisible and only wastes space, while a row with no
// file is a download that fails.
type RetentionHandler struct {
	service *Service

	// sweepers are the other owners of stored objects: a run's logs, videos, and
	// traces, and a discovery's recordings. Empty on a process that does not wire any,
	// and the uploaded-artifact sweep still runs.
	sweepers []Sweeper
}

func NewRetentionHandler(service *Service, sweepers ...Sweeper) *RetentionHandler {
	return &RetentionHandler{service: service, sweepers: sweepers}
}

func (h *RetentionHandler) Type() string { return RetentionType }

// IdempotencyKey is the day the sweep runs.
//
// A retry on the same day finds the existing row and does not enqueue a second
// sweep. A sweep is idempotent anyway, because deleting an object that is already
// gone is a success, but the key keeps the jobs table honest about how many sweeps
// actually happened.
func (h *RetentionHandler) IdempotencyKey(_ RetentionPayload) string {
	return time.Now().UTC().Format("2006-01-02")
}

func (h *RetentionHandler) Handle(ctx context.Context, _ RetentionPayload, jc jobs.JobContext) error {
	days, err := h.service.settings.Int(ctx, "storage.retention_days", settings.Target{})
	if err != nil {
		return err
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	jc.Event("Deleting artifacts created before %s", cutoff.Format(time.RFC3339))

	// The other owners first, because they hold the large files: a run's video and
	// trace are what actually fill a disk, and a sweep that stopped early after the
	// uploads would leave them (BE-7.5.3).
	for _, sweeper := range h.sweepers {
		removed, err := sweeper.SweepArtifacts(ctx, cutoff, retentionBatch)
		if err != nil {
			// Reported and carried on: one owner failing must not cost the others their
			// sweep, and the schedule brings this back tomorrow.
			slog.WarnContext(ctx, "a retention sweep failed", "error", err)
			continue
		}
		if removed > 0 {
			jc.Event("Deleted %d stored file(s) belonging to finished runs", removed)
		}
	}

	rows, err := h.service.db.Queries().ListArtifactsOlderThan(ctx,
		dbgen.ListArtifactsOlderThanParams{CreatedAt: cutoff, PageSize: retentionBatch})
	if err != nil {
		return fmt.Errorf("list artifacts for retention: %w", err)
	}
	if len(rows) == 0 {
		jc.Event("Nothing uploaded is past retention")
		jc.Progress(100)
		return nil
	}

	var deleted int
	for i, row := range rows {
		// Checked between units of work rather than only at the start, so a
		// cancelled or shutting-down sweep stops promptly and the next one picks up
		// where this left off.
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := h.service.store.Delete(ctx, row.StorageKey); err != nil {
			// One unreachable object must not stop the sweep: the rest of the batch
			// is still worth reclaiming, and this row is retried tomorrow.
			slog.WarnContext(ctx, "retention could not delete object",
				"artifact_id", row.ID, "error", err)
			continue
		}
		if _, err := h.service.db.Queries().DeleteArtifact(ctx, row.ID); err != nil {
			return fmt.Errorf("delete artifact %s: %w", row.ID, err)
		}

		deleted++
		jc.Progress((i + 1) * 100 / len(rows))
	}

	jc.Event("Deleted %d of %d artifacts past retention", deleted, len(rows))
	return nil
}
