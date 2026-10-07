package apierr

import (
	"fmt"
	"net/http"
)

// Every code in this file has a row in docs/error-codes.md. A new code adds a
// row in the same PR; CI checks the two agree (BE-X.4).
const (
	// Generic
	CodeInternal        = "internal_error"
	CodeValidation      = "validation_failed"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeRateLimited     = "rate_limited"
	CodeIdempotencyKey  = "idempotency_key_conflict"
	CodeNotImplemented  = "not_implemented"
	CodeServiceDegraded = "service_degraded"

	// Authentication and authorisation
	CodeUnauthenticated = "unauthenticated"
	// G101 fires on the name, not the value. This is an error code returned to a
	// client, and there is no credential anywhere near it.
	CodeInvalidCredentials = "invalid_credentials" //nolint:gosec
	CodeAccountLocked      = "account_locked"
	CodeAccountDisabled    = "account_disabled"
	CodeInviteInvalid      = "invite_invalid"
	CodePasswordWeak       = "password_too_weak"
	CodeForbidden          = "forbidden"
	CodeRoleRequired       = "role_required"
	CodeNotProjectMember   = "not_project_member"

	// Users
	CodeUserNotFound      = "user_not_found"
	CodeEmailAlreadyTaken = "email_already_taken"
	CodeSelfDemotion      = "cannot_change_own_role"

	// Projects
	CodeProjectNotFound = "project_not_found"
	CodeProjectArchived = "project_archived"

	// Artifacts and uploads
	CodeArtifactNotFound     = "artifact_not_found"
	CodeUploadTooLarge       = "upload_too_large"
	CodeUnsupportedMediaType = "unsupported_media_type"
	CodeArchiveRejected      = "archive_rejected"
	CodeUploadCorrupt        = "upload_corrupt"

	// Settings
	CodeSettingUnknownKey  = "setting_unknown_key"
	CodeSettingInvalid     = "setting_invalid_value"
	CodeSettingScope       = "setting_wrong_scope"
	CodeSettingRoleTooLow  = "setting_role_too_low"
	CodeSecretNotReadable  = "secret_not_readable"
	CodeStorageUnreachable = "storage_unreachable"

	// Jobs
	CodeJobNotFound      = "job_not_found"
	CodeJobNotCancelable = "job_not_cancelable"

	// Setup
	CodeSetupComplete = "setup_already_complete"

	// Triggers
	CodeWebhookSignature = "webhook_signature_invalid"
	CodeWebhookStale     = "webhook_timestamp_stale"
	CodeWebhookReplayed  = "webhook_replayed"

	// AI
	CodeProviderNotConfigured = "ai_provider_not_configured"
	CodeTierNotAssigned       = "ai_tier_not_assigned"
	CodeSpendCeilingReached   = "ai_spend_ceiling_reached"
	CodeExternalAINotApproved = "external_ai_not_approved"
	CodeProviderUnavailable   = "ai_provider_unavailable"
	CodeProviderCredentials   = "ai_credentials_invalid" //nolint:gosec // an error code, not a credential
	CodeModelResponseInvalid  = "ai_response_invalid"
	CodeModelUnusableForTier  = "ai_model_unusable_for_tier"
	CodeProviderInUse         = "ai_provider_in_use"

	// Ingest and generation
	CodeSpecificationInvalid = "specification_invalid"
	CodeNoEndpoints          = "no_endpoints_ingested"
	CodeNoRequirements       = "no_requirements_extracted"
	CodeNoApprovedCases      = "no_approved_test_cases"

	// Execution (phase 4)
	CodeTargetHostNotAllowed    = "target_host_not_allowed"
	CodeNoTargetConfigured      = "no_target_configured"
	CodeTargetAddressNotAllowed = "target_address_not_allowed"
	CodeTargetUnresolvable      = "target_unresolvable"
	CodeRunnerUnavailable       = "runner_unavailable"
	CodeRunNotFound             = "run_not_found"
	CodeRunNotCancelable        = "run_not_cancelable"
	CodeRunAlreadyActive        = "run_already_active"
	CodeRunResultNotFound       = "run_result_not_found"

	// Analysis and defects (phase 5)
	CodeAnalysisNotFound   = "analysis_not_found"
	CodeAnalysisNoEvidence = "analysis_without_evidence"
	CodeAnalysisNotReady   = "analysis_not_ready"
	CodeDefectNotFound     = "defect_not_found"
	CodeDefectNotClosable  = "defect_not_closable"
	CodeReportNotFound     = "report_not_found"
	CodeReportNotReady     = "report_not_ready"

	// Repositories (phase 6)
	CodeNoRepository          = "no_repository_connected"
	CodeRepositoryUnreachable = "repository_unreachable"
	CodeRepositoryTooLarge    = "repository_too_large"
	CodeNoRepositoryMap       = "no_repository_map"
	CodeUnknownStack          = "unknown_stack"
	CodeNoCoverageTool        = "no_coverage_tool"
	CodeNoCoverageMeasured    = "no_coverage_measured"

	// UI tests (phase 7)
	CodeFlowGraphNotFound  = "flow_graph_not_found"
	CodeNoFlowGraph        = "no_flow_graph"
	CodeNoBrowserDriver    = "no_browser_driver"
	CodeQuarantineNotFound = "quarantine_not_found"

	// Test data and mocks (phase 8)
	CodeNoMockServer    = "no_mock_server"
	CodeMockUnavailable = "mock_unavailable"
	CodeNoMockRoutes    = "no_mock_routes"

	// Performance and security (phase 9)
	CodeTestKindDisabled       = "test_kind_disabled"
	CodeHostConfirmationNeeded = "host_confirmation_required"
	CodeNoPerformanceMetrics   = "no_performance_metrics"
	CodeSecurityScanNotFound   = "security_scan_not_found"
	CodeNoEndpointsToProbe     = "no_endpoints_to_probe"

	// Integrations (phase 10)
	CodeMCPServerNotFound        = "mcp_server_not_found"
	CodeMCPToolDenied            = "mcp_tool_denied"
	CodeMCPUnreachable           = "mcp_unreachable"
	CodeIntegrationNotConfigured = "integration_not_configured"
)

