package settings

import "github.com/hyscaler/qavia/api/internal/role"

// Trigger settings: how a run can start without somebody pressing a button.
//
// All of it is built in. A CI system starts a run through the generic webhook with
// no external platform configured, which is what criterion 0 of requirements.md 9
// asks for.

// CategoryTriggers groups them on the settings screen.
const CategoryTriggers = "Triggers"

func init() { declareTriggers() }

func declareTriggers() {
	Declare(Entry{
		Key:      "triggers.webhook_token",
		Category: CategoryTriggers,
		Label:    "Webhook token",
		HelpText: "Per-project token for POST /api/v1/hooks/{token}. " +
			"It is also the HMAC key the sender signs the body with. " +
			"Replace it to revoke access; it cannot be read back.",
		Kind:    KindSecret,
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "triggers.webhook_tolerance",
		Category: CategoryTriggers,
		Label:    "Webhook timestamp tolerance",
		HelpText: "How far a signed timestamp may be from now, in either direction. " +
			"It bounds how long a captured request stays useful, and it has to allow " +
			"for clock skew on the sender.",
		Kind:    KindDuration,
		Default: "5m",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "triggers.retention_cron",
		Category: CategoryTriggers,
		Label:    "Retention sweep schedule",
		HelpText: "When artifacts older than the retention period are deleted. " +
			"Standard five-field cron, in UTC.",
		Kind:    KindString,
		Default: "0 3 * * *",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
		// The scheduler registers its entries once, at boot.
		RestartRequired: true,
	})
}
