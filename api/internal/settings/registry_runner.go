package settings

import "github.com/hyscaler/qavia/api/internal/role"

// Runner settings: what executes a generated suite, under which runtime, with which
// limits (F-7.1, F-7.3, F-7.13).
//
// Two rules shape this group. Image references are digests, not tags, so an admin
// updating a runner image is a settings change rather than a deploy, and the digest
// that ran is recorded on the run row. Limits are settings too, because the honest
// value differs per installation: a laptop and a runner host do not agree on what a
// reasonable memory ceiling is, and the platform should not pretend otherwise.
//
// Everything here is global. A project cannot lower its own isolation, raise its own
// memory ceiling, or pick its own runtime, because a per-project override of a
// security control is not a control (work.md 8, G2).

// CategoryRunner groups them on the settings screen.
const CategoryRunner = "Runner"

func init() { declareRunner() }

func declareRunner() {
	Declare(Entry{
		Key:      "runner.driver",
		Category: CategoryRunner,
		Label:    "Execution driver",
		HelpText: "How a run is executed. 'docker' talks to a Docker or containerd " +
			"daemon. 'disabled' turns execution off and leaves generation working.",
		Kind:            KindEnum,
		Enum:            []string{"docker", "disabled"},
		Default:         "docker",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: true,
	})

	Declare(Entry{
		Key:      "runner.runtime",
		Category: CategoryRunner,
		Label:    "Container runtime",
		HelpText: "gvisor (runsc) is a user-space kernel, so an escape has to get " +
			"through it before it reaches the host. runc is UNSAFE for this workload " +
			"and exists for local development only: a run executes model-generated " +
			"code derived from a file a client uploaded.",
		Kind:    KindEnum,
		Enum:    []string{"gvisor", "runc"},
		Default: "gvisor",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.max_concurrent_runs",
		Category: CategoryRunner,
		Label:    "Concurrent runs",
		HelpText: "How many containers the platform runs at once across all projects. " +
			"A run holds a slot for its whole wall clock, so this is the real bound on " +
			"runner host load.",
		Kind:    KindInt,
		Default: 3,
		Min:     Bound(1),
		Max:     Bound(64),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.max_runs_per_project",
		Category: CategoryRunner,
		Label:    "Concurrent runs per project",
		HelpText: "Caps one project's share of the runner. Zero means no per-project " +
			"cap beyond the global one.",
		Kind:    KindInt,
		Default: 1,
		Min:     Bound(0),
		Max:     Bound(64),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.Admin,
	})

	declareRunnerImages()
	declareRunnerLimits()
	declareAnalysis()
	declareRepo()
}

// declareRepo is BE-6.1 and BE-6.2: how a repository is fetched and how much of it
// this platform is willing to hold.
//
// The token is not here as an entry: it is declared in the targets group as a
// project-scoped secret, because that is where a project's credentials live and a
// second place for secrets is a second rotation story.
func declareRepo() {
	Declare(Entry{
		Key:      "repo.clone_depth",
		Category: CategoryRunner,
		Label:    "Clone depth",
		HelpText: "How much history to fetch. The platform needs the current tree and " +
			"recent commits, not ten years of them. Zero would mean a full clone, so the " +
			"floor is one.",
		Kind:    KindInt,
		Default: 50,
		Min:     Bound(1),
		Max:     Bound(10000),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "repo.clone_timeout",
		Category: CategoryRunner,
		Label:    "Clone timeout",
		HelpText: "How long a clone may take before it is abandoned. A repository that " +
			"has not arrived by then is either enormous or unreachable.",
		Kind:    KindDuration,
		Default: "10m",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "repo.max_size_mib",
		Category: CategoryRunner,
		Label:    "Repository size limit",
		HelpText: "Megabytes one checkout may occupy on the worker. Measured after the " +
			"clone, because git offers no byte budget: past the limit the job is refused " +
			"rather than the disk being filled.",
		Kind:    KindInt,
		Default: 2048,
		Min:     Bound(64),
		Max:     Bound(51200),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "repo.workspace_root",
		Category: CategoryRunner,
		Label:    "Workspace directory",
		HelpText: "Where per-job checkouts are created on the worker. Empty uses the " +
			"system temporary directory, which is right for a laptop and wrong for a " +
			"runner host with a dedicated volume.",
		Kind:            KindString,
		Default:         "",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: true,
	})

	Declare(Entry{
		Key:      "repo.max_exploration_steps",
		Category: CategoryRunner,
		Label:    "Repository exploration steps",
		HelpText: "How many times the comprehension agent may look at something before " +
			"it has to produce a map. Each step is one call on the code tier against a " +
			"cached prefix, so the cost is roughly linear in this number. Running out is " +
			"not a failure: the map is written from what was seen and marked cut short.",
		Kind:    KindInt,
		Default: 24,
		Min:     Bound(4),
		Max:     Bound(120),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "repo.max_generated_tests",
		Category: CategoryRunner,
		Label:    "Generated unit tests per pass",
		HelpText: "How many untested paths one pass writes tests for. Each is one call " +
			"on the code tier, and a map of a large repository can list hundreds: a pass " +
			"that wrote all of them would be a review nobody finishes.",
		Kind:    KindInt,
		Default: 15,
		Min:     Bound(1),
		Max:     Bound(200),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "repo.stack_override",
		Category: CategoryRunner,
		Label:    "Detected stack override",
		HelpText: "Forces the test framework for a repository whose stack the platform " +
			"could not identify. Leave empty to use what detection found, and check the " +
			"project's repository panel for which files it read.",
		Kind:    KindString,
		Default: "",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})
}