// ---------------------------------------------------------------- generic

// Internal wraps an unexpected failure. The mapper replaces the message with an
// incident ID, so nothing here reaches a client.
func Internal(cause error) *Error {
	return (&Error{
		Code:    CodeInternal,
		Status:  http.StatusInternalServerError,
		Message: "Something went wrong on our side.",
	}).WithCause(cause)
}

// Validation reports semantically invalid input. Shape validation already
// happened in the request validator middleware, so this is for rules the schema
// cannot express.
func Validation(message string, details map[string]any) *Error {
	e := New(CodeValidation, http.StatusBadRequest, message)
	if details != nil {
		return e.WithDetails(details)
	}
	return e
}

func NotFound(what string) *Error {
	return New(CodeNotFound, http.StatusNotFound, fmt.Sprintf("%s not found.", what))
}

func Conflict(message string) *Error {
	return New(CodeConflict, http.StatusConflict, message)
}

func RateLimited(retryAfterSeconds int) *Error {
	return New(CodeRateLimited, http.StatusTooManyRequests,
		"Too many requests. Wait a moment and try again.").
		WithDetails(map[string]any{"retryAfterSeconds": retryAfterSeconds})
}

// IdempotencyKeyConflict fires when a key is reused with a different payload.
// Replaying the same payload is a success, not an error.
func IdempotencyKeyConflict() *Error {
	return New(CodeIdempotencyKey, http.StatusConflict,
		"This Idempotency-Key was already used with a different request body.")
}

// NotImplemented backs the "coming soon" test types, which are visibly disabled
// rather than hidden (F-3.12).
func NotImplemented(what string) *Error {
	return New(CodeNotImplemented, http.StatusNotImplemented,
		fmt.Sprintf("%s is not available yet.", what))
}

// ------------------------------------------------ authentication and access

func Unauthenticated() *Error {
	return New(CodeUnauthenticated, http.StatusUnauthorized, "Sign in to continue.")
}

// InvalidCredentials is deliberately identical for an unknown account and a
// wrong password. Distinguishing them leaks which accounts exist.
func InvalidCredentials() *Error {
	return New(CodeInvalidCredentials, http.StatusUnauthorized,
		"Email or password is incorrect.")
}

