# Qavia — Requirements

## 1. Product Summary

Qavia is an internal Hyscaler platform that acts as an AI QA engineer.

A user logs in, creates a project, selects which kinds of tests they want, uploads
what the system needs (API spec, repository, requirement document), and submits.
The system then works in the background: it understands the input, generates test
cases and runnable test code, executes those tests in isolated containers,
analyses failures, and notifies the user when everything is ready.

The user does not babysit the process. They submit and leave.

---

## 2. Scope

### 2.1 In scope

- Internal use by Hyscaler employees only. Single organisation.
- Web application. Login required. All work happens server-side and asynchronously.
- Generation of: test cases, API tests, unit tests, UI tests, performance tests,
  security tests, test data, mock servers.
- Execution of generated tests inside isolated containers.
- AI analysis of failures with root cause and suggested fix.
- Configuration of the platform from the UI, not from environment files.

### 2.2 Out of scope (v1)

- Multi-tenancy, tenant isolation, per-tenant billing.
- Public sign-up, external customers, pricing plans.
- Open-source distribution, self-host packaging for third parties.
- Mobile app testing (Appium), contract testing (Pact), accessibility audits.
- Anything that requires attacking or load-testing a host Hyscaler does not own.

### 2.3 Explicitly deferred

| Item | Deferred to |
|---|---|
| PDF / Word requirement ingestion | Phase 5+ |
| Multiple export formats (Bruno, REST Assured, NUnit, JUnit) | Post-v1 |
| Cypress and Selenium generation (Playwright only in v1) | Post-v1 |
| JMeter and Artillery (k6 only in v1) | Post-v1 |
| Google Workspace SSO | Post-v1. Local login only in v1. Contained addition later — one OIDC login handler plus a settings group, behind the same session middleware, no change to the role or project-scope middleware and no change to routes. |
| Local / on-premise model inference (Qwen, Llama) | See §8.1 |

---

## 3. Users and Roles

| Role | Can do |
|---|---|
| **Admin** | Everything, plus global settings, LLM keys, runner config, user management, host allowlists |
| **QA Lead** | Create projects, configure project settings, approve generated tests, trigger runs, view all projects |
| **QA Engineer** | Create projects they own, generate, review, edit, run tests on their own projects |
| **Viewer** | Read-only access to projects, runs, reports |

Auth: local email and password. Passwords hashed with Argon2id. Sessions are
server-side, so revoking access is immediate. An admin invites users and assigns
roles; there is no self-registration.

SSO is deferred (§2.3).

---

## 4. Functional Requirements

### FR-1 — Project creation and input

- **FR-1.1** User creates a project with a name and description.
- **FR-1.2** User selects one or more test types to generate. Available types:
  test cases, API tests, unit tests, UI tests, performance tests, security tests,
  test data, mock server. Types not yet implemented are visibly disabled with a
  "coming soon" label, not hidden.
- **FR-1.3** User uploads or connects at least one input source:
  - OpenAPI / Swagger file (JSON or YAML)
  - Postman collection
  - Git repository (GitHub or GitLab, via connected account)
  - Requirement text pasted directly
  - Requirement document (PDF, DOCX) — Phase 5+
  - Database schema (SQL dump) — Phase 5+
- **FR-1.4** Uploaded files are stored with a SHA-256 checksum. Re-uploading an
  identical file does not re-run generation.
- **FR-1.5** User optionally supplies a target base URL and authentication details
  so tests can be executed. Without a target URL, generation still works;
  execution is disabled with a clear reason shown.
- **FR-1.6** On submit, the system creates a job chain and returns immediately.
  The user may close the browser.

### FR-2 — Understanding the input

- **FR-2.1** The system extracts, from whatever input it was given: features,
  endpoints, request and response schemas, validation rules, authentication
  scheme, business rules, user flows, and edge cases.
- **FR-2.2** Every extracted item is stored as a `requirement` row, traceable back
  to the artifact and location it came from.
