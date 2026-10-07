package settings

import "github.com/hyscaler/qavia/api/internal/role"

// Platform settings: storage, security, sessions, uploads, jobs, observability.
//
// One Declare call per setting, grouped by category into files, so adding a category
// means adding a file rather than editing a growing one.
//
// Every key here was a candidate to be an environment variable and is deliberately
// not one. Only the six in requirements.md 5.1 stay in the environment: those needed
// before the database is reachable, or that decrypt the settings themselves.

// Category names. They are constants because the API groups by them and a typo would
// silently create a second group in the UI.
const (
	CategoryStorage       = "Storage"
	CategorySecurity      = "Security"
	CategoryJobs          = "Jobs"
	CategoryUploads       = "Uploads"
	CategoryObservability = "Observability"
	CategoryNotifications = "Notifications"
	CategoryPreferences   = "Preferences"
)

func init() {
	declareStorage()
	declareSecurity()
	declareUploads()
	declareJobs()
	declareObservability()
	declareNotifications()
	declarePreferences()
}

func declareStorage() {
	Declare(Entry{
		Key:      "storage.provider",
		Category: CategoryStorage,
		Label:    "Storage provider",
		HelpText: "Where artifacts, logs, and reports are kept. " +
			"Local disk is the built-in default and needs no configuration.",
		Kind:    KindEnum,
		Enum:    []string{"local-disk", "minio", "s3"},
		Default: "local-disk",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
		// Read once at boot when the driver is constructed.
		RestartRequired: true,
	})

	Declare(Entry{
		Key:             "storage.local_path",
		Category:        CategoryStorage,
		Label:           "Local storage path",
		HelpText:        "Directory used by the local-disk provider.",
		Kind:            KindString,
		Default:         "./var/storage",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: true,
	})

	Declare(Entry{
		Key:      "storage.endpoint",
		Category: CategoryStorage,
		Label:    "Endpoint",
		HelpText: "S3-compatible endpoint URL. Leave empty for AWS S3.",
		Kind:     KindString,
		Default:  "",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "storage.region",
		Category: CategoryStorage,
		Label:    "Region",
		Kind:     KindString,
		Default:  "ap-south-1",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "storage.bucket",
		Category: CategoryStorage,
		Label:    "Bucket",
		Kind:     KindString,
		Default:  "qavia",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "storage.access_key",
		Category: CategoryStorage,
		Label:    "Access key",
		HelpText: "Only needed for MinIO or S3.",
		Kind:     KindSecret,
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "storage.secret_key",
		Category: CategoryStorage,
		Label:    "Secret key",
		Kind:     KindSecret,
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "storage.retention_days",
		Category: CategoryStorage,
		Label:    "Artifact retention (days)",
		HelpText: "Logs, screenshots, videos, and traces older than this are deleted.",
		Kind:     KindInt,
		Default:  90,
		Min:      Bound(1),
		Max:      Bound(3650),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})
}

func declareSecurity() {
	Declare(Entry{
		Key:      "security.session_lifetime",
		Category: CategorySecurity,
		Label:    "Session lifetime",
		HelpText: "How long a session lasts regardless of activity.",
		Kind:     KindDuration,
		Default:  "12h",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "security.session_idle_timeout",
		Category: CategorySecurity,
		Label:    "Session idle timeout",
		HelpText: "How long an unused session survives. " +
			"Shorter means an unattended laptop stops being a way in.",
		Kind:    KindDuration,
		Default: "2h",
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "security.login_window",
		Category: CategorySecurity,
		Label:    "Failed login window",
		HelpText: "Period over which failed sign-ins are counted.",
		Kind:     KindDuration,
		Default:  "15m",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "security.max_account_failures",
		Category: CategorySecurity,
		Label:    "Failures before lockout",
		HelpText: "Failed sign-ins within the window before an account is locked.",
		Kind:     KindInt,
		Default:  5,
		Min:      Bound(1),
		Max:      Bound(100),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "security.max_ip_failures",
		Category: CategorySecurity,
		Label:    "Failures per address",
		HelpText: "Failed sign-ins from one address before it is refused. " +
			"This never locks an account, so nobody can lock out a colleague on purpose.",
		Kind:    KindInt,
		Default: 25,
		Min:     Bound(1),
		Max:     Bound(1000),
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "security.lock_duration",
		Category: CategorySecurity,
		Label:    "Lockout duration",
		Kind:     KindDuration,
		Default:  "15m",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "security.argon2_memory_kib",
		Category: CategorySecurity,
		Label:    "Password hashing memory (KiB)",
		HelpText: "Raise as hardware improves. Existing passwords are upgraded on next sign-in.",
		Kind:     KindInt,
		Default:  19456,
		Min:      Bound(8192),
		Max:      Bound(1048576),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "security.argon2_iterations",
		Category: CategorySecurity,
		Label:    "Password hashing iterations",
		Kind:     KindInt,
		Default:  2,
		Min:      Bound(1),
		Max:      Bound(10),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})
}

func declareUploads() {
	Declare(Entry{
		Key:      "uploads.max_bytes",
		Category: CategoryUploads,
		Label:    "Maximum upload size (bytes)",
		Kind:     KindInt,
		Default:  50 << 20,
		Min:      Bound(1024),
		Max:      Bound(2 << 30),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "uploads.allowed_types",
		Category: CategoryUploads,
		Label:    "Allowed content types",
		HelpText: "Matched against the sniffed type, never the file extension.",
		Kind:     KindStringList,
		Default: []string{
			"application/json", "application/yaml", "text/yaml", "text/plain",
			"application/zip", "application/gzip", "application/x-tar",
		},
		Scopes:  []Scope{ScopeGlobal},
		MinRole: role.Admin,
	})

	Declare(Entry{
		Key:      "uploads.max_archive_ratio",
		Category: CategoryUploads,
		Label:    "Maximum archive expansion ratio",
		HelpText: "An archive that expands by more than this is rejected as a bomb.",
		Kind:     KindInt,
		Default:  100,
		Min:      Bound(2),
		Max:      Bound(10000),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})
}