func AccountLocked(retryAfterSeconds int) *Error {
	return New(CodeAccountLocked, http.StatusLocked,
		"Too many failed sign-in attempts. This account is locked temporarily.").
		WithDetails(map[string]any{"retryAfterSeconds": retryAfterSeconds})
}

func AccountDisabled() *Error {
	return New(CodeAccountDisabled, http.StatusForbidden,
		"This account is disabled. Ask an admin to re-enable it.")
}

func InviteInvalid() *Error {
	return New(CodeInviteInvalid, http.StatusBadRequest,
		"This invitation link is invalid or has expired. Ask an admin to send a new one.")
}

func PasswordTooWeak(reason string) *Error {
	return New(CodePasswordWeak, http.StatusBadRequest, reason)
}

func Forbidden() *Error {
	return New(CodeForbidden, http.StatusForbidden,
		"You do not have access to this.")
}

func RoleRequired(required string) *Error {
	return New(CodeRoleRequired, http.StatusForbidden,
		fmt.Sprintf("This action requires the %s role.", required))
}

func NotProjectMember() *Error {
	return New(CodeNotProjectMember, http.StatusForbidden,
		"You are not a member of this project. Ask the project owner to add you.")
}

// ------------------------------------------------------------------- users

func UserNotFound(id fmt.Stringer) *Error {
	return New(CodeUserNotFound, http.StatusNotFound, "User not found.").
		WithCause(fmt.Errorf("user %s", id))
}

func EmailAlreadyTaken() *Error {
	return New(CodeEmailAlreadyTaken, http.StatusConflict,
		"An account with that email already exists.")
}

func CannotChangeOwnRole() *Error {
	return New(CodeSelfDemotion, http.StatusForbidden,
		"You cannot change your own role. Ask another admin.")
}

// ---------------------------------------------------------------- projects

func ProjectNotFound(id fmt.Stringer) *Error {
	return New(CodeProjectNotFound, http.StatusNotFound, "Project not found.").
		WithCause(fmt.Errorf("project %s", id))
}

func ProjectArchived() *Error {
	return New(CodeProjectArchived, http.StatusConflict,
		"This project is archived and is read-only. Unarchive it to make changes.")
}

// --------------------------------------------------------------- artifacts

func ArtifactNotFound(id fmt.Stringer) *Error {
	return New(CodeArtifactNotFound, http.StatusNotFound, "File not found.").
		WithCause(fmt.Errorf("artifact %s", id))
}

func UploadTooLarge(limitBytes int64) *Error {
	return New(CodeUploadTooLarge, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("That file is larger than the %d MB upload limit.", limitBytes/(1<<20))).
		WithDetails(map[string]any{"limitBytes": limitBytes})
}

func UnsupportedMediaType(detected string, allowed []string) *Error {
	return New(CodeUnsupportedMediaType, http.StatusUnsupportedMediaType,
		fmt.Sprintf("Files of type %q are not accepted.", detected)).
		WithDetails(map[string]any{"detected": detected, "allowed": allowed})
}

// ArchiveRejected covers path traversal entries, escaping symlinks, and
// compression bombs. The reason is safe to show: it describes the archive, not
// the system.
func ArchiveRejected(reason string) *Error {
	return New(CodeArchiveRejected, http.StatusBadRequest,
		fmt.Sprintf("This archive was rejected: %s", reason))
}

func UploadCorrupt(reason string) *Error {
	return New(CodeUploadCorrupt, http.StatusBadRequest,
		fmt.Sprintf("That file could not be read: %s", reason))
}

// ---------------------------------------------------------------- settings

// SettingUnknownKey fires for an undeclared key. An undeclared key is an error,
// never a zero value: a zero that silently means "off" is the failure mode the
// registry exists to prevent (backend-standards.md 6).
func SettingUnknownKey(key string) *Error {
	return New(CodeSettingUnknownKey, http.StatusBadRequest,
		fmt.Sprintf("%q is not a known setting.", key)).
		WithDetails(map[string]any{"key": key})
}

func SettingInvalid(key, reason string) *Error {
	return New(CodeSettingInvalid, http.StatusBadRequest, reason).
		WithDetails(map[string]any{"key": key})
}