- **FR-2.3** Extraction output is shown to the user for review before generation
  proceeds, if the project has "review before generate" enabled (a project
  setting, default off).

### FR-3 — Test case generation

- **FR-3.1** For each requirement, the system generates test cases covering:
  happy path, each validation rule, each authentication and authorisation state,
  boundary values, and negative cases.
- **FR-3.2** Each test case records: title, preconditions, steps, expected result,
  priority, category, and the requirement it traces to.
- **FR-3.3** Test cases are returned as structured data validated against a schema.
  Free-text parsing is not acceptable.
- **FR-3.4** The user can edit, approve, reject, or add test cases in the UI.
- **FR-3.5** Duplicate test cases across requirements are detected and merged.

### FR-4 — Code generation

- **FR-4.1** Approved test cases are converted to runnable code.
- **FR-4.2** v1 targets: Supertest (API), Postman collection (API),
  Jest / Vitest / pytest (unit, chosen by detected project stack),
  Playwright (UI), k6 (performance).
- **FR-4.3** The framework used is auto-detected from the repository when one is
  connected, and overridable per project from the UI.
- **FR-4.4** Generated files are browsable in the UI as a file tree with syntax
  highlighting, and downloadable as a zip.
- **FR-4.5** Each generated file records which test case IDs it covers.

### FR-5 — Execution

- **FR-5.1** The system executes generated tests inside disposable containers.
- **FR-5.2** Each run records: target URL, start and end time, status, and a
  result row per test case with status, duration, and log location.
- **FR-5.3** Logs, screenshots, videos, and traces are stored in object storage
  and linked from the result.
- **FR-5.4** Runs respect concurrency, timeout, CPU, and memory limits configured
  in settings.
- **FR-5.5** A run can only target a host on the project's allowlist. See §8.3.
- **FR-5.6** The user can watch a live log tail while a run is in progress, and
  can cancel a run.
- **FR-5.7** Flaky tests are detected by re-running failures and flagged, not
  silently reported as failed.

### FR-6 — Failure analysis

- **FR-6.1** For each failed test, the system produces: reason, probable root
  cause, suggested fix, and evidence (the specific log lines, response body, or
  source location that supports the conclusion).
- **FR-6.2** When a repository is connected, analysis includes the relevant source
  file and, where available, the commit that last touched it.
- **FR-6.3** Analysis does not display a confidence percentage unless that number
  is derived from a measurable signal. Permitted signals: re-run stability across
  N attempts, or overlap between the named file and recent commits. A number
  produced by asking the model how confident it is must not be shown.

### FR-7 — Reporting and dashboard

- **FR-7.1** Project dashboard shows: requirement count, generated test count,
  passed, failed, skipped, flaky, last run time, and current job status.
- **FR-7.2** Two distinct coverage metrics are shown, never combined into one
  number:
  - **Requirement coverage** — percentage of requirements with at least one
    passing test case.
  - **Code coverage** — line and branch coverage reported by the underlying
    coverage tool, shown only when a repository is connected.
- **FR-7.3** Recent failures are listed with a direct link to their analysis.
- **FR-7.4** Reports export as HTML and PDF.

### FR-8 — Notifications

- **FR-8.1** The user is notified when a job chain completes, fails, or needs
  their input.
- **FR-8.2** The **in-app notification centre always works** and requires no
  configuration. It is the built-in channel.
- **FR-8.3** Email and Slack are optional additional channels. They appear as
  available only once configured. Unconfigured, they are shown as
  "Not configured" — never as an error, and never blocking a notification.
- **FR-8.4** Each available channel is individually toggleable per user from the UI.
- **FR-8.5** Notification content includes what finished, the outcome summary, and
  a deep link to the result.

### FR-9 — Test maintenance

- **FR-9.1** When an input artifact changes, the system re-ingests it and diffs
  against the previous version.
- **FR-9.2** Each change is classified: endpoint added, endpoint removed,
  signature changed, semantics changed, no change.
- **FR-9.3** For each affected test case the system proposes regenerate, mark
  stale, or delete. The user approves the proposal before anything is applied.