func declareJobs() {
	Declare(Entry{
		Key:      "jobs.max_concurrency",
		Category: CategoryJobs,
		Label:    "Worker concurrency",
		HelpText: "How many jobs one worker process runs at once.",
		Kind:     KindInt,
		Default:  4,
		Min:      Bound(1),
		Max:      Bound(64),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "jobs.max_attempts",
		Category: CategoryJobs,
		Label:    "Retry attempts",
		HelpText: "A failed job is retried this many times with backoff before giving up.",
		Kind:     KindInt,
		Default:  3,
		Min:      Bound(1),
		Max:      Bound(10),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "jobs.ai_fan_out_limit",
		Category: CategoryJobs,
		Label:    "Concurrent AI calls per job",
		HelpText: "Caps fan-out. A 400-endpoint spec must not launch 400 provider calls at once.",
		Kind:     KindInt,
		Default:  4,
		Min:      Bound(1),
		Max:      Bound(32),
		Scopes:   []Scope{ScopeGlobal, ScopeProject},
		MinRole:  role.QALead,
	})
}

func declareObservability() {
	Declare(Entry{
		Key:      "observability.log_level",
		Category: CategoryObservability,
		Label:    "Log level",
		Kind:     KindEnum,
		Enum:     []string{"debug", "info", "warn", "error"},
		Default:  "info",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "observability.trace_exporter",
		Category: CategoryObservability,
		Label:    "Trace exporter",
		HelpText: "Where spans go. None disables tracing without changing any code.",
		Kind:     KindEnum,
		Enum:     []string{"none", "stdout", "otlp"},
		Default:  "none",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
		// The provider is built once at boot.
		RestartRequired: true,
	})

	Declare(Entry{
		Key:             "observability.otlp_endpoint",
		Category:        CategoryObservability,
		Label:           "OTLP endpoint",
		HelpText:        "Collector address, for example localhost:4317.",
		Kind:            KindString,
		Default:         "",
		Scopes:          []Scope{ScopeGlobal},
		MinRole:         role.Admin,
		RestartRequired: true,
	})
}

func declareNotifications() {
	// The in-app channel is built in and has no setting: it always works and needs
	// no configuration (FR-8.2). Everything here is an optional extra channel.
	Declare(Entry{
		Key:      "notifications.smtp_host",
		Category: CategoryNotifications,
		Label:    "SMTP host",
		HelpText: "Optional. Unconfigured, the platform uses the in-app notification centre.",
		Kind:     KindString,
		Default:  "",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.smtp_port",
		Category: CategoryNotifications,
		Label:    "SMTP port",
		Kind:     KindInt,
		Default:  587,
		Min:      Bound(1),
		Max:      Bound(65535),
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.smtp_username",
		Category: CategoryNotifications,
		Label:    "SMTP username",
		Kind:     KindString,
		Default:  "",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.smtp_password",
		Category: CategoryNotifications,
		Label:    "SMTP password",
		Kind:     KindSecret,
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.from_address",
		Category: CategoryNotifications,
		Label:    "From address",
		Kind:     KindString,
		Default:  "qavia@hyscaler.com",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.slack_token",
		Category: CategoryNotifications,
		Label:    "Slack bot token",
		HelpText: "Optional. Unconfigured, run summaries stay in the notification centre.",
		Kind:     KindSecret,
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})

	Declare(Entry{
		Key:      "notifications.slack_channel",
		Category: CategoryNotifications,
		Label:    "Slack channel",
		Kind:     KindString,
		Default:  "",
		Scopes:   []Scope{ScopeGlobal},
		MinRole:  role.Admin,
	})
}

func declarePreferences() {
	// User-scoped, so MinRole is Viewer: everyone edits their own preferences. The
	// scope, not the role, is what stops one user editing another's.
	Declare(Entry{
		Key:      "preferences.timezone",
		Category: CategoryPreferences,
		Label:    "Timezone",
		HelpText: "Timestamps are stored in UTC and displayed in this zone.",
		Kind:     KindString,
		Default:  "Asia/Kolkata",
		Scopes:   []Scope{ScopeUser, ScopeGlobal},
		MinRole:  role.Viewer,
	})

	Declare(Entry{
		Key:      "preferences.theme",
		Category: CategoryPreferences,
		Label:    "Theme",
		Kind:     KindEnum,
		Enum:     []string{"system", "light", "dark"},
		Default:  "system",
		Scopes:   []Scope{ScopeUser},
		MinRole:  role.Viewer,
	})

	Declare(Entry{
		Key:      "preferences.notification_channels",
		Category: CategoryPreferences,
		Label:    "Notification channels",
		HelpText: "Channels you want to be notified on. In-app is always available.",
		Kind:     KindStringList,
		Default:  []string{"in_app"},
		Scopes:   []Scope{ScopeUser, ScopeProject, ScopeGlobal},
		MinRole:  role.Viewer,
	})

	Declare(Entry{
		Key:      "preferences.landing_page",
		Category: CategoryPreferences,
		Label:    "Default landing page",
		Kind:     KindEnum,
		Enum:     []string{"projects", "runs", "defects", "notifications"},
		Default:  "projects",
		Scopes:   []Scope{ScopeUser},
		MinRole:  role.Viewer,
	})
}