func SettingWrongScope(key, allowed string) *Error {
	return New(CodeSettingScope, http.StatusBadRequest,
		fmt.Sprintf("%q can only be set at %s scope.", key, allowed)).
		WithDetails(map[string]any{"key": key, "allowedScope": allowed})
}

func SettingRoleTooLow(key, required string) *Error {
	return New(CodeSettingRoleTooLow, http.StatusForbidden,
		fmt.Sprintf("Changing %q requires the %s role.", key, required)).
		WithDetails(map[string]any{"key": key, "requiredRole": required})
}

// SecretNotReadable is returned if any code path asks the API to hand back a
// stored secret. The read path returns {isSet, updatedAt, hint} instead.
func SecretNotReadable(key string) *Error {
	return New(CodeSecretNotReadable, http.StatusForbidden,
		"Stored secrets cannot be read back. Replace the value instead.").
		WithDetails(map[string]any{"key": key})
}

func StorageUnreachable(cause error) *Error {
	return (&Error{
		Code:   CodeStorageUnreachable,
		Status: http.StatusServiceUnavailable,
		Message: "File storage is not reachable. " +
			"Check Settings, Storage, or switch back to local disk.",
	}).WithCause(cause)
}

// -------------------------------------------------------------------- jobs

func JobNotFound(id fmt.Stringer) *Error {
	return New(CodeJobNotFound, http.StatusNotFound, "Job not found.").
		WithCause(fmt.Errorf("job %s", id))
}

func JobNotCancelable(status string) *Error {
	return New(CodeJobNotCancelable, http.StatusConflict,
		fmt.Sprintf("This job is %s and can no longer be cancelled.", status))
}

// ------------------------------------------------------------------- setup

// SetupAlreadyComplete makes the first-run admin endpoint unreachable once a
// user exists.
func SetupAlreadyComplete() *Error {
	return New(CodeSetupComplete, http.StatusNotFound, "Setup is already complete.")
}

// ---------------------------------------------------------------- triggers

// WebhookSignatureInvalid is returned for an unknown token, a project with no
// webhook secret, and a signature that does not match.
//
// One error for all three, deliberately. Distinguishing them tells whoever is
// probing which projects exist and which have webhooks configured, and the caller
// with a genuine problem has the same fix in every case.
func WebhookSignatureInvalid() *Error {
	return New(CodeWebhookSignature, http.StatusUnauthorized,
		"The webhook signature is not valid for this body.")
}

// WebhookStale bounds how long a captured request stays useful.
func WebhookStale(toleranceSeconds int) *Error {
	return New(CodeWebhookStale, http.StatusUnauthorized,
		fmt.Sprintf("The webhook timestamp is outside the %d second tolerance window.",
			toleranceSeconds)).
		WithDetails(map[string]any{"toleranceSeconds": toleranceSeconds})
}

// WebhookReplayed refuses a correctly signed delivery that was already accepted.
func WebhookReplayed() *Error {
	return New(CodeWebhookReplayed, http.StatusUnauthorized,
		"This webhook delivery was already accepted. Send a new request rather than replaying one.")
}

// ---------------------------------------------------------------------- ai

func ProviderNotConfigured() *Error {
	return New(CodeProviderNotConfigured, http.StatusConflict,
		"No AI provider is configured. Add one in Settings, AI, Providers before running AI jobs.")
}

func TierNotAssigned(tier string) *Error {
	return New(CodeTierNotAssigned, http.StatusConflict,
		fmt.Sprintf("No model assigned to the %s tier. Configure it in Settings, AI, Tiers.", tier)).
		WithDetails(map[string]any{"tier": tier})
}

func SpendCeilingReached(scope string) *Error {
	return New(CodeSpendCeilingReached, http.StatusConflict,
		fmt.Sprintf("The %s AI spend ceiling has been reached. Raise it in Settings, AI, Budget.", scope)).
		WithDetails(map[string]any{"scope": scope})
}

// ExternalAINotApproved enforces the data-residency rule server-side. A project
// without external_ai_approved may only use providers marked local.
func ExternalAINotApproved() *Error {
	return New(CodeExternalAINotApproved, http.StatusForbidden,
		"This project is not approved for external AI processing, so it can only use a local provider.")
}

