package settings

import "github.com/hyscaler/qavia/api/internal/role"

// UI testing settings: how a discovery signs in, how far it explores, and when a
// chronically flaky UI test stops failing the suite (F-8.1, F-8.2, F-7.12).
//
// The credentials are separate from the target ones on purpose. `targets.auth_*` is
// transport authentication — a bearer token, an API key header — which a suite sends
// with every request. A UI login is a form somebody fills in, and the two are rarely
// the same value: an application can want a session cookie from a login page while its
// API wants a signed token. Sharing one field would force an installation to choose
// which half of its tests work.

// CategoryUITests groups them on the settings screen.
const CategoryUITests = "UI tests"

func init() { declareUITests() }

func declareUITests() {
	Declare(Entry{
		Key:      "ui.discovery_budget",
		Category: CategoryUITests,
		Label:    "Discovery actions",
		HelpText: "How many things the discovery agent may click, type, or open before it " +
			"has to produce a flow graph. Each action is one call on the code tier against " +
			"a cached prefix, so the cost is roughly linear in this number. Running out is " +
			"not a failure: the graph is written from what was seen and marked cut short.",
		Kind:    KindInt,
		Default: 40,
		Min:     Bound(5),
		Max:     Bound(200),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "ui.browser_driver",
		Category: CategoryUITests,
		Label:    "Browser driver",
		HelpText: "Which browser driver drives a discovery. Empty means the bundled " +
			"Playwright container, which needs nothing configured. Name a registered " +
			"external driver to use one instead; the agent does not know the difference.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "ui.login_username",
		Category: CategoryUITests,
		Label:    "UI login username",
		HelpText: "The account a discovery signs in as. Substituted inside the browser " +
			"container wherever the agent asks for $QAVIA_USERNAME, so the value is used " +
			"without being shown to a model or written into a generated spec.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "ui.login_password",
		Category: CategoryUITests,
		Label:    "UI login password",
		HelpText: "The password for that account. Encrypted at rest, never returned, and " +
			"redacted from every log, trace, and recorded flow. Replace it rather than " +
			"editing it.",
		Kind:    KindSecret,
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "mock.image",
		Category: CategoryUITests,
		Label:    "Mock server image",
		HelpText: "The mock server image, by digest. Built by infra/docker/build.sh alongside " +
			"the runner images. A tag would mean a running mock cannot say what produced it.",
		Kind:            KindString,
		Default:         "",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: false,
	})

	Declare(Entry{
		Key:      "mock.host",
		Category: CategoryUITests,
		Label:    "Mock server address",
		HelpText: "The address a client application and a runner container can both reach " +
			"the mock on — usually the Docker bridge address, such as 172.17.0.1. The port " +
			"is chosen per mock and reported with its URL.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "quarantine.auto",
		Category: CategoryUITests,
		Label:    "Quarantine flaky tests automatically",
		HelpText: "A test that flakes more often than the threshold stops failing the run " +
			"and appears on the quarantine list instead. It keeps running and keeps " +
			"recording results, so a fix is visible when it lands. Turn this off to see " +
			"every flake fail the suite.",
		Kind:    KindBool,
		Default: true,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "quarantine.flake_threshold",
		Category: CategoryUITests,
		Label:    "Flaky runs before quarantine",
		HelpText: "How many of the recent runs a test may flake in before the platform " +
			"quarantines it. Two of ten is noise worth watching; three is a test nobody " +
			"trusts.",
		Kind:    KindInt,
		Default: 3,
		Min:     Bound(2),
		Max:     Bound(50),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "quarantine.window_runs",
		Category: CategoryUITests,
		Label:    "Runs the threshold looks at",
		HelpText: "How far back the flake count reaches. A short window reacts quickly and " +
			"quarantines a test that had one bad afternoon; a long one is slower and surer.",
		Kind:    KindInt,
		Default: 10,
		Min:     Bound(3),
		Max:     Bound(200),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "quarantine.max_age",
		Category: CategoryUITests,
		Label:    "Quarantine review age",
		HelpText: "How long a quarantine may stand before it is called stale on the review " +
			"list. Nothing is released automatically: turning a forgotten flaky test into " +
			"a suddenly red suite is the failure this feature exists to prevent.",
		Kind:    KindDuration,
		Default: "336h",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "ui.login_path",
		Category: CategoryUITests,
		Label:    "Login page path",
		HelpText: "Where the sign-in form lives, for example /login. Optional: a discovery " +
			"finds it by following links, and naming it saves the actions that takes.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject},
		MinRole: role.QALead,
	})
}