- **FR-9.4** Deleted and replaced test cases are retained in history and can be
  restored.

### FR-10 — Integrations (all optional)

Every item in this group is optional. None is required for the platform to be
fully usable. See §5.4 for the governing rule and the built-in equivalent of each.

- **FR-10.1** GitHub and GitLab: connect account, list repositories, clone,
  receive push and pull-request webhooks. *Optional — built-in fallback is direct
  archive upload or clone-by-URL.*
- **FR-10.2** CI: GitHub Actions and Jenkins can trigger a run and receive a
  result summary as a status check or PR comment. *Optional — built-in fallback is
  the internal scheduler plus a generic inbound webhook.*
- **FR-10.3** Jira: create a bug from a failed test, with the analysis and
  artifacts attached. *Optional — built-in fallback is the internal defect
  tracker, FR-11.*
- **FR-10.4** Slack: post run summaries to a configured channel. *Optional —
  built-in fallback is the in-app notification centre.*
- **FR-10.5** Every integration's credentials and endpoints are configured from
  the UI, per §5. No integration is ever configured by environment variable.
- **FR-10.6** When an integration is configured and later fails (expired token,
  unreachable host), the platform falls back to the built-in equivalent, records
  the failure, and notifies an admin. Work is never lost because a third party is
  down.

### FR-11 — Internal defect tracker (built-in)

The platform ships its own defect tracker so bug tracking never depends on an
external system.

- **FR-11.1** A failed test with an analysis can be promoted to a defect in one
  action.
- **FR-11.2** A defect records: title, description, severity, status, the run
  result it came from, the test case, the requirement it traces to, the analysis,
  and links to logs, screenshots, and videos.
- **FR-11.3** Statuses: open, acknowledged, in progress, fixed, won't fix,
  duplicate. Assignable to a user. Comment thread.
- **FR-11.4** Duplicate detection: a new defect from the same test case with the
  same root cause is linked to the existing open defect rather than duplicated.
- **FR-11.5** Defects list with filters by project, severity, status, assignee.
- **FR-11.6** When Jira **is** configured, a defect can additionally be pushed to
  Jira, and the Jira key is stored on the defect for two-way reference. The
  internal defect remains the source of truth inside Qavia. Jira is a mirror,
  not a replacement.

---

## 5. Settings and Configuration — UI First

**Hard requirement: the platform is configured from the UI. Environment variables
are used only where the application cannot function without them at boot.**

### 5.1 What must stay in the environment

Only these. Nothing else.

| Variable | Why it cannot be a UI setting |
|---|---|
| `DATABASE_URL` | Settings live in the database. Cannot read them without it. |
| `REDIS_URL` | Queue must connect before any job, including settings-dependent ones, can run. |
| `APP_ENCRYPTION_KEY` | Secrets in the settings table are encrypted with it. Cannot be stored in the thing it decrypts. |
| `APP_URL` | Needed to build absolute links in emails sent before an admin has visited settings. |
| `PORT`, `APP_ENV` | Process bootstrap. |

Five variables plus two process basics. That is the whole `.env`.

`APP_ENCRYPTION_KEY` must be a 32-byte random value, base64 encoded. Losing it
means every stored secret becomes unrecoverable and must be re-entered.

### 5.2 What lives in the UI

Everything below is stored in the `settings` table and edited from the admin or
project settings screens. No restart required unless marked.

**AI (global, admin only)**

The platform is provider-agnostic. Users configure one or more AI providers —
Anthropic, AWS Bedrock, Google Vertex, OpenAI, Azure OpenAI, Gemini, or any
OpenAI-compatible endpoint including self-hosted Ollama and vLLM — entirely from
the UI. Prompts and agents reference abstract **tiers** (`reasoning`, `code`,
`cheap`, `vision`), and settings map each tier to a concrete provider and model.

Full design, including the capability matrix, per-provider prompt-caching
differences, and MCP configuration: **`ai-architecture.md`**.

