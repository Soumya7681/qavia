package defects

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hyscaler/qavia/api/internal/capability"
	"github.com/hyscaler/qavia/api/internal/capability/defecttracker"
)

// The external defect mirror (BE-10.2, BE-10.6).
//
// When a defect is filed, it is written to the internal tracker first and always. The
// mirror then pushes it to whatever external tracker is configured — Jira today — and
// stores where it landed. The order is the guarantee: the internal defect exists before
// the mirror runs, so a Jira outage degrades to an admin alert and a defect with no
// external ref, never a lost bug (BE-10.2.4, BE-10.6.2).
//
// It fans out the way the notification service does, and for the same reason: the
// built-in is not "attempted then fallen back to" here, because the built-in write
// already happened. The mirror pushes to the active *external* trackers and treats each
// failure as a degradation to report, not an error to raise.

// Trackers is the slice of the tracker registry this mirror uses.
type Trackers interface {
	Active(ctx context.Context) []defecttracker.Tracker
	Builtin() (defecttracker.Tracker, bool)
	Kind() string
}

// Alerter raises an admin alert when an external tracker fails, so a broken Jira is
// visible rather than silently stopping the mirror (BE-10.6.1).
type Alerter interface {
	Degraded(ctx context.Context, event capability.Degradation)
}

// Mirror pushes filed defects to external trackers.
type Mirror struct {
	trackers Trackers
	service  *Service
	alerter  Alerter

	// baseURL builds the absolute links a ticket carries back to this platform. A
	// stored absolute URL goes stale when a deployment changes hostname, so it is built
	// from APP_URL at push time.
	baseURL string
}

func NewMirror(trackers Trackers, service *Service, alerter Alerter, baseURL string) *Mirror {
	return &Mirror{
		trackers: trackers,
		service:  service,
		alerter:  alerter,
		baseURL:  strings.TrimRight(baseURL, "/"),
	}
}

// Push mirrors one defect to every active external tracker.
//
// Best effort by design: a failure to mirror is a degradation, not a failure of filing.
// The internal defect already exists, so the caller's operation succeeded whatever
// happens here.
func (m *Mirror) Push(ctx context.Context, defect Defect) {
	if m == nil || m.trackers == nil {
		return
	}

	builtinID := ""
	if builtin, ok := m.trackers.Builtin(); ok {
		builtinID = builtin.ID()
	}

	ticket := defecttracker.Ticket{
		ID:          defect.ID,
		ProjectID:   defect.ProjectID,
		Title:       defect.Title,
		Description: defect.Description,
		Severity:    defecttracker.Severity(defect.Severity),
		Status:      defecttracker.Status(defect.Status),
		Links:       m.links(defect),
	}

	if err := defecttracker.Validate(ticket); err != nil {
		slog.WarnContext(ctx, "defect not mirrorable", "defect_id", defect.ID, "error", err)
		return
	}

	for _, tracker := range m.trackers.Active(ctx) {
		if tracker.ID() == builtinID {
			// The internal write already happened; the built-in is not a mirror target.
			continue
		}

		reference, err := tracker.Create(ctx, ticket)
		if err != nil {
			slog.WarnContext(ctx, "mirror defect to external tracker",
				"defect_id", defect.ID, "tracker", tracker.ID(), "error", err)

			if m.alerter != nil {
				m.alerter.Degraded(ctx, capability.Degradation{
					Kind:       m.trackers.Kind(),
					FailedID:   tracker.ID(),
					FallbackID: builtinID,
					Operation:  "create",
					Err:        err,
				})
			}
			continue
		}

		if err := m.service.SetExternalRef(ctx, defect.ID, map[string]any{
			"provider": reference.Provider,
			"key":      reference.Key,
			"url":      reference.URL,
		}); err != nil {
			slog.WarnContext(ctx, "store external ref", "defect_id", defect.ID, "error", err)
		}
	}
}

// links are the absolute URLs a ticket points back to.
func (m *Mirror) links(defect Defect) map[string]string {
	links := map[string]string{
		"Defect": fmt.Sprintf("%s/projects/%s/defects/%s", m.baseURL, defect.ProjectID, defect.ID),
	}
	if defect.RunResultID != nil {
		links["Failure"] = fmt.Sprintf("%s/run-results/%s", m.baseURL, *defect.RunResultID)
	}
	return links
}
