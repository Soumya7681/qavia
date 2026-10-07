# Qavia Error Codes

Every error the API returns carries a stable, machine-readable `code`. **The
frontend branches on the code, never on the message string.** Messages change;
codes do not.

Response envelope, produced only by the mapper in `internal/platform/httpx`:

```json
{
  "code": "target_host_not_allowed",
  "message": "Host \"api.example.test\" is not on this project's allowlist. Add it in Settings, Project, Targets.",
  "details": { "host": "api.example.test" }
}
```

A `500` carries an incident ID and nothing else. The full wrapped cause is in the
structured log against that ID:

```json
{ "code": "internal_error", "message": "Something went wrong on our side.", "incidentId": "9f2c1a7e" }
```

Codes are declared in `api/internal/platform/apierr/errors.go`. A test parses
that file and fails if a code is missing from this table, so the two cannot
drift.

---

## Generic

| Code | Status | When | Notes |
|---|---|---|---|
| `internal_error` | 500 | Unmapped failure | Response carries `incidentId` only. Cause is logged, never returned |
| `validation_failed` | 400 | Semantically invalid input | Shape validation already ran in the request validator. `details` carries field context |
| `not_found` | 404 | Generic missing resource | Prefer a specific code where one exists |
| `conflict` | 409 | Generic state conflict | Prefer a specific code where one exists |
| `rate_limited` | 429 | Too many requests | `details.retryAfterSeconds` |
| `idempotency_key_conflict` | 409 | `Idempotency-Key` reused with a different body | Replaying the same body is a success, not an error |
| `not_implemented` | 501 | A feature not yet delivered | Backs the visibly-disabled "coming soon" test types (F-3.12) |
| `service_degraded` | 200 or 503 | An optional integration failed and the built-in took over | Work is never lost; an admin is alerted (FR-10.6) |

## Authentication and access

| Code | Status | When | Notes |
|---|---|---|---|
| `unauthenticated` | 401 | No valid session | Frontend redirects to login |
| `invalid_credentials` | 401 | Wrong password **or** unknown account | **Deliberately identical for both.** Distinguishing them leaks which accounts exist |
| `account_locked` | 423 | Too many failed attempts | `details.retryAfterSeconds`. An admin can clear it |
| `account_disabled` | 403 | Account disabled by an admin | |
| `invite_invalid` | 400 | Invite token unknown, used, or expired | Single-use, expiring |
| `password_too_weak` | 400 | Password fails the registry policy | Message states the rule |
| `forbidden` | 403 | Authenticated but not permitted | |
| `role_required` | 403 | Role too low for the route group | `message` names the required role |
| `not_project_member` | 403 | Not a member of the project | |

## Users

| Code | Status | When |
|---|---|---|
| `user_not_found` | 404 | Unknown user ID |
| `email_already_taken` | 409 | Invite or update collides with an existing account |
| `cannot_change_own_role` | 403 | An admin tries to change their own role |

## Projects

| Code | Status | When | Notes |
|---|---|---|---|
| `project_not_found` | 404 | Unknown project, or one the caller cannot see | |
| `project_archived` | 409 | Mutating an archived project | Archived projects are read-only, and this is a domain error, not a 500 |

## Artifacts and uploads

| Code | Status | When | Notes |
|---|---|---|---|
| `artifact_not_found` | 404 | Unknown artifact | |
| `upload_too_large` | 413 | Over the configured size cap | `details.limitBytes`. Cap is a setting |
| `unsupported_media_type` | 415 | Sniffed content type not on the allowlist | `details.detected`, `details.allowed`. Sniffed, never trusted from the extension |
| `archive_rejected` | 400 | Path traversal, escaping symlink, or compression bomb | Reason describes the archive, not the system |
| `upload_corrupt` | 400 | File cannot be parsed | Message names the line where possible |

## Settings

| Code | Status | When | Notes |
|---|---|---|---|
| `setting_unknown_key` | 400 | Key is not declared in the registry | An undeclared key is an error, never a zero value |
| `setting_invalid_value` | 400 | Value fails the registry JSON Schema | `details.key` |
| `setting_wrong_scope` | 400 | Setting written at a scope it does not support | `details.allowedScope` |
| `setting_role_too_low` | 403 | Caller's role is below the entry's minimum | `details.requiredRole` |
| `secret_not_readable` | 403 | Any attempt to read a stored secret back | Read path returns `{ isSet, updatedAt, hint }` |
| `storage_unreachable` | 503 | Object store probe failed | Message points at Settings, Storage, or back to local disk |

## Jobs

| Code | Status | When |
|---|---|---|
| `job_not_found` | 404 | Unknown job |
| `job_not_cancelable` | 409 | Job already reached a terminal state |