// ServiceDegraded reports a dependency that is up but not answering usefully.
//
// 503 rather than 500, and it keeps its message: an operator can act on "the AI
// service is not reachable", and an incident ID would tell them nothing.
func ServiceDegraded(what string) *Error {
	return New(CodeServiceDegraded, http.StatusServiceUnavailable,
		fmt.Sprintf("%s is not reachable right now. The work will be retried.", what))
}

// ProviderUnavailable is a rate limit, an overload, or a connection failure at the
// provider. It is the only class that earns a cross-provider retry.
func ProviderUnavailable(detail string) *Error {
	message := "The AI provider is unavailable or rate limiting."
	if detail != "" {
		message = detail
	}
	return New(CodeProviderUnavailable, http.StatusServiceUnavailable, message)
}

func ProviderCredentialsInvalid(detail string) *Error {
	message := "The AI provider rejected its credentials. Check them in Settings, AI, Providers."
	if detail != "" {
		message = detail
	}
	return New(CodeProviderCredentials, http.StatusBadGateway, message)
}

// ModelResponseInvalid fires when a model could not be held to its schema even
// after the retries. Nothing partial is persisted when this happens.
func ModelResponseInvalid(detail string) *Error {
	message := "The model did not return a usable response."
	if detail != "" {
		message = detail
	}
	return New(CodeModelResponseInvalid, http.StatusBadGateway, message)
}

// ModelUnusableForTier explains a refusal rather than hiding the option, so a UI
// can disable it with the reason attached (F-16.5).
func ModelUnusableForTier(model, tier, reason string) *Error {
	return New(CodeModelUnusableForTier, http.StatusBadRequest,
		fmt.Sprintf("%s cannot serve the %s tier. %s", model, tier, reason)).
		WithDetails(map[string]any{"model": model, "tier": tier, "reason": reason})
}

// ProviderInUse refuses a delete that a tier assignment depends on, and names what
// is depending on it rather than cascading (BE-1.13).
func ProviderInUse(assignments int) *Error {
	return New(CodeProviderInUse, http.StatusConflict,
		fmt.Sprintf("This is used by %d tier assignment(s). Reassign those tiers first.",
			assignments)).
		WithDetails(map[string]any{"assignments": assignments})
}

// ------------------------------------------------- ingest and generation

// SpecificationInvalid names where a specification failed.
//
// The line is the whole point: "invalid JSON" tells somebody staring at a
// 4,000-line file nothing they can act on (BE-2.2).
func SpecificationInvalid(reason string, line int) *Error {
	details := map[string]any{}
	if line > 0 {
		details["line"] = line
	}
	return New(CodeSpecificationInvalid, http.StatusBadRequest,
		fmt.Sprintf("This specification could not be read: %s", reason)).
		WithDetails(details)
}

// NoEndpointsIngested fires when generation is asked for before anything was
// parsed, which is a sequencing mistake rather than a failure.
func NoEndpointsIngested() *Error {
	return New(CodeNoEndpoints, http.StatusConflict,
		"This project has no parsed endpoints yet. Upload a specification and run ingest first.")
}

func NoRequirementsExtracted() *Error {
	return New(CodeNoRequirements, http.StatusConflict,
		"This project has no requirements yet. Run ingest before generating test cases.")
}

// NoApprovedCases stops code generation from writing a suite out of drafts.
//
// Generating from a case somebody is still editing wastes the call and produces a
// file that has to be thrown away, so the refusal names the review step rather
// than proceeding (BE-3.2).
func NoApprovedCases() *Error {
	return New(CodeNoApprovedCases, http.StatusConflict,
		"No test cases are approved yet. Approve the ones you want implemented, then generate code.")
}

// --------------------------------------------------------------- execution

// TargetHostNotAllowed is enforced before enqueue and again after DNS
// resolution. A browser-side check is not a control.
func TargetHostNotAllowed(host string) *Error {
	return New(CodeTargetHostNotAllowed, http.StatusForbidden,
		fmt.Sprintf("Host %q is not on this project's allowlist. Add it in Settings, Project, Targets.", host)).
		WithDetails(map[string]any{"host": host})
}

