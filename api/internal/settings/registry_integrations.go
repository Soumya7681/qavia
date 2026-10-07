package settings

import "github.com/hyscaler/qavia/api/internal/role"

// External integration settings (BE-10.2, BE-10.3, BE-10.5).
//
// Every one of these is optional and additive. Unconfigured, the built-in path is the
// product: the internal defect tracker, archive upload and clone-by-URL, the in-app
// notification centre, manual and scheduled runs. An empty screen here limits nothing
// (requirements.md 5.4).
//
// The pattern is the same for each: a base URL, an account, and an encrypted token,
// read live at the point of use so a rotated credential takes effect on the next call.
// Whether the integration is *active* is not a flag here — it is whether the settings it
// needs are present, which the adapter's Available() reports.

const CategoryIntegrations = "Integrations"

func init() { declareIntegrations() }

func declareIntegrations() {
	// --- Jira (BE-10.2). The internal defect stays the source of truth; Jira is a
	// mirror, so these configure where the mirror lands.
	Declare(Entry{
		Key:      "jira.base_url",
		Category: CategoryIntegrations,
		Label:    "Jira base URL",
		HelpText: "Your Jira Cloud site, for example https://acme.atlassian.net. " +
			"Unconfigured, defects live only in the built-in tracker.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeGlobal, ScopeProject},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "jira.email",
		Category: CategoryIntegrations,
		Label:    "Jira account email",
		HelpText: "The account the API token belongs to. Jira Cloud authenticates with " +
			"email plus token.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeGlobal, ScopeProject},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "jira.api_token",
		Category: CategoryIntegrations,
		Label:    "Jira API token",
		HelpText: "A Jira API token, scoped to the account above. Encrypted at rest and " +
			"never returned; replace it rather than editing it.",
		Kind:    KindSecret,
		Scopes:  []Scope{ScopeGlobal, ScopeProject},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "jira.project_key",
		Category: CategoryIntegrations,
		Label:    "Jira project key",
		HelpText: "The Jira project defects are filed in, for example QA. Per project, so " +
			"one client's defects never land in another client's Jira.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeGlobal, ScopeProject},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "jira.issue_type",
		Category: CategoryIntegrations,
		Label:    "Jira issue type",
		HelpText: "The issue type filed for a defect, for example Bug.",
		Kind:     KindString,
		Default:  "Bug",
		Scopes:   []Scope{ScopeGlobal, ScopeProject},
		MinRole:  role.Admin,
	})
}