## Setup

| Code | Status | When | Notes |
|---|---|---|---|
| `setup_already_complete` | 404 | First-run admin endpoint called after a user exists | Permanently 404 thereafter, not 403 |

## Triggers

| Code | Status | When | Notes |
|---|---|---|---|
| `webhook_signature_invalid` | 401 | Unknown token, project with no webhook secret, or a signature that does not match the raw body | One code for all three on purpose: distinguishing them tells a prober which projects exist and which have webhooks configured |
| `webhook_timestamp_stale` | 401 | Signed timestamp outside the tolerance window | `details.toleranceSeconds`. The window applies in both directions, because sender clock skew is real |
| `webhook_replayed` | 401 | A correctly signed delivery that was already accepted | Replaying a captured request is how one delivery becomes two runs |

## Ingest and generation

| Code | Status | When | Notes |
|---|---|---|---|
| `specification_invalid` | 400 | A specification failed to parse or validate | `details.line` where the parser reported one. "Invalid JSON" alone is unactionable on a 4,000-line file |
| `no_endpoints_ingested` | 409 | Generation asked for before anything was parsed | A sequencing mistake, not a failure. The message says to ingest first |
| `no_requirements_extracted` | 409 | Design asked for before extraction produced anything | Same shape as above, one stage later |
| `no_approved_test_cases` | 409 | Code generation asked for with nothing approved | Generating from drafts produces a suite that has to be thrown away. The message names the review step |

## AI

| Code | Status | When | Notes |
|---|---|---|---|
| `ai_provider_not_configured` | 409 | No provider configured | Refused at **enqueue**, not inside a worker twenty minutes later |
| `ai_tier_not_assigned` | 409 | Tier has no model | Message names the tier and the settings screen. Never a silent code default |
| `ai_spend_ceiling_reached` | 409 | Per-provider or total ceiling hit | `details.scope`. Checked before the call |
| `external_ai_not_approved` | 403 | Project lacks `external_ai_approved` and an external provider was assigned | Server-side residency enforcement, not a UI hint |
| `ai_provider_unavailable` | 503 | Provider rate limited, overloaded, or unreachable | The only class that earns a cross-provider retry. Never a 400 |
| `ai_credentials_invalid` | 502 | Provider rejected its credentials | Names the settings screen. Never echoes the provider's own body |
| `ai_response_invalid` | 502 | Model could not be held to its schema after the retries | Nothing partial is persisted when this fires |
| `ai_model_unusable_for_tier` | 400 | Model assigned to a tier it cannot serve | `details.reason` explains, so the UI disables with a reason rather than hiding |
| `ai_provider_in_use` | 409 | Delete refused while a tier assignment references it | Names how many assignments are in the way instead of cascading |
| `service_degraded` | 503 | A dependency is up but not answering usefully | Keeps its message: an incident ID would tell an operator nothing |

## Execution

| Code | Status | When | Notes |
|---|---|---|---|
| `target_host_not_allowed` | 403 | Target not on the project allowlist | Enforced before enqueue **and** again after DNS resolution in a `Dialer.Control` hook |
| `no_target_configured` | 409 | Execution attempted with no target URL | Generation still works. The message says so |
| `target_address_not_allowed` | 403 | Host is allowlisted but resolves to loopback, link-local, private, or a cloud metadata address | `details.address` and `details.reason`. Named because "not allowed" is unactionable when a host has several addresses. A project may opt in to private targets for a local staging environment |
| `target_unresolvable` | 422 | The target host could not be resolved from the runner | Separated from a refusal so a resolver problem is not hunted in an allowlist |
| `runner_unavailable` | 503 | No container runtime is reachable | Retryable, and distinct from a suite that failed. Generation and review keep working |
| `run_not_found` | 404 | Unknown run, or one the caller cannot see | The two are deliberately indistinguishable |
| `run_not_cancelable` | 409 | Cancel asked for on a run that already finished | `details.status` |
| `run_already_active` | 409 | The project's concurrent-run limit is already reached | `details.active` and `details.limit`. The global limit is a worker semaphore; this is the per-project share |
| `run_result_not_found` | 404 | Unknown test result | |

## Analysis and defects