// TargetAddressNotAllowed is the second half of the allowlist check: the host was
// listed, but what it resolves to is somewhere a run must not reach. It names the
// address, because "not allowed" without the address is unactionable when a host
// has several.
func TargetAddressNotAllowed(host, address, reason string) *Error {
	return New(CodeTargetAddressNotAllowed, http.StatusForbidden,
		fmt.Sprintf("Host %q resolves to %s, which runs cannot reach: %s.", host, address, reason)).
		WithDetails(map[string]any{"host": host, "address": address, "reason": reason})
}

// TargetUnresolvable separates "the platform refused" from "DNS did not answer", so
// a user is not hunting an allowlist for a resolver problem.
func TargetUnresolvable(host string, cause error) *Error {
	return New(CodeTargetUnresolvable, http.StatusUnprocessableEntity,
		fmt.Sprintf("Host %q could not be resolved from the runner, so a run cannot reach it.", host)).
		WithDetails(map[string]any{"host": host}).WithCause(cause)
}

// RunnerUnavailable is the container runtime being unreachable. Retryable, and
// distinct from a suite that failed.
func RunnerUnavailable() *Error {
	return New(CodeRunnerUnavailable, http.StatusServiceUnavailable,
		"The execution runtime is unavailable, so runs cannot start. Generation and review still work.")
}

func RunNotFound() *Error {
	return New(CodeRunNotFound, http.StatusNotFound, "That run does not exist.")
}

// RunNotCancelable is a run that already finished. Cancelling it is not an error
// worth failing a request over, but it is not a no-op either: the caller asked for
// something that did not happen.
func RunNotCancelable(status string) *Error {
	return New(CodeRunNotCancelable, http.StatusConflict,
		fmt.Sprintf("This run is %s, so it cannot be cancelled.", status)).
		WithDetails(map[string]any{"status": status})
}

// RunAlreadyActive is the per-project concurrency cap. It names the limit so the
// answer is "wait or raise it" rather than "try again".
func RunAlreadyActive(active, limit int) *Error {
	return New(CodeRunAlreadyActive, http.StatusConflict,
		fmt.Sprintf("This project already has %d run(s) in flight and the limit is %d. "+
			"Wait for one to finish, or raise the limit in Settings, Runner.", active, limit)).
		WithDetails(map[string]any{"active": active, "limit": limit})
}

func RunResultNotFound() *Error {
	return New(CodeRunResultNotFound, http.StatusNotFound, "That test result does not exist.")
}

func AnalysisNotFound() *Error {
	return New(CodeAnalysisNotFound, http.StatusNotFound, "That analysis does not exist.")
}

// AnalysisWithoutEvidence is the rejection that makes the analysis feature worth
// having. An explanation that cites nothing, or cites something that is not there,
// is not stored: it is retried with the problems fed back (BE-5.3).
func AnalysisWithoutEvidence(problems []string) *Error {
	return New(CodeAnalysisNoEvidence, http.StatusUnprocessableEntity,
		"The analysis did not cite evidence that checks out, so it was not stored.").
		WithDetails(map[string]any{"problems": problems})
}

// AnalysisNotReady is a promotion attempted before the failure has been explained.
// The order matters: a defect promoted from an unanalysed failure carries no root
// cause, and somebody has to work it out again from scratch.
func AnalysisNotReady() *Error {
	return New(CodeAnalysisNotReady, http.StatusConflict,
		"This failure has not been analysed yet, so there is nothing to promote. Run the analysis first.")
}

func DefectNotFound() *Error {
	return New(CodeDefectNotFound, http.StatusNotFound, "That defect does not exist.")
}

// DefectNotClosable is a duplicate being edited directly. The link is the thing to
// change; editing the copy leaves two states to reconcile.
func DefectNotClosable(reason string) *Error {
	return New(CodeDefectNotClosable, http.StatusConflict, reason)
}

// NoRepositoryConnected is asked for whenever a feature needs source code. It is a
// configuration message rather than a failure: everything the platform does from a
// specification keeps working without a repository (F-3.4).
func NoRepositoryConnected() *Error {
	return New(CodeNoRepository, http.StatusConflict,
		"This project has no repository connected, so there is no source code to read. "+
			"Connect one in Settings, Project, or upload an archive.")
}

