package settings

import "github.com/hyscaler/qavia/api/internal/role"

// AI settings: where the agent service is, what a call may cost, and what happens
// when the budget runs out.
//
// Provider credentials are not here. They live in llm_providers, because a
// provider is a row with models and tier assignments hanging off it, not a single
// value; the settings registry is for values, and modelling a provider as seven
// loose keys would make "which key belongs to which provider" unanswerable.

// CategoryAI groups these on the settings screen.
const CategoryAI = "AI"

func init() { declareAI() }

func declareAI() {
	Declare(Entry{
		Key:      "ai.service_url",
		Category: CategoryAI,
		Label:    "AI service URL",
		HelpText: "Where the Python agent service is reachable. " +
			"It runs alongside the API and holds no database access.",
		Kind:            KindString,
		Default:         "http://localhost:8000",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: true,
	})

	Declare(Entry{
		Key:      "ai.request_timeout",
		Category: CategoryAI,
		Label:    "AI request timeout",
		HelpText: "How long one model call may take before it is abandoned. " +
			"A reasoning model on a long specification is slow; this is not a health check.",
		Kind:    KindDuration,
		Default: "5m",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "ai.max_validation_retries",
		Category: CategoryAI,
		Label:    "Structured output retries",
		HelpText: "How many times a response that fails its schema is retried with the " +
			"validation error fed back. Structured output reduces malformed replies; it does " +
			"not eliminate them.",
		Kind:    KindInt,
		Default: 2,
		Min:     Bound(0),
		Max:     Bound(5),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "ai.spend_ceiling_usd",
		Category: CategoryAI,
		Label:    "Spend ceiling (USD)",
		HelpText: "Total AI spend allowed in the budget period. Zero means no ceiling. " +
			"Checked before a call, and again at enqueue, so a job is refused rather than " +
			"failing halfway through.",
		Kind:    KindNumber,
		Default: 0,
		Min:     Bound(0),
		Scopes:  []Scope{ScopeGlobal, ScopeProject},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "ai.spend_period",
		Category: CategoryAI,
		Label:    "Budget period",
		HelpText: "The window the ceiling applies to.",
		Kind:     KindEnum,
		Enum:     []string{"day", "week", "month"},
		Default:  "month",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "ai.on_ceiling",
		Category: CategoryAI,
		Label:    "When the ceiling is reached",
		HelpText: "Block refuses new AI work until the period rolls over or the ceiling " +
			"is raised. Warn notifies admins and lets the work continue.",
		Kind:    KindEnum,
		Enum:    []string{"block", "warn"},
		Default: "block",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "ai.provider_pin",
		Category: CategoryAI,
		Label:    "Pinned AI provider",
		HelpText: "Project scope only. Empty inherits the global tier assignments; a " +
			"provider ID pins every tier for this project to that provider. This is how a " +
			"client who refuses external processing is kept on a local model (F-16.12).",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})
}