| Code | Status | When | Notes |
|---|---|---|---|
| `analysis_not_found` | 404 | No analysis for this failure yet | A normal state, not a problem: it is what tells the UI to offer the button rather than the panel |
| `analysis_without_evidence` | 422 | An analysis cited nothing, or cited something that is not there | `details.problems` lists every failed citation. The analysis is retried with them fed back and never stored unverified (F-9.2) |
| `analysis_not_ready` | 409 | A failure was promoted to a defect before it was analysed | The order matters: a defect promoted with no root cause makes somebody work it out again |
| `defect_not_found` | 404 | Unknown defect, or one the caller cannot see | The two are deliberately indistinguishable |
| `defect_not_closable` | 409 | A change that conflicts with how duplicates work, such as setting the status to `duplicate` without linking an original | A duplicate is a link, not a label |
| `report_not_found` | 404 | Unknown report, or one the caller cannot see | |
| `report_not_ready` | 409 | Download attempted while the report is still generating | `details.status`. "Not yet" and "never existed" are different answers to a client that is polling |

## Repositories

| Code | Status | When | Notes |
|---|---|---|---|
| `no_repository_connected` | 409 | A feature that needs source code was asked for on a project with none | Configuration, not failure: everything driven by a specification keeps working without a repository |
| `repository_unreachable` | 502 | The clone failed | Carries git's own message, which is usually the actionable part: a wrong branch, a rejected token, a host that does not resolve. Retryable |
| `repository_too_large` | 422 | The checkout exceeded the configured size limit | Measured after the clone, because git has no byte budget. The fix is a shallower clone or a higher limit |
| `no_repository_map` | 404 | No exploration has run for this project yet | A normal state: it is what tells a UI to offer the button rather than the panel |
| `unknown_stack` | 409 | A generated test was asked for on a repository whose framework could not be identified | `details.inspected` lists the files read. A test file in a framework the project does not depend on cannot run, so the platform refuses rather than guessing |
| `no_coverage_tool` | 409 | Coverage was asked for on a repository that declares none | The platform will not substitute its own instrumentation: that number would not match the client's CI, which is the only thing the number is for |
| `no_coverage_measured` | 404 | No coverage has been measured for this project | Absent rather than zero, because zero is a claim about code the platform does not have |

## UI tests

| Code | Status | When | Notes |
|---|---|---|---|
| `flow_graph_not_found` | 404 | Unknown flow graph, or one the caller cannot see | The two are deliberately indistinguishable |
| `no_flow_graph` | 404 | No discovery has run for this project yet | A normal state, like `no_repository_map`: it is what tells a UI to offer the button rather than the panel |
| `no_browser_driver` | 503 | Browser discovery was asked for on a worker with no container runtime, or one whose runtime is not answering | Refused in a second with a reason rather than hanging on a container that will never start |
| `quarantine_not_found` | 404 | Unknown quarantine, or one already released | The two are the same answer to a caller trying to claim or release one: there is nothing live either way |

## Test data and mocks

| Code | Status | When | Notes |
|---|---|---|---|
| `no_mock_server` | 404 | No mock has been started for this project | A normal state: it is what tells a UI to offer the start button rather than a status panel |
| `mock_unavailable` | 503 | This host cannot run a mock: no container runtime, no configured image, or no reachable address | The missing piece is named, because "unavailable" alone is not something an admin can act on |
| `no_mock_routes` | 409 | A mock was asked for on a project whose specification declares no endpoints | Refused rather than started empty: a mock that 404s everything is indistinguishable from one that is not running |

---

## Performance and security

| Code | Status | When | Notes |
|---|---|---|---|
| `test_kind_disabled` | 409 | A performance or security run on a project that has not enabled it | Disabled by default: these two kinds are an attack when pointed at the wrong host |
| `host_confirmation_required` | 409 | The first performance or security run against a new host | `details.host`. The UI turns this into a confirm prompt; a run resent naming the host passes |
| `no_performance_metrics` | 404 | Metrics read for a run that has none | Only a performance run produces them |
| `security_scan_not_found` | 404 | Unknown security scan | |
| `no_endpoints_to_probe` | 409 | A security scan on a project with no parsed specification | Nothing to aim a probe at |

## Integrations

| Code | Status | When | Notes |
|---|---|---|---|
| `mcp_server_not_found` | 404 | Unknown MCP server | |
| `mcp_tool_denied` | 403 | A tool call the server's allowlist does not permit | Deny-all: a tool never opted into is refused before it leaves the platform |
| `mcp_unreachable` | 502 | A server could not be connected to for a test or a call | Carries the connection error |
| `integration_not_configured` | 409 | An adapter operation on an integration that is not set up | A normal state; the built-in is in use |

---

## Adding a code

1. Add the constant and a constructor in `api/internal/platform/apierr/errors.go`.
2. Add a row here in the same PR. The parse test fails otherwise.
3. Describe the code in the relevant `openapi/qavia.yaml` response.
4. Message rules: a sentence, capitalised, actionable, and never containing a
   secret, credential, or raw upstream response body.