Settings screens:

| Screen | Contents |
|---|---|
| AI → Providers | Add / edit / test / enable providers. Kind, config, credentials (secret), data residency. |
| AI → Models | Per provider: enabled models, tiers served, capabilities, prices, token limits. |
| AI → Tiers | Map each tier to a model, an optional fallback model, and an effort level where supported. |
| AI → Budget | Monthly ceiling per provider and total. Block or warn on reaching it. Current spend by provider, project, and agent. |
| MCP | Configured MCP servers: transport, credentials, tool allowlist, scope, health. |

**Execution runner (global, admin only)**

| Setting | Type | Default |
|---|---|---|
| Container runtime | enum `runc` / `gvisor` / `firecracker` | `gvisor` |
| Max concurrent runs | int | 4 |
| Per-run timeout (seconds) | int | 900 |
| Per-container CPU limit | number (cores) | 1.0 |
| Per-container memory limit (MB) | int | 2048 |
| Outbound network policy | enum `deny-all` / `allowlist` | `allowlist` |
| Global host allowlist | string list | empty |
| Node runner image | string | pinned digest |
| Playwright runner image | string | pinned digest |
| k6 runner image | string | pinned digest |
| Retry failed test for flake detection | int (0 disables) | 2 |

**Storage (global, admin only) — restart required**

| Setting | Type | Secret |
|---|---|---|
| Provider | enum `minio` / `s3` / `local-disk` | no |
| Endpoint | string | no |
| Region | string | no |
| Bucket | string | no |
| Access key | string | yes |
| Secret key | string | yes |
| Artifact retention (days) | int, default 90 | no |

**Notifications (global, admin only) — all optional**

In-app notification is built in and needs no configuration.

Optional: SMTP host, port, username, password (secret), from-address, TLS mode.
Slack bot token (secret) and default channel.

**Integrations (global, admin only) — all optional**

Every one of these is optional. Unconfigured, the platform uses its built-in
equivalent per §5.4 and shows "Not configured — using built-in *X*".

GitHub OAuth app ID and secret. GitLab OAuth app ID and secret.
Jira base URL, email, API token (secret), default project key.
Jenkins base URL and API token (secret).

**Project settings (QA Lead and above, per project)**

| Setting | Type | Default |
|---|---|---|
| Enabled test types | multi-select | as chosen at creation |
| Target base URL | string | none |
| Target host allowlist | string list | derived from base URL |
| Auth mode for tests | enum none/bearer/basic/oauth2/api-key | none |
| Auth credentials | object | none, secret |
| Preferred unit test framework | enum, auto-detected | auto |
| Preferred UI test framework | enum | `playwright` |
| Review extraction before generating | bool | false |
| Auto-run tests after generation | bool | false |
| Model overrides | object | inherit global |
| Notify on completion | multi-select channels | in-app |

**User settings (each user, own only)**

Display name, timezone, theme, per-channel notification toggles, default landing page.

### 5.3 How settings work

- One `settings` table. Rows are keyed by `(scope, scope_id, key)` where scope is
  `global`, `project`, or `user`.
- Resolution order when reading a value: **user → project → global → code default.**
  The first one found wins.
- A **settings registry** in code is the single source of truth. Each entry declares
  key, label, help text, type, default, validation rule, scope, whether it is a
  secret, whether changing it requires a restart, and which role may edit it.
- The settings UI is rendered from the registry. Adding a new setting means adding
  one registry entry — no new UI code, no new migration.
- Secrets are encrypted with AES-256-GCM using `APP_ENCRYPTION_KEY` before being
  written. The API never returns a secret value. It returns
  `{ isSet: true, updatedAt, hint: "sk-ant-…XyZ4" }`.
- Every write to `settings` produces a `settings_audit` row: who, when, which key,
  old value, new value. For secrets, the values are recorded as `[redacted]` and
  only the fact of the change is kept.
- Settings are cached in memory with a short TTL and invalidated on write, so a
  change takes effect on the next job without a deploy.