// declareAnalysis is phase 5's switch: whether a finished run explains its own
// failures without being asked (F-9.1).
//
// On by default, because the whole point of the analysis is that a failure arrives
// already explained. It is a setting because it costs provider calls, and an
// installation watching its spend should be able to make it deliberate.
func declareAnalysis() {
	Declare(Entry{
		Key:      "analysis.auto_analyse",
		Category: CategoryRunner,
		Label:    "Explain failures automatically",
		HelpText: "After a run finishes with failures, analyse them without being asked. " +
			"Each failure costs one reasoning-tier call, and the run's log is a cached " +
			"prefix shared across them.",
		Kind:    KindBool,
		Default: true,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "analysis.max_failures",
		Category: CategoryRunner,
		Label:    "Failures analysed per run",
		HelpText: "Caps the fan-out. A suite where 300 tests failed has one cause more " +
			"often than 300, and explaining all of them costs 300 calls to say it.",
		Kind:    KindInt,
		Default: 25,
		Min:     Bound(1),
		Max:     Bound(500),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "analysis.auto_promote",
		Category: CategoryRunner,
		Label:    "File defects automatically",
		HelpText: "Off by default. An analysed failure can be promoted to a defect with " +
			"one click, and a queue that fills itself is a queue people stop reading.",
		Kind:    KindBool,
		Default: false,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})
}

// declareRunnerImages holds one digest-pinned reference per framework family
// (BE-4.3). A tag is mutable, so a tag would mean the platform cannot say which
// image produced a result; the digest is recorded on the run row for exactly that
// reason.
func declareRunnerImages() {
	images := []struct {
		key, label, help, def string
	}{
		{
			key:   "runner.image_node",
			label: "Node runner image",
			help: "Runs Supertest, Jest, and Vitest suites. Pin by digest " +
				"(name@sha256:...), never by tag.",
			def: "ghcr.io/hyscaler/qavia-runner-node",
		},
		{
			key:   "runner.image_playwright",
			label: "Playwright runner image",
			help: "Runs Playwright suites, browsers included. Pin by digest " +
				"(name@sha256:...), never by tag.",
			def: "ghcr.io/hyscaler/qavia-runner-playwright",
		},
		{
			key:   "runner.image_k6",
			label: "k6 runner image",
			help:  "Runs k6 load scripts. Pin by digest (name@sha256:...), never by tag.",
			def:   "ghcr.io/hyscaler/qavia-runner-k6",
		},
		{
			key:   "runner.image_python",
			label: "Python runner image",
			help:  "Runs pytest suites. Pin by digest (name@sha256:...), never by tag.",
			def:   "ghcr.io/hyscaler/qavia-runner-python",
		},
		{
			key:   "runner.image_security",
			label: "Security probe runner image",
			help: "Sends the reviewed security payloads at the target and judges the " +
				"responses. Pin by digest (name@sha256:...), never by tag.",
			def: "ghcr.io/hyscaler/qavia-runner-security",
		},
	}

	for _, image := range images {
		Declare(Entry{
			Key:      image.key,
			Category: CategoryRunner,
			Label:    image.label,
			HelpText: image.help,
			Kind:     KindString,
			Default:  image.def,
			Scopes:   []Scope{ScopeGlobal},
			MinRole:  role.Admin,
		})
	}

	Declare(Entry{
		Key:      "runner.image_net_helper",
		Category: CategoryRunner,
		Label:    "Firewall helper image",
		HelpText: "Carries iptables and nothing else. It is the only container the " +
			"platform grants a capability to, and it programs the egress rules that " +
			"confine a run. Never point this at a runner image: a container that " +
			"executes generated code must not be able to rewrite its own firewall.",
		Kind:    KindString,
		Default: "ghcr.io/hyscaler/qavia-net-helper",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.require_digest",
		Category: CategoryRunner,
		Label:    "Require pinned digests",
		HelpText: "Refuses to start a run whose image is referenced by tag. Turn this " +
			"off only to build images locally, and turn it back on.",
		Kind:    KindBool,
		Default: true,
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.pull_policy",
		Category: CategoryRunner,
		Label:    "Image pull policy",
		HelpText: "'missing' pulls only what the host does not have, which is the " +
			"normal choice for digest-pinned images. 'never' requires the operator to " +
			"load images out of band.",
		Kind:    KindEnum,
		Enum:    []string{"missing", "always", "never"},
		Default: "missing",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})
}