// RepositoryUnreachable is a clone that failed. Retryable, and it carries git's own
// message because that message is usually the actionable part: a wrong branch, a
// rejected token, a host that does not resolve.
func RepositoryUnreachable(cause error) *Error {
	return New(CodeRepositoryUnreachable, http.StatusBadGateway,
		"The repository could not be cloned. Check the URL, the branch, and the access token.").
		WithCause(cause)
}

// RepositoryTooLarge is the workspace quota. Named rather than reported as a disk
// error, because the fix is a setting or a shallower clone.
func RepositoryTooLarge(detail string) *Error {
	return New(CodeRepositoryTooLarge, http.StatusUnprocessableEntity,
		"The repository is larger than this platform is configured to hold. "+detail).
		WithDetails(map[string]any{"detail": detail})
}

// NoRepositoryMap is asked for before any exploration has run. A normal state: it is
// what tells a UI to offer the button rather than the panel.
func NoRepositoryMap() *Error {
	return New(CodeNoRepositoryMap, http.StatusNotFound,
		"This project has no repository map yet. Run an exploration to produce one.")
}

// UnknownStack is a refusal to guess. A test file written in a framework the project
// does not depend on cannot run, so the platform names the files it read and waits for
// an override rather than spending a provider call on something unusable (BE-6.3.3).
func UnknownStack(inspected []string) *Error {
	return New(CodeUnknownStack, http.StatusConflict,
		"This platform could not identify the repository's test framework, so it will not "+
			"guess one. Set repo.stack_override for this project.").
		WithDetails(map[string]any{"inspected": inspected})
}

// NoCoverageTool is a refusal to substitute. The number this platform reports has to
// be the number the client's own CI reports, and only their own tool produces it: a
// figure from instrumentation this platform added would be indefensible the first time
// somebody compared the two (F-7.14.1).
func NoCoverageTool(inspected []string) *Error {
	return New(CodeNoCoverageTool, http.StatusConflict,
		"This repository declares no coverage tool, and this platform will not substitute "+
			"its own: the number would not match your CI. Add a coverage tool to the "+
			"project, or set the stack override.").
		WithDetails(map[string]any{"inspected": inspected})
}

// NoCoverageMeasured is the honest absence. A project with no repository connected has
// no code coverage, and zero would be a claim about code nobody has (BE-6.7.3).
func NoCoverageMeasured(reason string) *Error {
	return New(CodeNoCoverageMeasured, http.StatusNotFound, reason)
}

// ------------------------------------------------------------- ui tests

// QuarantineNotFound covers both "no such quarantine" and "already released": to a
// caller trying to claim or release one, there is nothing live either way.
func QuarantineNotFound() *Error {
	return New(CodeQuarantineNotFound, http.StatusNotFound,
		"That quarantine does not exist, or it has already been released.")
}

// NoMockServer is asked for before a mock has ever been started. A normal state: it is
// what tells a UI to offer the start button rather than a status panel.
// TestKindDisabled is a performance or security run on a project that has not enabled
// it. Disabled by default is the control: these are the two kinds that are an attack
// when pointed at the wrong host (BE-9.5.1).
// MCPServerNotFound is an unknown MCP server.
func MCPServerNotFound() *Error {
	return New(CodeMCPServerNotFound, http.StatusNotFound, "That MCP server does not exist.")
}

// MCPToolDenied is a tool call the server's allowlist does not permit. Deny-all is the
// rule, so a tool that was never opted into is refused before it leaves the platform
// (BE-10.1.3).
func MCPToolDenied(server, tool string) *Error {
	return New(CodeMCPToolDenied, http.StatusForbidden,
		fmt.Sprintf("The tool %q is not allowlisted on the MCP server %q.", tool, server)).
		WithDetails(map[string]any{"server": server, "tool": tool})
}

// MCPUnreachable is a server that could not be connected to for a test or a call.
func MCPUnreachable(detail string) *Error {
	return New(CodeMCPUnreachable, http.StatusBadGateway,
		"The MCP server could not be reached. "+detail)
}