- A **first-run setup wizard** walks the first admin through the genuinely required
  settings only: an AI provider, and storage if local disk is not acceptable.
  Everything else is skippable and can be added later. The app refuses to enqueue
  AI jobs until a provider is configured, and says so plainly rather than failing
  inside a worker.

### 5.4 Self-contained by default — every external platform is optional

**Governing rule: Qavia works completely on its own infrastructure. Every
external platform is an optional enhancement, configured from the UI. Nothing
outside the platform is required for any core workflow.**

An unconfigured integration is never an error and never blocks a feature. The UI
shows it as "Not configured — using built-in *X*", with a link to configure it.

| Capability | Built-in — always available | Optional external |
|---|---|---|
| Bug tracking | Internal defect tracker (FR-11) | Jira |
| Notification | In-app notification centre | Email (SMTP), Slack |
| File storage | Local disk | MinIO, S3 |
| Source code input | Archive upload (zip / tar), or clone-by-URL with an optional token | GitHub or GitLab OAuth app |
| Run triggering | Manual run, internal scheduler, generic inbound webhook | GitHub Actions, Jenkins |
| Browser control for UI tests | Bundled Playwright container | Playwright MCP server |
| Agent tools (files, grep, git) | Local worker tools | MCP servers |
| Login | Local email and password | — (SSO deferred, §2.3) |
| Database schema input | SQL dump upload | Read-only Postgres MCP server |

**One genuine dependency: a model.** Test generation cannot happen without one.
That dependency is satisfiable entirely on Hyscaler hardware — an `openai-compatible`
provider pointed at a local Ollama or vLLM instance counts as fully self-contained
and needs no external service. See `ai-architecture.md` §3.5.

Implementation rule that makes this real: **code depends on a capability
interface, never on a vendor.** `Notifier`, `DefectTracker`, `SourceProvider`,
`ObjectStore`, `BrowserDriver`, `RunTrigger`. The built-in implementation of each is
registered unconditionally at boot. External adapters are registered only when their
settings are present and valid. No feature branches on "is Jira configured" —
it asks the `DefectTracker` interface and gets whichever implementation is active.

Consequences:

- A fresh install with an empty settings table plus one local model provider is a
  working product.
- Removing an integration's configuration reverts cleanly to the built-in. No
  orphaned records, no broken screens.
- An integration that breaks at runtime degrades to the built-in, records the
  failure, and alerts an admin (FR-10.6).

---

## 6. Non-Functional Requirements

| ID | Requirement |
|---|---|
| NFR-1 | A user submitting a job gets an HTTP response in under 500 ms. All real work is asynchronous. |
| NFR-2 | Generation for a 40-endpoint OpenAPI spec completes in under 15 minutes. |
| NFR-3 | The UI reflects job progress within 5 seconds of a state change. |
| NFR-4 | Jobs are idempotent. A retried job must not duplicate generated rows. |
| NFR-5 | A worker crash loses at most one in-flight job, which is retried automatically up to 3 times with backoff. |
| NFR-6 | Runner containers are destroyed after every run. No container is reused. |
| NFR-7 | All timestamps stored in UTC. All display in the user's configured timezone. |
| NFR-8 | Every AI call records model, input tokens, output tokens, cache read tokens, and cost against the project. |
| NFR-9 | The system runs on a single VM for up to 10 concurrent users, and scales by adding worker processes. |
| NFR-10 | No secret is ever written to application logs. |

---

## 7. Data Model (v1)