// declareRunnerLimits is BE-4.5. Every limit is enforced by the driver or the
// kernel, never by the test framework, and exceeding one is reported as an
// attributed reason rather than a bare exit code.
func declareRunnerLimits() {
	Declare(Entry{
		Key:      "runner.cpus",
		Category: CategoryRunner,
		Label:    "CPU limit per run",
		HelpText: "CPU cores one container may use, fractions allowed.",
		Kind:     KindNumber,
		Default:  1.0,
		Min:      Bound(0.1),
		Max:      Bound(16),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "runner.memory_mib",
		Category: CategoryRunner,
		Label:    "Memory limit per run",
		HelpText: "Megabytes one container may use. Exceeding it is an OOM kill, " +
			"reported as 'killed: memory limit'. Playwright needs more than an API suite.",
		Kind:    KindInt,
		Default: 2048,
		Min:     Bound(256),
		Max:     Bound(32768),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.pids",
		Category: CategoryRunner,
		Label:    "Process limit per run",
		HelpText: "Maximum processes and threads inside one container. This is what " +
			"stops a fork bomb, and it stops it in the kernel.",
		Kind:    KindInt,
		Default: 512,
		Min:     Bound(32),
		Max:     Bound(8192),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.tmpfs_mib",
		Category: CategoryRunner,
		Label:    "Writable disk per run",
		HelpText: "Size of the in-memory writable layer mounted at /workspace and " +
			"/tmp. The root filesystem is read-only, so this is the only place a run " +
			"can write, and it counts against the memory limit.",
		Kind:    KindInt,
		Default: 512,
		Min:     Bound(64),
		Max:     Bound(8192),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.timeout",
		Category: CategoryRunner,
		Label:    "Run wall clock",
		HelpText: "How long a run may take before the driver kills it. Enforced by the " +
			"platform, not by the test framework's own timeout.",
		Kind:    KindDuration,
		Default: "20m",
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "runner.stale_after",
		Category: CategoryRunner,
		Label:    "Orphan sweep age",
		HelpText: "A container older than this with no live run behind it is reaped by " +
			"the sweeper. Keep it above the wall clock.",
		Kind:    KindDuration,
		Default: "1h",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "runner.retry_failed_tests",
		Category: CategoryRunner,
		Label:    "Retries per failing test",
		HelpText: "A test that fails then passes on retry is reported as flaky rather " +
			"than as a pass or a failure. Zero disables flake detection.",
		Kind:    KindInt,
		Default: 1,
		Min:     Bound(0),
		Max:     Bound(3),
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.QALead,
	})

	Declare(Entry{
		Key:      "runner.allow_private_targets",
		Category: CategoryRunner,
		Label:    "Allow private network targets",
		HelpText: "Off by default: a target resolving to a loopback, link-local, or " +
			"private address is rejected before enqueue and again at dial time. Turn on " +
			"only for a project whose staging environment genuinely lives on a private " +
			"address, and understand that it widens SSRF reach to that network.",
		Kind:    KindBool,
		Default: false,
		Scopes:  []Scope{ScopeProject, ScopeGlobal},
		MinRole: role.Admin,
	})
}
