package settings

import "github.com/hyscaler/qavia/api/internal/role"

// Target settings: where a project's tests run against, and how they authenticate
// (F-3.13).
//
// The important rule is what happens when these are empty: generation still works
// and execution is disabled with a stated reason. A project with no target is a
// normal project that has not been pointed at an environment yet, not a broken one
// (BE-2.13).

// CategoryTargets groups them on the settings screen.
const CategoryTargets = "Targets"

func init() { declareTargets() }

func declareTargets() {
	Declare(Entry{
		Key:      "targets.base_url",
		Category: CategoryTargets,
		Label:    "Target base URL",
		HelpText: "Where generated tests run against, for example https://staging.example.com. " +
			"Leave empty to generate without executing.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "targets.allowlist",
		Category: CategoryTargets,
		Label:    "Allowed target hosts",
		HelpText: "Hosts a run may reach. The base URL's host is allowed automatically. " +
			"Enforced server-side before a run and again after DNS resolution, because a " +
			"browser-side check is not a control.",
		Kind:    KindStringList,
		Default: []string{},
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "targets.auth_mode",
		Category: CategoryTargets,
		Label:    "Target authentication",
		HelpText: "How generated tests authenticate against the target.",
		Kind:     KindEnum,
		Enum:     []string{"none", "bearer", "basic", "oauth2", "api-key"},
		Default:  "none",
		Scopes:   []Scope{ScopeProject, ScopeGlobal},
		MinRole:  role.QALead,
	})

	Declare(Entry{
		Key:      "targets.auth_credential",
		Category: CategoryTargets,
		Label:    "Target credential",
		HelpText: "The token, password, or key the chosen mode needs. " +
			"Encrypted at rest and never returned; replace it rather than editing it.",
		Kind:    KindSecret,
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "targets.auth_header_name",
		Category: CategoryTargets,
		Label:    "API key header",
		HelpText: "Which header carries the key when the mode is api-key.",
		Kind:     KindString,
		Default:  "X-API-Key",
		Scopes:   []Scope{ScopeProject, ScopeGlobal},
		MinRole:  role.QALead,
	})

	Declare(Entry{
		Key:      "repo.access_token",
		Category: CategoryTargets,
		Label:    "Repository access token",
		HelpText: "A read-only token for cloning a private repository. It is used by " +
			"the worker and never enters a runner container, and it is never returned " +
			"once stored.",
		Kind:    KindSecret,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "generation.review_extraction",
		Category: CategoryTargets,
		Label:    "Review extraction before generating",
		HelpText: "Pauses the chain after extraction and notifies, so a QA engineer can " +
			"correct what was understood before test cases are designed from it.",
		Kind:    KindBool,
		Default: false,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QAEngineer,
	})
}