```
users               id, email, name, role, timezone, created_at
sessions            id, user_id, expires_at

settings            id, scope, scope_id, key, value(jsonb), is_secret,
                    updated_by, updated_at   -- unique(scope, scope_id, key)
settings_audit      id, scope, scope_id, key, old_value, new_value, actor_id, at

projects            id, name, description, owner_id, created_at, archived_at
project_members     project_id, user_id, role

artifacts           id, project_id, kind, filename, storage_key, sha256,
                    version, created_at
repo_connections    id, project_id, provider, repo_url, default_branch,
                    credential_ref, created_at

jobs                id, project_id, type, status, progress, attempts, error,
                    parent_job_id, payload(jsonb), started_at, finished_at
job_events          id, job_id, level, message, at

requirements        id, project_id, artifact_id, source_ref, kind, title, body
test_cases          id, project_id, requirement_id, title, preconditions,
                    steps(jsonb), expected, priority, category, status,
                    fingerprint, created_at, superseded_by
test_files          id, project_id, framework, path, content, test_case_ids,
                    generated_at

runs                id, project_id, target_url, trigger, status, started_at,
                    finished_at, triggered_by
run_results         id, run_id, test_case_id, test_file_id, status, duration_ms,
                    attempt, log_key, screenshot_key, video_key
analyses            id, run_result_id, reason, root_cause, suggested_fix,
                    evidence(jsonb), related_commit, stability_score

defects             id, project_id, run_result_id, test_case_id, requirement_id,
                    analysis_id, title, description, severity, status, assignee_id,
                    duplicate_of, external_ref(jsonb), created_by, created_at
defect_comments     id, defect_id, author_id, body, created_at

llm_providers       id, name, kind, config(jsonb), credentials(jsonb, encrypted),
                    data_residency, is_enabled, is_default, health_status,
                    health_checked_at, created_by, created_at
llm_models          id, provider_id, model_id, display_name, tiers[],
                    capabilities(jsonb), price_input, price_output,
                    price_cache_read, max_input_tokens, max_output_tokens,
                    is_enabled
tier_assignments    scope, scope_id, tier, model_id, fallback_model_id, effort
                    -- unique(scope, scope_id, tier)
llm_calls           id, project_id, job_id, agent, tier, provider_id, model_id,
                    input_tokens, output_tokens, cache_read_tokens,
                    cache_write_tokens, cost_usd, latency_ms, at

mcp_servers         id, name, transport, command, args[], url,
                    credentials(jsonb, encrypted), scope, scope_id,
                    enabled_tools[], is_enabled, health_status
mcp_calls           id, server_id, tool, arguments(jsonb, redacted), status,
                    agent, job_id, at

notifications       id, user_id, kind, title, body, link, read_at, created_at
```

Two fields carry more weight than they look:

- `artifacts.sha256` and `artifacts.version` are what make FR-9 (maintenance)
  possible.
- `test_cases.requirement_id` and `test_cases.fingerprint` are what make
  traceability and deduplication possible.

Both go in during Phase 0. Retrofitting them later is a rewrite.

---

## 8. Security Requirements

These three items need a decision recorded before the phase that depends on them
is built.

### 8.1 Client code and specs leave the network (blocks Phase 1)

Hyscaler is a services company. Client repositories, API specifications, and
database schemas are likely covered by NDAs and master service agreements.
Sending them to the Anthropic API is a disclosure to a third party.

Before Phase 1 ships, someone must:

1. Review what client contracts say about subprocessors and third-party AI
   processing.
2. Decide whether per-client consent is required, and obtain it where it is.
3. Record which projects are approved for external AI processing. The platform
   must store this as a per-project flag and refuse to run AI jobs on projects
   that are not approved.
4. Ask Anthropic about a zero-data-retention configuration for the account.
   Note that the Claude Fable 5 model specifically requires 30-day retention and
   cannot run under ZDR, which is one reason it is not in the model plan.

If a client refuses external processing, that project needs local model inference
on Hyscaler hardware. That is substantial extra work and should be treated as its
own project, not a checkbox.

### 8.2 The runner executes AI-generated code (blocks Phase 3)

Phase 3 runs code written by a language model, derived from files a user uploaded,
inside Hyscaler infrastructure. This is arbitrary code execution by design, and
plain Docker is not a sufficient boundary.

Required before the runner is built:

- Use gVisor or Firecracker rather than the default `runc` runtime. Make it a
  setting, but default to the hardened option.