// IntegrationNotConfigured is asked of an adapter that is not set up. A normal state,
// reported so a UI can say "using built-in X" rather than showing an error.
func IntegrationNotConfigured(name string) *Error {
	return New(CodeIntegrationNotConfigured, http.StatusConflict,
		fmt.Sprintf("%s is not configured, so the built-in is in use.", name))
}

func TestKindDisabled(kind string) *Error {
	return New(CodeTestKindDisabled, http.StatusConflict,
		fmt.Sprintf("%s testing is disabled for this project. A QA Lead or Admin must enable it first.", kind)).
		WithDetails(map[string]any{"kind": kind})
}

// HostConfirmationRequired is the first run against a new host. The UI turns this into
// a "confirm you mean <host>" prompt; a run resent with the host named passes (BE-9.5.2).
func HostConfirmationRequired(host, kind string) *Error {
	return New(CodeHostConfirmationNeeded, http.StatusConflict,
		fmt.Sprintf("This is the first %s run against %s. Confirm the host to authorise it.", kind, host)).
		WithDetails(map[string]any{"host": host, "kind": kind})
}

// NoPerformanceMetrics is a metrics read for a run that has none: a functional run, or
// a performance run that produced no summary.
func NoPerformanceMetrics() *Error {
	return New(CodeNoPerformanceMetrics, http.StatusNotFound,
		"This run has no performance metrics. Only a performance run produces them.")
}

func SecurityScanNotFound() *Error {
	return New(CodeSecurityScanNotFound, http.StatusNotFound, "That security scan does not exist.")
}

// NoEndpointsToProbe is a security scan on a project with no parsed specification: there
// is nothing to aim a probe at.
func NoEndpointsToProbe() *Error {
	return New(CodeNoEndpointsToProbe, http.StatusConflict,
		"This project's specification declares no endpoints, so there is nothing to probe. Upload a specification first.")
}

func NoMockServer() *Error {
	return New(CodeNoMockServer, http.StatusNotFound,
		"This project has no mock server. Start one to get a URL a client application can point at.")
}

// MockUnavailable is a host that cannot run one: no container runtime, no configured
// image, or no address a caller could reach it on. Said with the missing piece named,
// because "unavailable" alone is not something an admin can fix.
func MockUnavailable(reason string) *Error {
	return New(CodeMockUnavailable, http.StatusServiceUnavailable,
		"A mock server cannot be started here. "+reason)
}

// NoMockRoutes is a project whose specification declares nothing to serve. Refused
// rather than started empty: a mock that answers 404 for everything is indistinguishable
// from one that is not running (BE-8.6.1).
func NoMockRoutes() *Error {
	return New(CodeNoMockRoutes, http.StatusConflict,
		"This project's specification declares no endpoints, so there is nothing to mock. "+
			"Upload a specification first.")
}

func FlowGraphNotFound() *Error {
	return New(CodeFlowGraphNotFound, http.StatusNotFound, "That flow graph does not exist.")
}

// NoFlowGraph is asked for before any discovery has run. A normal state, and the thing
// that tells a UI to offer the button rather than the panel.
func NoFlowGraph() *Error {
	return New(CodeNoFlowGraph, http.StatusNotFound,
		"This project has no discovered UI flows yet. Run a discovery to produce them.")
}

// NoBrowserDriver is a worker with no container runtime, or one whose runtime is not
// answering. Said plainly rather than hanging: browser discovery needs a container,
// and a request that cannot get one should fail in a second with a reason (BE-7.1.3).
func NoBrowserDriver(reason string) *Error {
	return New(CodeNoBrowserDriver, http.StatusServiceUnavailable,
		"No browser driver is available right now. "+reason)
}

func ReportNotFound() *Error {
	return New(CodeReportNotFound, http.StatusNotFound, "That report does not exist.")
}

// ReportNotReady separates "still generating" from "never existed". A client polling
// for a document needs to know which one it is looking at.
func ReportNotReady(status string) *Error {
	return New(CodeReportNotReady, http.StatusConflict,
		fmt.Sprintf("This report is %s. It is not ready to download yet.", status)).
		WithDetails(map[string]any{"status": status})
}

func NoTargetConfigured() *Error {
	return New(CodeNoTargetConfigured, http.StatusConflict,
		"This project has no target URL, so tests cannot be executed. "+
			"Generation still works. Add a target in Settings, Project.")
}
