// Package audit records privileged actions (F-17.1, requirements.md 8.4).
//
// One entry point, so the set of audited actions is a list in this file rather
// than a habit spread across services. Detail is a typed value and passes through
// the same redaction as the logger, so no row here can hold a secret.
//
// This ships alongside auth rather than in BE-0.27 as planned, because login and
// lockout are audited actions and an audit trail with a gap in it at the start is
// not an audit trail.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Action is the closed set of audited actions. Adding one means adding a constant,
// which is the point: the list is reviewable.
type Action string

const (
	ActionLogin         Action = "login"
	ActionLoginFailed   Action = "login_failed"
	ActionLogout        Action = "logout"
	ActionAccountLocked Action = "account_locked"
	ActionAccountUnlock Action = "account_unlocked"

	ActionUserInvited     Action = "user_invited"
	ActionInviteAccepted  Action = "invite_accepted"
	ActionPasswordChanged Action = "password_changed"
	ActionRoleChanged     Action = "role_changed"
	ActionUserDisabled    Action = "user_disabled"
	ActionUserEnabled     Action = "user_enabled"
	ActionSessionsRevoked Action = "sessions_revoked"

	ActionProjectCreated       Action = "project_created"
	ActionProjectUpdated       Action = "project_updated"
	ActionProjectArchived      Action = "project_archived"
	ActionProjectUnarchived    Action = "project_unarchived"
	ActionProjectMemberAdded   Action = "project_member_added"
	ActionProjectMemberRemoved Action = "project_member_removed"

	ActionArtifactUploaded Action = "artifact_uploaded"
	ActionArtifactDeleted  Action = "artifact_deleted"

	ActionJobSubmitted Action = "job_submitted"
	ActionJobCancelled Action = "job_cancelled"

	ActionWebhookRejected Action = "webhook_rejected"
	ActionSetupCompleted  Action = "setup_completed"

	ActionSettingChanged Action = "setting_changed"
	ActionSecretRotated  Action = "secret_rotated"
	ActionRunTriggered   Action = "run_triggered"
	ActionRunCancelled   Action = "run_cancelled"

	// ActionTargetRejected is the SSRF boundary saying no. It is audited because a
	// rejected target is either a mistake worth seeing or an attempt worth
	// investigating, and neither is visible if the refusal only reaches the caller
	// (BE-4.7.5).
	ActionTargetRejected   Action = "target_rejected"
	ActionAllowlistChanged Action = "allowlist_changed"
	ActionProjectApproval  Action = "project_approval_changed"
	ActionIntegrationSaved Action = "integration_saved"

	// Defects are audited because they are the record a team acts on: who filed it,
	// who moved it, and who decided two failures were the same problem.
	ActionDefectFiled         Action = "defect_filed"
	ActionDefectStatusChanged Action = "defect_status_changed"
	ActionDefectLinked        Action = "defect_linked"

	// A repository connection is audited because it names an external system this
	// platform will authenticate to and clone from.
	ActionRepositoryConnected    Action = "repository_connected"
	ActionRepositoryDisconnected Action = "repository_disconnected"

	// Quarantines are audited because they are a decision to stop a test from failing
	// the suite: who excused it, who took it on, and who let it go (BE-7.6.3).
	ActionTestQuarantined Action = "test_quarantined"
	ActionQuarantineOwned Action = "quarantine_owner_assigned"
	ActionQuarantineEnded Action = "quarantine_released"

	// A mock server is audited because it is a URL that answers like a client's API and
	// can be configured to fail: somebody will eventually ask why a test run looked
	// wrong while it was up (BE-8.6).
	ActionMockStarted Action = "mock_started"
	ActionMockStopped Action = "mock_stopped"

	// Performance and security runs are audited because they generate traffic at a
	// target: the question after one is who authorised it, against what host (BE-9.5.3).
	ActionPerformanceRun Action = "performance_run"
	ActionSecurityScan   Action = "security_scan"
	ActionHostConfirmed  Action = "risk_host_confirmed"
)

// allActions is every action the platform records. It exists so a filter on a
// typo is refused rather than answered with an empty page, and so the list is
// reviewable in one place.
var allActions = []Action{
	ActionLogin, ActionLoginFailed, ActionLogout, ActionAccountLocked, ActionAccountUnlock,
	ActionUserInvited, ActionInviteAccepted, ActionPasswordChanged, ActionRoleChanged,
	ActionUserDisabled, ActionUserEnabled, ActionSessionsRevoked,
	ActionProjectCreated, ActionProjectUpdated, ActionProjectArchived, ActionProjectUnarchived,
	ActionProjectMemberAdded, ActionProjectMemberRemoved,
	ActionArtifactUploaded, ActionArtifactDeleted,
	ActionJobSubmitted, ActionJobCancelled,
	ActionWebhookRejected, ActionSetupCompleted,
	ActionSettingChanged, ActionSecretRotated, ActionRunTriggered, ActionRunCancelled,
	ActionTargetRejected, ActionAllowlistChanged,
	ActionDefectFiled, ActionDefectStatusChanged, ActionDefectLinked,
	ActionRepositoryConnected, ActionRepositoryDisconnected,
	ActionTestQuarantined, ActionQuarantineOwned, ActionQuarantineEnded,
	ActionMockStarted, ActionMockStopped,
	ActionPerformanceRun, ActionSecurityScan, ActionHostConfirmed,
	ActionProjectApproval, ActionIntegrationSaved,
}

// Known reports whether the action is one this platform records.
func (a Action) Known() bool {
	for _, candidate := range allActions {
		if candidate == a {
			return true
		}
	}
	return false
}

// Actions returns every audited action, for a filter dropdown.
func Actions() []Action { return allActions }

// Entry is one audited action.
type Entry struct {
	Action Action

	// Actor is nil for something the system did on its own, such as a scheduled
	// drift check.
	ActorID    *uuid.UUID
	ActorEmail string

	// Subject is what was acted on: an email, a setting key, a host.
	Subject string

	ProjectID *uuid.UUID
	IP        *netip.Addr

	// Detail is marshalled to jsonb. Never put a credential in it.
	Detail map[string]any
}

// Recorder writes audit entries.
type Recorder struct {
	db *store.DB
}

func NewRecorder(db *store.DB) *Recorder {
	return &Recorder{db: db}
}

// Record writes one entry.
//
// A failure to audit is logged at error level and swallowed. That is a deliberate
// trade: refusing a successful login because the audit insert failed would turn a
// logging problem into an outage, and the structured log still carries the event.
func (r *Recorder) Record(ctx context.Context, entry Entry) {
	detail := json.RawMessage(`{}`)
	if len(entry.Detail) > 0 {
		encoded, err := json.Marshal(entry.Detail)
		if err != nil {
			slog.ErrorContext(ctx, "marshal audit detail",
				"action", string(entry.Action), "error", err)
		} else {
			detail = encoded
		}
	}

	params := dbgen.RecordAuditEntryParams{
		ActorID:    entry.ActorID,
		ActorEmail: entry.ActorEmail,
		Action:     string(entry.Action),
		Subject:    entry.Subject,
		ProjectID:  entry.ProjectID,
		Detail:     detail,
	}
	params.Ip = entry.IP

	if err := r.db.Queries().RecordAuditEntry(ctx, params); err != nil {
		slog.ErrorContext(ctx, "record audit entry",
			"action", string(entry.Action), "subject", entry.Subject, "error", err)
	}
}