- Default-deny outbound network. Only the project's allowlisted hosts and the
  package registries the runner genuinely needs are reachable.
- Block access to the cloud metadata endpoint (`169.254.169.254`) at the network
  layer, not in application code.
- Hard limits on CPU, memory, wall-clock time, process count, and disk writes.
- Run as a non-root user with a read-only root filesystem and a small writable
  tmpfs.
- One-shot containers, destroyed after every run. No reuse, no caching of state.
- Runner hosts live on a network segment with no route to Hyscaler production or
  internal corporate systems.
- Every command the runner executes is logged with the run ID.

### 8.3 Generated tests hit real endpoints (blocks Phases 3, 6, 8)

Security tests (SQL injection, XSS, IDOR, rate-limit probing) and performance
tests (k6 load) are indistinguishable from an attack when pointed at a host you
do not control.

Required:

- Per-project host allowlist, enforced on the server before a run is enqueued,
  not in the browser.
- Resolve the target hostname and reject private, loopback, and link-local
  addresses unless the project explicitly opts in for a local staging target.
- Security and performance test types are disabled by default and must be enabled
  per project by a QA Lead or Admin, with the target confirmed.
- Store the target URL on the `runs` row so every request is attributable after
  the fact.
- Show a confirmation step naming the exact target host before the first
  security or performance run against a new host.

### 8.4 General

- All secrets encrypted at rest with AES-256-GCM.
- Secrets never returned by the API, never written to logs, never included in
  error messages or notification bodies.
- Uploaded files are scanned for size and type before storage. Archive bombs and
  files above a configured size limit are rejected.
- Generated test data must not contain real personal data. Fake payment card
  numbers must come only from published test ranges (Stripe, Visa test PANs) —
  never freshly generated Luhn-valid numbers outside those ranges.
- Audit log for: login, settings change, run trigger, allowlist change, project
  approval flag change, secret rotation.

---

## 9. Acceptance Criteria

The v1 platform is accepted when all of the following hold for one real Hyscaler
project:

0. **Zero-integration install.** With an empty settings table, the five bootstrap
   environment variables, local-disk storage, and one local model provider — and
   with Jira, Slack, SMTP, GitHub, GitLab, Jenkins, S3, and every MCP server
   unconfigured — every criterion below still passes. No screen errors. Every
   unconfigured integration reads "Not configured — using built-in *X*".
1. An admin completes first-run setup entirely through the UI, with only the five
   bootstrap environment variables set.
2. A QA engineer creates a project, uploads a real OpenAPI spec, selects test
   cases and API tests, and submits.
3. The user closes the browser. Within 15 minutes they receive a notification that
   generation is complete.
4. At least 90% of endpoints in the spec have one or more generated test cases.
5. A QA engineer reviews the generated cases and confirms they are usable without
   substantial rewriting.
6. The exported Supertest suite runs and passes against the project's staging API.
7. A run executed inside the platform produces the same pass/fail result as the
   locally-run export.
8. For 10 deliberately introduced failures, the analysis names the correct root
   cause in at least 6.
9. Changing a required field in the spec, re-uploading, and accepting the proposed
   maintenance diff updates exactly the affected test cases and nothing else.
10. No secret appears in any log file, API response, or notification.

---

## 10. Glossary

| Term | Meaning |
|---|---|
| **Artifact** | An uploaded or connected input: spec file, repo, document. |
| **Requirement** | One extracted unit of intent: an endpoint, a validation rule, a business rule. |
| **Test case** | A human-readable case: preconditions, steps, expected result. |
| **Test file** | Generated runnable code covering one or more test cases. |
| **Run** | One execution of a set of test files against a target. |
| **Job** | One unit of background work. Jobs chain: ingest → generate → execute → analyse. |
| **Agent** | One AI role with a defined input, output schema, and model assignment. |
| **Flake** | A test whose result changes across identical re-runs. |
| **Requirement coverage** | Requirements with at least one passing test, over total requirements. |
| **Code coverage** | Line and branch coverage from the language's own coverage tool. |
