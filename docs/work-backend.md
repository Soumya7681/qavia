# Qavia Backend Work

Every backend task, in build order. Track A of [work.md](./work.md). This track
runs to completion before any frontend task starts.

**Read first:** [backend-standards.md](./backend-standards.md). Every task assumes
it. Layering, error handling, job rules, and the §15 definition of done are not
repeated per task.

**Conventions used below**

| Field | Meaning |
|---|---|
| Feature | ID from [features.md](./features.md) |
| Requires | Tasks that must be complete first |
| Blocks | Tasks waiting on this one, including `FE-*` from [work-frontend.md](./work-frontend.md) |
| Done when | A statement someone else can verify. Not "code written" |

**Universal step 0 for any task that adds an HTTP route:** write the path,
request, response, and error codes into `api/openapi/qavia.yaml`, run
`make gen`, commit the generated code. A handler with no spec entry has nothing
to implement (`backend-standards.md` §11).

---

# BE-S  Setup

Before phase 0. Nothing product-shaped here, but skipping it costs more later.

### BE-S.1  Repository skeleton

**Requires** nothing. **Blocks** everything.

1. Create the four-directory layout from `tech-stack.md` §11:
   `web/`, `api/`, `services/ai/`, `infra/docker/`, plus the existing `docs/`.
2. `api/` as a Go module with `cmd/api/main.go` and `cmd/worker/main.go` that
   compile and print a version string.
3. `services/ai/` as a `uv` project with `pyproject.toml`, Python pinned to 3.12+.
4. `web/` as a pnpm project, Node LTS pinned in `.nvmrc`.
5. Create the `internal/` tree exactly as listed in `backend-standards.md` §1,
   with a `doc.go` in each package. Empty packages are fine; the shape is the
   point.
6. `.gitignore`, `LICENSE` if required internally, `README.md` pointing at
   `docs/README.md`.

**Done when** `go build ./...`, `uv sync`, and `pnpm install` all succeed from a
clean clone.

### BE-S.2  Root Makefile

**Requires** BE-S.1.

1. `make dev` starts Postgres, Redis, and both Go processes against the compose
   file from BE-S.3.
2. `make gen` runs `sqlc generate`, `oapi-codegen`, `openapi-typescript`, and the
   FastAPI-schema-to-Go-client generation.
3. `make test` runs `go test -race ./...`, `pytest`, and `vitest`.
4. `make lint` runs `golangci-lint`, `ruff`, and `eslint`.
5. `make migrate-up`, `make migrate-down`, `make schema` (regenerates
   `api/schema.sql`).

**Done when** each target runs from a clean clone with no manual steps.

### BE-S.3  Local compose

**Requires** BE-S.1.

1. `infra/docker/compose.yaml` with Postgres 16, Redis 7, and MinIO (MinIO for
   testing the optional driver only, never required).
2. Named volumes so data survives a restart.
3. Health checks on each service so `make dev` waits rather than racing.
4. A `.env.example` with exactly the six bootstrap variables from
   `requirements.md` §5.1 and nothing else. A seventh variable in this file is a
   review failure.

**Done when** `make dev` brings up a working stack and both Go binaries connect.

### BE-S.4  CI pipeline

**Requires** BE-S.2.

1. Jobs: `lint`, `generate-check`, `test-go`, `test-python`, `test-web`,
   `migrate-check`.
2. `generate-check` runs `make gen` and fails on any diff.
3. `migrate-check` applies every migration up, then every `Down`, against a
   scratch database, regenerates `schema.sql`, and fails on a diff.
4. `test-go` runs with `-race`. Not optional (`tech-stack.md` §13).
5. Cache Go modules, pnpm store, and the uv cache.

**Done when** a PR with a deliberate lint error, a stale generated file, or a
broken `Down` migration is rejected by CI.

### BE-S.5  Toolchain pinning

**Requires** BE-S.1.

1. Go toolchain version in `go.mod`. Node in `.nvmrc`. Python in
   `pyproject.toml`.
2. Pin exact versions of `langchain-core`, `langgraph`, and every provider
   package (`tech-stack.md` §15).
3. Configure Renovate or Dependabot, weekly, grouped, CI as the gate.

**Done when** two developers on different machines get byte-identical generated
output from `make gen`.

### BE-S.6  Observability skeleton

**Requires** BE-S.1. **Blocks** BE-0.4.

1. Decide the trace backend for local development (console exporter is fine to
   start).
2. Add the OpenTelemetry collector to compose if a real backend is chosen.

**Done when** a decision is recorded in this file and the exporter is wired in
BE-0.4.

---

# BE-0  Foundation and Settings

No AI in this phase. The point of phase 0 is that every later phase is cheap.
Thirty tasks, and the settings registry plus the capability registry are the two
that everything else leans on.

### BE-0.1  Config package

**Requires** BE-S.1. **Blocks** all.

1. `internal/platform/config` is the only package that calls `os.Getenv`
   (`backend-standards.md` §6).
2. Load and validate the six bootstrap values: `DATABASE_URL`, `REDIS_URL`,
   `APP_ENCRYPTION_KEY`, `APP_URL`, `PORT`, `APP_ENV`.
3. Validate `APP_ENCRYPTION_KEY` decodes from base64 to exactly 32 bytes. Fail at
   boot with a clear message if not.
4. Return an immutable struct. No global mutable state.
5. Add a `golangci-lint` custom rule or a CI grep that fails on `os.Getenv`
   outside this package.

**Done when** starting either binary with a missing or malformed bootstrap value
prints an actionable message and exits non-zero, and the lint rule catches a
planted `os.Getenv` elsewhere.

### BE-0.2  Structured logging with redaction

**Requires** BE-0.1. **Blocks** BE-0.9.

1. `internal/platform/logging` wraps `slog.NewJSONHandler`.
2. Custom handler redacts by key pattern before writing: `*key*`, `*token*`,
   `*secret*`, `*password*`, `credentials`, `authorization`.
3. Redaction walks nested groups and attributes, not just top-level keys.
4. Read the correlation ID from `context.Context` and attach it to every record.
5. Level from a setting once settings exist; default `info`.

**Done when** a unit test logs a struct containing an API key at every nesting
depth and no test assertion finds the plaintext in the output. NFR-10 is enforced
by the handler, not by call-site discipline.

### BE-0.3  Error type and HTTP mapping

**Requires** BE-S.1. **Blocks** BE-0.9, FE error handling.

1. `internal/platform/apierr` with the `Error` type from
   `backend-standards.md` §5: `Code`, `Status`, `Message`, wrapped `cause`.
2. Constructors per domain error, each with a stable `Code`.
3. One mapper in `internal/platform/httpx` producing
   `{ code, message, details? }`.
4. An unmapped error becomes a 500 with a generated incident ID, the full wrapped
   chain logged, and only the ID returned.
5. Start `docs/error-codes.md` as the catalog. Every new code is appended in the
   same PR that introduces it. `BE-X.4` publishes it.

**Done when** a handler returning a wrapped domain error produces the right
status and code, and a handler returning `errors.New("boom")` produces a 500 with
an incident ID and no leaked detail.

### BE-0.4  OpenTelemetry

**Requires** BE-S.6, BE-0.1.

1. `internal/platform/observability` sets up the tracer provider and shutdown.
2. `otelhttp` on inbound and outbound HTTP. `otelpgx` on the pool once BE-0.6
   lands.
3. Manual spans around job execution and, later, container runs.
4. Propagate the trace context into job payloads so a chain is one trace.

**Done when** a request that enqueues a job produces a single trace spanning the
HTTP handler and the worker.

### BE-0.5  Migration harness

**Requires** BE-S.3. **Blocks** every table task.

1. `api/migrations/` with goose, plain SQL, numbered.
2. Embed migrations in both binaries with `embed`.
3. `api/schema.sql` regenerated by `make schema` and checked in.
4. First migration creates the extensions and helper functions needed (for
   example `pgcrypto` for `gen_random_uuid()`).
5. Document the schema conventions table from `backend-standards.md` §9 in the
   migrations README so nobody has to remember it.

**Done when** `make migrate-up`, `make migrate-down`, and `make schema` all work
and CI enforces the schema diff.

### BE-0.6  Database access layer

**Requires** BE-0.5. **Blocks** every store.

1. `sqlc.yaml` configured over pgx v5, with type overrides for `uuid`,
   `timestamptz`, `numeric`, and every `jsonb` column mapped to a named Go type.
2. `internal/store` owns the pool. The pool is injected into stores only, never
   into a service or a handler.
3. Pool settings (max conns, lifetimes) read from settings once BE-0.14 exists;
   sane constants until then.
4. Add `squirrel` and write down the one exception rule: dynamic filtering only,
   inside a store, parameterised placeholders only, integration test mandatory.
5. Set up testcontainers-go helper: one Postgres container per suite, migrations
   applied, truncation between tests.

**Done when** a trivial generated query runs green in an integration test against
a real container.

### BE-0.7  Core tables migration

**Requires** BE-0.5.

Create, in one reviewed migration set:

1. `users`, `sessions`.
2. `projects`, `project_members`.
3. `artifacts` **including `sha256` and `version`** (`requirements.md` §7). These
   ship now, not in phase 11. Retrofitting is a rewrite.
4. `jobs`, `job_events`.
5. `settings`, `settings_audit`.
6. `notifications`.
7. `audit_log`.
8. Indexes on every foreign key and on `(project_id, created_at)` for every list
   query. Explicit `ON DELETE` on every foreign key.

**Done when** `schema.sql` matches, `Down` works, and the traceability columns
exist even though nothing reads them yet.

### BE-0.8  OpenAPI contract bootstrap

**Requires** BE-S.2. **Blocks** every route task, FE-S.3.

1. `api/openapi/qavia.yaml`, OpenAPI 3.1, `/api/v1` prefix from day one.
2. Define shared components first: the error envelope, the cursor pagination
   parameters and `nextCursor` response field, the secret read shape
   `{ isSet, updatedAt, hint }`, and the `202 Accepted` job-reference response.
3. `oapi-codegen` generating the strict server interface plus the `kin-openapi`
   request validator middleware into `api/openapi/gen/`.
4. `openapi-typescript` output path configured for `web/` even though `web/` is
   empty. The generation must already work when `FE-S.3` arrives.
5. Never hand-edit `gen/`. Add a CI check for that.

**Done when** a single `/healthz` path defined in the spec generates a server
interface, and an implementation that does not match fails to compile.

### BE-0.9  Router and middleware chain

**Requires** BE-0.2, BE-0.3, BE-0.8.

1. chi router in `cmd/api/main.go`, wired explicitly. No DI container.
2. Middleware order: recovery, correlation ID, OTel, request logging, request
   validator, session, then route groups.
3. `httpx.RequireRole(...)` and `httpx.RequireProjectMembership` applied to route
   **groups**, never individual routes (`backend-standards.md` §11).
4. `/healthz` (liveness) and `/readyz` (database, Redis, object store reachable).
5. Graceful shutdown: stop accepting, drain, close pool.
6. Add a CI check or review checklist item: a route registered outside a group is
   a failure.

**Done when** an unauthenticated request to a guarded route returns 401 with the
standard envelope, and a wrong-role request returns 403, both without touching a
handler.

### BE-0.10  Authentication and users

**Feature** F-1.1, F-1.3 | **Requires** BE-0.7, BE-0.9 | **Blocks** FE-S.4, FE-0.2

1. Argon2id hashing via `golang.org/x/crypto/argon2`, parameters declared in the
   settings registry (BE-0.13) with safe defaults until then.
2. Server-side sessions with `alexedwards/scs` and the Postgres store. Rotate the
   session ID on privilege change. Immediate revocation by deleting the row.
3. Roles: Admin, QA Lead, QA Engineer, Viewer. Enforced per route group.
4. Admin-invited users only. `POST /users/invite` creates a user with a
   single-use, expiring invite token. **No self-registration endpoint exists.**
5. `POST /auth/login`, `POST /auth/logout`, `GET /me`, `POST /users/{id}/role`,
   `POST /auth/accept-invite`, `POST /auth/change-password`.
6. Login writes an audit row (BE-0.27) on success and on failure.

**Done when** an admin invites a user, that user sets a password and logs in, an
admin deletes the session row, and the next request from that user is 401
immediately.

### BE-0.11  Login rate limiting and lockout

**Feature** F-1.2 | **Requires** BE-0.10.

1. Per-account and per-IP counters over a sliding window.

   **Built on Postgres, not Redis, as originally planned.** `login_attempts` is
   already the durable record an admin inspects, two sources of truth for one
   counter is worse than one, login volume for ten concurrent users does not need
   a second datastore, and it keeps `work.md` §8 item 7 open by not making Redis
   more load-bearing than it has to be.
2. Lockout after a configured number of failures, for a configured duration. Both
   are registry settings.
3. Return the same generic error for wrong password and unknown account. Do not
   leak account existence.
4. Lockout state is visible to an admin and clearable by an admin.
5. Audit every lockout.

**Done when** N failed attempts lock the account, the lock expires on schedule, an
admin can clear it, and the response never distinguishes unknown user from wrong
password.

### BE-0.12  Secret encryption

**Feature** F-1.8 | **Requires** BE-0.1. **Blocks** BE-0.13, BE-1.4.

1. AES-256-GCM with `APP_ENCRYPTION_KEY`. Store ciphertext, nonce, and tag.
2. Decrypt only at the point of use. Never hold plaintext in a long-lived
   variable or a struct field that outlives the call.
3. Produce the read shape `{ isSet, updatedAt, hint }` where the hint shows the
   first four and last four characters only.
4. Provide a key-rotation command that re-encrypts every secret under a new key,
   run offline.

**Done when** a stored secret round-trips, the read path never returns plaintext
in any code path, and rotation re-encrypts every row.

### BE-0.13  Settings registry

**Feature** F-1.5 | **Requires** BE-0.6, BE-0.12 | **Blocks** BE-0.14, BE-0.16, FE-0.4

The single most load-bearing package in the codebase. Build it properly.

1. `internal/settings/registry.go` with one `Declare(...)` call per setting.
   Fields: key, label, help text, type, default, validation as JSON Schema,
   scope (`global` / `project` / `user`), secret flag, restart-required flag,
   minimum role.
2. Declaration happens in `init()` blocks grouped by category file
   (`registry_storage.go`, `registry_runner.go`, `registry_ai.go`, and so on) so
   adding a category is adding a file.
3. Duplicate key registration panics at boot. Better a failed boot than a silent
   override.
4. An **undeclared key returns an error, never a zero value**
   (`backend-standards.md` §6). A zero value that silently means "off" is exactly
   the failure this prevents.
5. Typed accessors: `String`, `Int`, `Bool`, `Duration`, `Secret`, `JSON[T]`.
6. Declare this phase's settings: storage provider and credentials, artifact
   retention days, upload size cap, MIME allowlist, session lifetime, Argon2id
   parameters, login lockout thresholds, log level, job concurrency, SMTP, Slack,
   and the user preference set (timezone, theme, notification channels).

**Done when** a new setting is one `Declare` call with no migration and no new
code path, and reading an undeclared key fails a test.

### BE-0.14  Settings resolution, cache, invalidation

**Feature** F-1.7 | **Requires** BE-0.13.

1. One `settings` table keyed `(scope, scope_id, key)` with a unique constraint.
2. Resolution is always `user → project → global → registry default`. One
   function. Never hand-rolled at a call site.
3. In-memory cache with a short TTL.
4. **Cross-process invalidation.** The API and worker are separate binaries, so a
   write publishes an invalidation over Redis pub/sub (or Postgres
   `LISTEN`/`NOTIFY` if item 7 in `work.md` §8 selects River). A stale read after
   a write is a bug, not a tolerance.
5. Values validate against the registry entry's JSON Schema on write.
6. Restart-required settings are flagged in the response so the UI can badge them.

**Done when** an integration test writes a project-scope value in one process and
the other process reads the new value on its next call, and a value violating its
schema is rejected with an actionable message.

### BE-0.15  Settings audit

**Feature** F-1.9 | **Requires** BE-0.14.

1. Every write to `settings` produces a `settings_audit` row: scope, scope_id,
   key, old value, new value, actor, timestamp.
2. For secrets, both values are recorded as `[redacted]`. Only the fact of the
   change is kept.
3. The audit write happens in the same transaction as the settings write.

**Done when** changing a secret leaves an audit row with no plaintext anywhere in
it, and a rolled-back settings write leaves no audit row.

### BE-0.16  Settings API

**Feature** F-1.6 | **Requires** BE-0.14, BE-0.15 | **Blocks** FE-0.4

1. `GET /api/v1/settings/registry` returns every entry the caller's role may see,
   with validation as JSON Schema. **This is seam item S2.** The UI is rendered
   from it, so it must be complete, not a subset.
2. `GET /api/v1/settings?scope=&scopeId=` returns resolved values with, per key,
   which scope the effective value came from.
3. `PUT /api/v1/settings` writes one or many, validating each and enforcing the
   minimum role per key.
4. Secrets read back as `{ isSet, updatedAt, hint }` only.
5. `DELETE` on a key clears the override so the value falls through to the next
   scope.

**Done when** the registry response alone contains everything a client needs to
render, validate, and permission-check a settings form, with no hardcoded
knowledge of any key.

### BE-0.17  Capability registry

**Feature** F-1.11 | **Requires** BE-0.9 | **Blocks** BE-0.18 to BE-0.21, BE-10.*

The mechanism behind "every external platform is optional". Getting this in now is
what stops `if jiraConfigured` spreading through ten screens later.

1. `internal/capability/` with one sub-package per interface: `notifier`,
   `defecttracker`, `sourceprovider`, `objectstore`, `browserdriver`,
   `runtrigger`.
2. Each interface carries `ID() string` and `Available(ctx) bool` plus its own
   methods.
3. A `Registry` per capability: `Register(impl)`, `Get(id)`, `Active(ctx)`.
4. **Built-in implementations register unconditionally in `main.go`.** External
   adapters register only when their settings are present and valid. `main.go` is
   the only file that knows a vendor name.
5. Write the **contract test suite** now, one per interface. Every implementation,
   built-in and external, must pass it (`backend-standards.md` §14).
6. Degradation helper: a wrapper that catches an adapter error, falls back to the
   built-in, records the failure, and raises an admin notification (FR-10.6).

**Done when** a fake external adapter that always errors is registered, the
degradation wrapper falls back to the built-in, the work completes, and an admin
notification exists.

### BE-0.18  ObjectStore, three drivers

**Feature** F-13.6 | **Requires** BE-0.17, BE-0.14.

1. `ObjectStore` interface: `Put`, `Get`, `Delete`, `SignedURL`, `Stat`.
2. **Local disk driver is the built-in default and requires zero configuration.**
   Root path from settings with a sane default.
3. MinIO and S3 drivers over AWS SDK for Go v2, differing only by endpoint.
4. Driver selected from settings at boot. Marked restart-required.
5. Key convention: `projects/{projectID}/{kind}/{artifactID}/{filename}`. Never
   trust a client-supplied path.
6. Retention job that deletes artifacts older than the configured days.

**Done when** all three drivers pass the same contract test suite, and a fresh
install with an empty settings table stores and retrieves a file on local disk.

### BE-0.19  In-app notifications

**Feature** F-14.1 to F-14.5 | **Requires** BE-0.17, BE-0.7 | **Blocks** FE-0.8

1. Built-in `Notifier` writing to the `notifications` table. **Always available,
   zero configuration.**
2. `GET /notifications`, `POST /notifications/{id}/read`,
   `POST /notifications/read-all`, `GET /notifications/unread-count`.
3. Every notification carries a deep link to the result (FR-8.5).
4. Per-user per-channel toggles resolved through settings.
5. An unconfigured channel is reported as `not_configured`, never as an error, and
   never blocks the in-app notification.
6. SSE or polling for the unread badge. SSE reuses BE-0.24.

**Done when** a job completing produces an in-app notification with a working deep
link on a system where SMTP and Slack are unconfigured, and no error is logged.

### BE-0.20  Built-in RunTrigger

**Feature** F-2.8, F-2.9 | **Requires** BE-0.17, BE-0.22.

1. Manual trigger, the default.
2. Internal scheduler using Asynq's periodic tasks for recurring runs and, later,
   drift checks.
3. Generic inbound webhook: `POST /api/v1/hooks/{token}` with a per-project token,
   HMAC signature verification, and replay protection.
4. All three register as `RunTrigger` implementations.

**Done when** a run can be triggered three ways with no external platform
configured, and a webhook with a bad signature is rejected and audited.

### BE-0.21  Built-in SourceProvider

**Feature** F-3.4 | **Requires** BE-0.17, BE-0.26.

1. Archive upload (zip, tar, tar.gz) as the built-in `SourceProvider`.
2. Safe extraction: reject path traversal entries, symlinks pointing outside the
   root, and archive bombs by uncompressed-size ratio and entry count.
3. Extract into a per-job workspace directory, never a shared one.
4. Clone-by-URL with an optional token lands in `BE-6.1`; the interface shape is
   fixed here so it slots in.

**Done when** a zip containing `../../etc/passwd` and a 10,000:1 compression bomb
are both rejected with clear errors, and a normal archive extracts into an
isolated workspace.

### BE-0.22  Job queue and runner

**Feature** F-2.1, F-2.4, F-2.5, F-2.7 | **Requires** BE-0.7, BE-0.14 | **Blocks** most later phases

**Decide `work.md` §8 open item 7 before starting this task.**

1. Asynq client and server. Queues by priority: `critical`, `default`, `low`.
2. `internal/jobs` with the `Handler[T]` and `JobContext` shapes from
   `backend-standards.md` §8.
3. Every enqueue writes a `jobs` row first; the row is the durable record, Redis
   holds only in-flight coordination.
4. **Idempotency is mandatory.** Every handler declares a key. Every write path is
   safe to run twice: `ON CONFLICT DO NOTHING` or an existence check inside the
   transaction, never check-then-write across statements.
5. Retry with exponential backoff, three attempts (NFR-5), then a terminal failed
   state with the error recorded on the row.
6. Status transitions: `queued`, `running`, `succeeded`, `failed`, `cancelled`.
   Illegal transitions rejected in one place.
7. `jc.Progress(percent)` and `jc.Event(format, args...)` write to `jobs` and
   `job_events`.
8. Cancellation is `ctx`. No separate `isCancelled` call.
9. Concurrency limit read from settings, applied per queue.
10. Correlation ID and trace context travel in the payload.

**Done when** an integration test runs a handler twice with the same idempotency
key and the second run produces no duplicate rows, and killing the worker
mid-handler results in a retry rather than a lost or duplicated job.

### BE-0.23  Pipeline definition and the noop chain

**Feature** F-2.1 | **Requires** BE-0.22.

1. `internal/jobs/pipeline.go` declares every chain in one place. Handlers never
   call the next handler directly; they enqueue the next stage.
2. A parent job row with `parent_job_id` on children so a chain is queryable as a
   unit.
3. Chain-level status: a chain is complete when its last stage completes, failed
   when any stage exhausts retries.
4. A `noop` job type that sleeps in steps and reports progress, chained three
   deep. This is how the pipeline is proven before any real work exists.

**Done when** submitting a `noop` chain shows three stages progressing, and the
chain's completion fires a notification.

### BE-0.24  SSE for job status

**Feature** F-2.2, F-2.3 | **Requires** BE-0.22 | **Blocks** FE-0.7

1. `GET /api/v1/jobs/{id}/events` as `text/event-stream`.
2. Fan-out hub: one Postgres `LISTEN` or Redis subscription per process, many
   browser connections, never one database connection per viewer.
3. Send the current state on connect, then deltas. A late joiner is not left blank.
4. Heartbeat comment every 15 seconds so proxies do not close the stream.
5. `Last-Event-ID` support for resume after a reconnect.
6. Clean teardown on client disconnect. Leaked goroutines here are the classic bug;
   assert with `-race` and a goroutine-count test.

**Done when** two browsers watching the same job both see updates within 5 seconds
(NFR-3), a reconnect resumes without gaps, and closing both leaves no goroutine
behind.

### BE-0.25  Projects and membership

**Feature** F-1.4 | **Requires** BE-0.9, BE-0.10 | **Blocks** FE-0.5

1. `POST /projects`, `GET /projects` (cursor paginated), `GET /projects/{id}`,
   `PATCH /projects/{id}`, `POST /projects/{id}/archive`.
2. `project_members` with a per-project role. `RequireProjectMembership`
   middleware reads it.
3. Archived projects are read-only. Every mutating service call checks
   `archived_at` and returns a domain error, not a 500.
4. Test-type selection at creation (F-3.12). Unimplemented types are returned with
   `available: false` and a reason, so the UI can disable rather than hide them.
5. Project settings write through the settings service at project scope, never a
   column on `projects`.

**Done when** a QA Engineer can create and see only their own projects, a QA Lead
sees all, and a mutating call on an archived project returns a clear domain error.

### BE-0.26  Artifact upload

**Feature** F-3.10, F-3.11 | **Requires** BE-0.18, BE-0.25 | **Blocks** FE-0.6

1. `POST /projects/{id}/artifacts`, multipart, streamed to the object store.
   Never buffer a whole file in memory.
2. Enforce, in this order: size cap from settings, MIME allowlist by sniffed
   content type (not by extension), archive-bomb rejection.
3. Compute SHA-256 **while streaming**, store it on the row.
4. **Identical re-upload does not re-run generation** (FR-1.4). Same
   `(project_id, sha256)` returns the existing artifact with `deduplicated: true`.
5. A changed file for the same logical artifact increments `version` and keeps the
   previous row. This is what phase 11 diffs against.
6. `GET /projects/{id}/artifacts`, `GET /artifacts/{id}`, `DELETE /artifacts/{id}`.

**Done when** uploading the same file twice creates one row and skips
regeneration, uploading a changed file creates version 2 with both rows intact, and
an oversized or wrong-type file is rejected before anything is written.

### BE-0.27  Audit log

**Feature** F-17.1 | **Requires** BE-0.7.

1. `internal/audit` with one `Record(ctx, action, subject, detail)` entry point.
2. Cover, at minimum: login, login failure, settings change, run trigger,
   allowlist change, project approval flag change, secret rotation, user role
   change, integration configured or removed.
3. Detail is a typed `jsonb`, never `map[string]any`, and passes through the same
   redaction as the logger.
4. `GET /audit` with filters, admin only, cursor paginated.

**Done when** every action in the list above produces exactly one audit row, and no
row contains a secret.

### BE-0.28  First-run setup

**Feature** F-1.10 | **Requires** BE-0.10, BE-0.16, BE-0.18 | **Blocks** FE-0.3

1. `GET /setup/status` returns what is still missing: no admin exists, storage
   unreachable, no AI provider configured.
2. `POST /setup/admin` creates the first admin. Available only while zero users
   exist, then permanently 404. Rate limited.
3. Storage reachability check: write and read back a probe object. Setup refuses
   to complete until it passes.
4. Everything except the admin and reachable storage is skippable. AI provider is
   flagged as "required before AI jobs can run", not as a setup blocker.
5. The app refuses to enqueue an AI job without a provider and says so plainly at
   enqueue time, not inside a worker twenty minutes later.

**Done when** a fresh database plus the six environment variables gets to a working
logged-in admin entirely through API calls, with no seed script and no manual SQL.

### BE-0.29  Test infrastructure and standing tests

**Requires** BE-0.6, BE-S.4.

1. testcontainers-go helpers: Postgres and Redis per suite.
2. `httptest` harness that builds the real router with the real middleware chain.
   Handlers are not unit tested separately.
3. A job-handler harness that runs a handler twice to prove idempotency.
4. Standing test: **secret leakage.** Run the whole API suite capturing every
   response body and log line, assert none matches a secret pattern.
5. Standing test: **zero-integration acceptance**, an empty integrations table
   (grows through the phases; `BE-10.8` is the final run).
6. Coverage reporting on the Go module, informational not blocking.

**Done when** all three standing tests run in CI and a deliberately planted secret
in a response fails the build.

### BE-0.30  M1 verification

**Requires** all of BE-0.

Run and record, on a clean environment:

1. Fresh database, six environment variables, nothing else.
2. Admin completes setup through the API.
3. A user creates a project, uploads a file, submits a `noop` chain.
4. The client disconnects entirely.
5. The chain completes and an in-app notification arrives with a working deep link.
6. Kill a worker mid-chain: the job retries, does not duplicate, does not vanish.
7. Change a setting in the API process and confirm the worker picks it up on its
   next job with no restart.

**Done when** all seven pass and the result is recorded in `work.md` §10. This is
**M1**.

---

# BE-1  AI Provider Layer

Still no product features. This phase makes the AI layer swappable from a
dropdown. Read [ai-architecture.md](./ai-architecture.md) §3 in full before
starting.

### BE-1.1  AI tables

**Requires** BE-0.5.

1. Migration for `llm_providers`, `llm_models`, `tier_assignments`, `llm_calls`
   exactly as specified in `ai-architecture.md` §3.2.
2. `credentials` is encrypted `jsonb`; the shape varies by kind and is validated
   per kind in code.
3. `data_residency` enum: `local`, `regional`, `external`.
4. `unique(scope, scope_id, tier)` on `tier_assignments`.
5. Index `llm_calls` on `(project_id, at)` and `(provider_id, at)`; the spend
   dashboard aggregates on both.
6. Add `external_ai_approved boolean not null default false` to `projects`.

**Done when** migration up and down are clean and `schema.sql` matches.

### BE-1.2  Python service skeleton

**Requires** BE-S.1.

1. FastAPI app in `services/ai/src/main.py`, structured per
   `backend-standards.md` §10.
2. **No database driver in the dependency list.** Stateless is enforced by not
   having the capability.
3. `/healthz`, and OpenAPI schema published at `/openapi.json`.
4. Request models are Pydantic. Correlation ID accepted as a header and attached
   to every log line.
5. Structured JSON logging with the same redaction key patterns as Go.

**Done when** the service starts, publishes a schema, and echoes a correlation ID.

### BE-1.3  Go client for the AI service

**Requires** BE-1.2.

1. Generate a typed Go client from the FastAPI schema in `make gen`.
2. `internal/llm` wraps it with timeouts, retries on connection errors only, and
   trace propagation.
3. CI regenerates and fails on a diff, so a Python contract change breaks the Go
   build rather than production.

**Done when** a Python response-model change that breaks the contract fails CI.

### BE-1.4  Provider adapters

**Feature** F-16.1, F-16.2 | **Requires** BE-1.2, BE-0.12.

1. `llm/providers.py`, one adapter per kind: `anthropic`, `bedrock`, `vertex`,
   `openai`, `azure-openai`, `gemini`, `openai-compatible`.
2. Credential shapes per `ai-architecture.md` §3.3, validated per kind.
3. `bedrock` supports `use_instance_role: true` with no stored keys. Offer it as
   the default when running on EC2 or ECS.
4. `openai-compatible` takes base URL, optional key, optional custom headers. This
   one adapter covers Ollama, vLLM, LiteLLM, OpenRouter, Together, Groq,
   Fireworks, DeepSeek.
5. Credentials arrive decrypted from Go in the request. Python never reads the
   database and never holds `APP_ENCRYPTION_KEY`.

**Done when** each adapter constructs a working chat model given valid credentials,
and an invalid credential shape fails with a per-kind actionable message.

### BE-1.5  LLMGateway

**Feature** F-16.3 | **Requires** BE-1.4. **Blocks** every agent.

1. `llm/gateway.py` with `get_client(tier, project_id)`.
2. Resolution: project tier assignment → global tier assignment → **error**.
   Never a silent code default.
3. The error message names the fix: "No model assigned to the reasoning tier.
   Configure it in Settings → AI → Tiers."
4. **This is the only file that calls `init_chat_model` or touches credentials.**
   An agent that imports a provider module is a review failure.
5. Attach the caching strategy (BE-1.6) selected from the model's capabilities.

**Done when** every agent path obtains its client through `get_client(tier)` and a
grep for provider names outside `llm/` returns nothing.

### BE-1.6  Prompt caching strategies

**Feature** F-16.6 | **Requires** BE-1.5.

1. `llm/caching.py` with three strategies selected from
   `capabilities.prompt_caching`:
   - **explicit-breakpoint** (Anthropic direct, Bedrock, Vertex): place
     `cache_control` breakpoints deliberately on the spec prefix block.
   - **passive-prefix** (OpenAI): nothing to set, just keep the prefix stable.
   - **cache-object-lifecycle** (Gemini): create the cache object once, reference
     its handle per call, delete it when the chain ends.
2. `openai-compatible` and local assume no caching; the cost model is linear.
3. The cache handle for Gemini is owned by the job chain, not by a single call.
   Deleting it is part of chain teardown, including on failure.

**Done when** a 40-call fan-out on an explicit-caching provider reports cache-read
tokens on calls 2 through 40, and a Gemini cache object is created once and deleted
at chain end even when the chain fails.

### BE-1.7  Structured output and validation retry

**Feature** F-16.7 | **Requires** BE-1.5.

1. `.with_structured_output(PydanticModel)` as the primary mechanism.
2. **Validate the response against the Pydantic model anyway.** The abstraction
   reduces malformed output; it does not eliminate it, especially on
   `prompt_only` providers.
3. On validation failure, retry with the validation error fed back into the
   prompt, up to a configured limit.
4. A hard failure after retries is a job failure with a readable reason, never a
   partially written result.
5. `structured_output: "prompt_only"` adds a stricter JSON-repair pass before
   validation.

**Done when** a provider forced to return malformed JSON is repaired within the
retry limit in a test, and exceeding the limit fails the job cleanly with nothing
persisted.

### BE-1.8  Accounting and spend ceiling

**Feature** F-16.9, F-16.10, F-16.11 | **Requires** BE-1.1, BE-1.5.

1. Python **returns** usage. **Go writes `llm_calls`.** Python never persists.
2. Every row records provider, model, tier, agent, input tokens, output tokens,
   cache-read tokens, cache-write tokens, computed cost, latency, job ID, project
   ID.
3. Prices live in `llm_models`, editable from the UI. Never hardcoded.
4. Spend ceiling checked **before** the call, per provider and in total. Behaviour
   on reaching it is a setting: block or warn.
5. Ceiling breach at enqueue time gives an actionable error, not a mid-chain
   failure.
6. Do not reuse a token count measured on one model as an estimate for another.
   Label pre-submit numbers as estimates.

**Done when** every AI call in the smoke test has exactly one `llm_calls` row with
a non-zero cost, and setting a ceiling below current spend blocks the next enqueue
with a clear message.

### BE-1.9  Cross-provider fallback

**Feature** F-16.8 | **Requires** BE-1.5, BE-1.8.

1. Retry on **retryable errors only**: rate limit, overload, connection failure.
   Never on a 400.
2. Retry targets the tier's `fallback_model_id`.
3. Record which provider actually served the call so cost attribution stays
   correct.
4. Note in code that a cross-provider retry invalidates the first provider's
   cache; the cost estimate should not assume otherwise.
5. Keep this distinct from same-provider refusal fallback, which is a provider
   feature, not a gateway one.

**Done when** a simulated 429 on the primary provider completes on the fallback and
the `llm_calls` row names the fallback provider.

### BE-1.10  Capability probe

**Feature** F-16.4, F-16.5 | **Requires** BE-1.4 | **Blocks** FE-1.1

1. New providers seed capabilities from a shipped defaults table.
2. `POST /ai/providers/{id}/test` runs a probe confirming chat, tool use,
   structured output, and vision.
3. **Detected capability overwrites declared capability.**
4. Store `health_status` and `health_checked_at`.
5. `tool_use: false` disqualifies a model from the agents that need tools.
   `vision: false` disqualifies it from the `vision` tier. The API returns the
   reason so the UI can explain rather than silently hide.

**Done when** adding a model with wrong declared capabilities and running the probe
corrects them, and an unusable model reports why.

### BE-1.11  Per-project pinning and residency enforcement

**Feature** F-16.12, F-16.13 | **Requires** BE-1.1, BE-1.5. **Gate G1 relevance.**

1. Project setting: AI provider, inherit global or pin to a specific provider.
2. `external_ai_approved` flag on the project, admin-set, audited.
3. **A project without the flag may only be assigned providers marked
   `data_residency: local`.** Enforced server-side before any AI job is enqueued,
   not as a UI hint.
4. The assigned provider is recorded on every generated artifact so output quality
   traces back to what produced it.
5. Attempting to assign an `external` provider to an unapproved project is a 403
   with a specific code.

**Done when** an unapproved project cannot be assigned an external provider through
any API path, and the rejection appears in the audit log.

### BE-1.12  Prefix stability test

**Requires** BE-1.6.

1. Unit test: render the same prompt twice, assert the prefixes are
   **byte-identical**.
2. Add a lint or test that fails when a timestamp, UUID, or per-call ID appears
   above the cache boundary in any prompt template.
3. Document in `services/ai/README.md` why this test exists, so nobody deletes it
   to make a change pass.

**Done when** inserting `datetime.now()` into a prompt template fails CI.

### BE-1.13  AI settings API

**Requires** BE-1.1, BE-0.16 | **Blocks** FE-1.1 to FE-1.4

1. CRUD for `/ai/providers`, `/ai/models`, `/ai/tiers`, `/ai/budget`.
2. Credentials write-only, read back as `{ isSet, updatedAt, hint }`.
3. Tier assignment validates that the chosen model actually declares that tier and
   has the needed capabilities.
4. Deleting a provider that a tier assignment references is refused with an
   actionable error naming the assignment.
5. All of it admin-only, applied at the route group.

**Done when** the four screens can be driven entirely from these endpoints with no
extra knowledge, and every destructive action is refused with a clear reason rather
than cascading.

### BE-1.14  Spend aggregation

**Feature** F-12.7 | **Requires** BE-1.8 | **Blocks** FE-1.5

1. `GET /ai/spend` with grouping by provider, project, and agent, and a date range.
2. These are genuine SQL aggregates, generated by sqlc like any other query.
3. Cursor-paginate the detail list; the summary is bounded by grouping.

**Done when** the numbers reconcile exactly against a direct `SUM` over
`llm_calls`.

### BE-1.15  Smoke-test job and phase verification

**Requires** all of BE-1.

1. An `ai_smoke` job type that asks the `cheap` tier for a fixed structured
   response and validates it.
2. Run it against two providers of different kinds.
3. Switch the `reasoning` tier from one provider to another and confirm the next
   job uses the new one, with no deploy.
4. Confirm a project with `external_ai_approved = false` is refused an external
   provider.

**Done when** all four hold and are recorded in `work.md` §10.

---

# BE-2  Ingest and Test Case Generation

**Gate G1 must be recorded before this phase starts.** This is the phase that
decides whether the product works.

Input scope is deliberately narrow: **OpenAPI and Swagger only**, Postman if time
allows. No PDFs, no repositories, no database schemas.

### BE-2.1  Requirements and test case tables

**Requires** BE-0.5. **Gate G1.**

1. `requirements`: `project_id`, `artifact_id`, `source_ref`, `kind`, `title`,
   `body`.
2. `test_cases`: `project_id`, `requirement_id`, `title`, `preconditions`,
   `steps` (`jsonb`, named Go type), `expected`, `priority`, `category`, `status`,
   **`fingerprint`**, `created_at`, **`superseded_by`**.
3. Unique index on `(project_id, fingerprint)` where `superseded_by is null`. This
   is the deduplication guarantee, enforced by the database rather than by code.
4. Index `requirements(project_id, artifact_id)` and
   `test_cases(project_id, requirement_id)`.

**Done when** the schema supports "which requirements have zero test cases" as one
indexed query.

### BE-2.2  OpenAPI parsing

**Feature** F-3.1, F-4.2 | **Requires** BE-0.26.

**Deterministic code, never a model** (`ai-architecture.md` §2).

1. `pb33f/libopenapi`. Support 3.0 and 3.1.
2. Resolve `$ref` including circular references. Validate and report errors with
   line and column.
3. Normalize into an internal endpoint model: method, path, parameters, request
   schema, response schemas, security scheme, description.
4. **Keep the source position** for every normalized item. This is what makes
   `requirements.source_ref` traceable back to a location in the file.
5. Reject a spec that fails validation with a message naming the line, not a
   generic parse error.

**Done when** a real 40-endpoint client spec normalizes fully, and every produced
requirement can be traced back to its line in the original file.

### BE-2.3  Postman collection import

**Feature** F-3.2 | **Requires** BE-2.2.

1. Decode the subset of the collection format needed into structs. No library
   exists worth depending on; this was never going to be more than that.
2. Map to the same internal endpoint model as OpenAPI so everything downstream is
   format-agnostic.
3. Variables and environment substitution handled explicitly, not left as raw
   `{{placeholder}}` strings.

**Done when** a Postman collection and an equivalent OpenAPI spec produce
comparable normalized endpoint models.

### BE-2.4  Requirement text input

**Feature** F-3.3 | **Requires** BE-2.1.

1. Pasted text stored as an artifact with a SHA-256 like any file.
2. Feeds the extract agent directly, with no structural parse step.

**Done when** pasted text produces requirements with a `source_ref` pointing at a
character range.

### BE-2.5  Ingest job

**Requires** BE-2.2, BE-0.23.

1. Job type `ingest`: fetch the artifact, parse, normalize, persist the endpoint
   model, chain to `extract`.
2. Idempotency key from `(artifact_id, sha256)`.
3. Progress reported per endpoint parsed.
4. A parse failure is a clean job failure with the line number surfaced to the
   user, not a retry loop.

**Done when** re-running `ingest` on the same artifact produces no duplicate rows
and completes in seconds.

### BE-2.6  Extract agent

**Feature** F-4.1, F-4.2 | **Requires** BE-1.5, BE-2.5 | **Blocks** FE-2.1

1. `services/ai/src/agents/extract.py`, **single-shot**, `reasoning` tier,
   Pydantic-locked. Not LangGraph; one call in, one validated object out.
2. Input is the **parsed and normalized** model, never the raw file. The model is
   never asked to read JSON structure.
3. Output: features, business rules, validation rules, auth model, edge cases.
   Every item carries a `source_ref`.
4. Go persists the result as `requirements` rows in one transaction, using
   `CopyFrom` for bulk.
5. Project setting "review extraction before generating" (default off) gates the
   chain: when on, the chain pauses and notifies rather than proceeding.

**Done when** a 40-endpoint spec yields requirements that each trace to a source
location, and the review gate genuinely pauses the chain.

### BE-2.7  Design agent and bounded fan-out

**Feature** F-5.1, F-5.2 | **Requires** BE-2.6, BE-1.6.

1. `agents/design.py`, single-shot, `reasoning` tier, Pydantic-locked.
2. One call **per requirement**, sharing the spec as a cached prefix. This is the
   whole cost story; BE-1.6 is what makes it affordable.
3. Coverage per requirement: happy path, each validation rule, each auth and
   authorisation state, boundary values, negatives.
4. **Bounded fan-out.** `errgroup.SetLimit` with the concurrency from settings. A
   400-endpoint spec must not launch 400 concurrent provider calls.
5. No transaction held across a provider call. Do the calls, then open a short
   transaction to persist.
6. Partial failure policy: a failed requirement is recorded and retried
   independently. One bad requirement does not fail the chain.

**Done when** a 40-endpoint spec completes in under 15 minutes (NFR-2) with at most
the configured number of concurrent calls, and one deliberately failing requirement
does not lose the other 39.

### BE-2.8  Fingerprint and dedupe

**Feature** F-5.3, F-5.4 | **Requires** BE-2.7.

1. **Deterministic fingerprint first:** hash of normalized
   `(method, path, assertion-kind)`. Free, exact, instant.
2. The unique index from BE-2.1 rejects exact duplicates at write time.
3. **AI dedupe is the near-miss fallback only**, `cheap` tier: given two candidate
   cases, same or not.
4. Candidate selection for the AI step is narrow (same requirement, or same
   endpoint) so cost stays bounded.
5. Merged cases record which case they superseded.

**Done when** re-running generation on an unchanged spec produces zero new rows,
and two differently-worded cases for the same assertion are merged.

### BE-2.9  Bulk persistence

**Requires** BE-2.7.

1. `CopyFrom` for bulk insert. 400 test cases is one statement, not 400 round
   trips.
2. The write plus the job status update is one transaction, or idempotency breaks.
3. Verify NFR-4 with a run-twice integration test on the real handler.

**Done when** persisting 400 cases takes one statement and running the handler
twice produces 400 rows, not 800.

### BE-2.10  Test case API

**Feature** F-5.5 | **Requires** BE-2.9 | **Blocks** FE-2.2

1. `GET /projects/{id}/test-cases` with filters (requirement, category, priority,
   status), cursor paginated, hard maximum page size.
2. This is one of the few **dynamic queries**: build with `squirrel`, inside the
   store, parameterised placeholders only, with an integration test per filter
   combination.
3. `PATCH /test-cases/{id}` for inline edit. `POST /test-cases` to add manually.
4. `POST /test-cases/bulk-status` for bulk approve and reject, capped batch size,
   one transaction.
5. Status transitions validated in the service: `draft`, `approved`, `rejected`.

**Done when** every filter combination is covered by an integration test and a
50,000-case project lists in constant time.

### BE-2.11  Requirement coverage

**Feature** F-12.2 | **Requires** BE-2.10 | **Blocks** FE-2.4

1. `GET /projects/{id}/coverage/requirements`: requirements with at least one test
   case, and separately with at least one **passing** test case once runs exist.
2. Return the list of uncovered requirements, not just the number.
3. **Never merge this with code coverage.** They are two separate numbers
   (FR-7.2).

**Done when** the number reconciles against a direct query and uncovered
requirements are individually listable.

### BE-2.12  Cost estimate

**Feature** F-5.7 | **Requires** BE-1.6, BE-1.8 | **Blocks** FE-2.5

1. `POST /projects/{id}/estimate` returns the projected cost of the next
   generation run.
2. Computed using the **assigned provider's actual caching behaviour**. The same
   spec can differ by roughly an order of magnitude between an explicit-caching
   provider and one with none, so a provider-agnostic number would be misleading.
3. Label it an estimate, and return the assumptions (endpoint count, calls, cached
   prefix tokens) so it is auditable.

**Done when** the estimate for a known spec on two different providers differs
correctly, and actual spend after the run lands within a documented tolerance.

### BE-2.13  Target configuration

**Feature** F-3.13 | **Requires** BE-0.14.

1. Project settings: target base URL, target host allowlist (derived from the base
   URL by default), auth mode (`none`, `bearer`, `basic`, `oauth2`, `api-key`),
   auth credentials as a secret.
2. **Without a target URL, generation still works** and execution is disabled with
   a stated reason returned by the API, not a disabled button with no explanation.
3. The allowlist edit is audited.

**Done when** a project with no target generates normally and every execution
endpoint returns a specific "no target configured" code.

### BE-2.14  M2 evaluation

**Requires** all of BE-2. **Open items 1 and 4 must be answered.**

1. Ingest a real Hyscaler client spec.
2. Measure: at least 90% of endpoints have one or more test cases.
3. **A working QA engineer reviews the output and confirms it is usable without
   substantial rewriting.** This is the milestone, not the percentage.
4. Re-run on the unchanged spec: zero duplicates.
5. Run the same spec on two providers and record the quality difference.
6. Store the evaluation as a repeatable harness, not a one-off, so prompt changes
   can be re-scored.

**Done when** all five hold. If point 3 fails, **iterate prompts and schemas here.
Do not start BE-3** (`plan.md` sequencing rule 1). This is **M2**.

---

# BE-3  Code Generation and Export

Thin layer over phase 2's output. Small phase, and it stays small only because
phase 2 produced good cases.

### BE-3.1  test_files table

**Requires** BE-0.5.

1. `test_files`: `project_id`, `framework`, `path`, `content`, `test_case_ids`
   (array), `generated_at`.
2. Index on `(project_id, framework)`.
3. `test_case_ids` is what makes F-6.11 traceability work.

**Done when** a file can be queried by the test case it covers, and vice versa.

### BE-3.2  Codegen agent, Supertest

**Feature** F-6.1 | **Requires** BE-2.10, BE-1.5.

1. `agents/codegen.py`, single-shot, `code` tier. Output is code, so not
   schema-locked, but the file envelope (path, framework, covered case IDs) is.
2. One call per endpoint with the spec as a cached prefix, or batched, depending on
   open item 2 in `work.md` §8. Decide before writing the fan-out.
3. Input is **approved** test cases only.
4. Generated suites read their target and credentials from environment variables
   supplied by the runner, never from hardcoded values.

**Done when** the generated suite for one endpoint is readable, runnable, and
covers every approved case for that endpoint.

### BE-3.3  Postman collection export

**Feature** F-6.2 | **Requires** BE-3.2.

1. Emit a valid Postman v2.1 collection from the same approved cases.
2. Variables for base URL and auth, not literals.

**Done when** the collection imports into Postman without warnings and runs.

### BE-3.4  Static validation of generated code

**Feature** F-6.8 | **Requires** BE-3.2, and the runner image from BE-4.3.

**Go cannot parse TypeScript or Python.** This check runs **inside the runner
image that owns the toolchain** and reports a structured result
(`backend-standards.md` §10).

1. Checks: the file parses, imports resolve, and there is no placeholder text
   (`// TODO`, `expect(true)`, empty test bodies).
2. A failure rejects the file and retries generation with the validation error fed
   back.
3. A file that fails twice is surfaced to the user rather than silently dropped.

**Done when** a deliberately planted `expect(true)` is caught and regenerated, and
the check runs in the runner rather than the API process.

### BE-3.5  File browsing API

**Feature** F-6.9 | **Requires** BE-3.1 | **Blocks** FE-3.1

1. `GET /projects/{id}/test-files` returns the tree structure, paginated.
2. `GET /test-files/{id}` returns content plus the covered case IDs.
3. Content is returned as plain text; highlighting is a frontend concern (Shiki,
   server-rendered).

**Done when** a 300-file suite browses without loading every file's content.

### BE-3.6  Export

**Feature** F-6.10 | **Requires** BE-3.1 | **Blocks** FE-3.3

1. `GET /test-files/{id}/download` for one file.
2. `GET /projects/{id}/test-files/export` streams a zip of the whole suite,
   including a README describing how to run it and which environment variables it
   needs.
3. Stream the zip; never build it in memory.

**Done when** the exported zip runs with `npm install && npm test` against a
staging API with only environment variables supplied.

### BE-3.7  Phase verification

**Requires** all of BE-3.

1. The exported Supertest suite runs with `npm test` against the project's staging
   API and passes.
2. A reviewer can trace any generated assertion back to its test case and its
   requirement.

**Done when** both hold and are recorded.

---

# BE-4  Execution Engine

**Gates G2 and G3 must be recorded before this phase starts.** Largest and
riskiest phase. This is a security boundary, not a feature: it executes
model-generated code derived from user-uploaded files.

### BE-4.1  Gate confirmation and runner host

**Requires** G2, G3 recorded. **Open item 5 answered.**

1. Record the chosen runtime, the network policy, and the runner host placement in
   `work.md` §8.
2. Provision the runner host on a network segment with **no route to Hyscaler
   production or corporate systems**.
3. Confirm the segment by testing, from the host, that production is unreachable.

**Done when** the isolation is demonstrated, not assumed.

### BE-4.2  Runner driver

**Feature** F-7.1, F-7.2 | **Requires** BE-4.1.

1. `internal/runner` over `github.com/docker/docker/client`, the first-party SDK.
2. Explicit `HostConfig` per run: runtime, cgroup limits, read-only rootfs, tmpfs,
   network mode.
3. Runtime from settings, defaulting to `gvisor`. `runc` exists for local
   development only and is flagged as unsafe in the setting's help text.
4. **One-shot.** The container is destroyed after every run, no reuse, no state
   caching. Reaping runs in a `defer` and again in a sweeper that catches orphans
   left by a crash.
5. Log streaming with context cancellation.

**Done when** a run creates a container, streams logs, and leaves nothing behind,
including after the worker is killed mid-run.

### BE-4.3  Runner images

**Feature** F-7.13 | **Requires** BE-4.2.

1. `infra/docker/` images, one per framework family: Node (Supertest, Jest,
   Vitest), Playwright, k6. Add pytest when phase 6 needs it.
2. Non-root user baked in. No package manager at runtime where avoidable.
3. **Pinned by digest, not tag.** Digests are settings so an admin updates them
   without a deploy.
4. A build pipeline that produces and records the digest.
5. Each image also carries the static-validation entry point for BE-3.4.

**Done when** changing an image digest in settings changes which image the next run
uses, with no deploy.

### BE-4.4  Egress control

**Feature** F-7.4, F-7.5 | **Requires** BE-4.2. **Gate G3.**

1. **Default deny outbound.** Only the project allowlist plus the registries the
   runner genuinely needs.
2. Implemented at the network layer (a per-run network with firewall rules), not in
   application code.
3. **Block `169.254.169.254`** at the network layer. An application-level block is
   not a control.
4. Test it: a container that curls the metadata endpoint and a non-allowlisted host
   must fail both.

**Done when** the two negative tests pass from inside a real runner container.

### BE-4.5  Resource limits

**Feature** F-7.3, F-7.6 | **Requires** BE-4.2.

1. CPU, memory, process count (pids limit), wall clock, and disk write limits, all
   from settings.
2. Non-root user, read-only root filesystem, small writable tmpfs.
3. Wall clock enforced by the driver, not trusted to the test framework.
4. Exceeding a limit is a clean, attributed failure ("killed: memory limit"), not a
   mystery exit code.

**Done when** a fork bomb, a memory hog, and an infinite loop are each killed and
reported with the correct reason.

### BE-4.6  Command logging

**Feature** F-17.2 | **Requires** BE-4.2.

1. Every command the runner executes is logged with the run ID.
2. Stored durably, queryable by run, redacted through the same handler as
   everything else.

**Done when** any run can be reconstructed from its command log.

### BE-4.7  Target allowlist and SSRF guard

**Feature** F-7.7, F-7.8 | **Requires** BE-2.13. **Gate G3.** **Blocks** FE-4.1

1. Per-project host allowlist enforced **server-side before enqueue**. A
   browser-side check is not a control.
2. Resolve the hostname and reject private, loopback, and link-local addresses
   unless the project explicitly opts in for a local staging target.
3. **Check again after DNS resolution, in a `net.Dialer.Control` hook.** That sees
   the resolved address immediately before connection and closes the DNS-rebinding
   window a resolve-then-dial check leaves open.
4. Store the target URL on the `runs` row for after-the-fact attribution.
5. Rejection is audited and returns `target_host_not_allowed` with the fix in the
   message.

**Done when** a run against a non-allowlisted host is rejected at the API, a
rebinding DNS record is rejected at dial time, and both appear in the audit log.

### BE-4.8  Run tables

**Requires** BE-0.5.

1. `runs`: `project_id`, `target_url`, `trigger`, `status`, `started_at`,
   `finished_at`, `triggered_by`.
2. `run_results`: `run_id`, `test_case_id`, `test_file_id`, `status`,
   `duration_ms`, `attempt`, `log_key`, `screenshot_key`, `video_key`.
3. Status includes `flaky` and `skipped`, not just pass and fail.
4. Index `(run_id, status)` and `(project_id, started_at)`.

**Done when** the results table supports the dashboard's counts as one query.

### BE-4.9  Execute job

**Feature** F-7.10 | **Requires** BE-4.2 to BE-4.8 | **Blocks** FE-4.4

1. Materialize test files into the container workspace. Never mount the host
   workspace writable.
2. Run with a JSON reporter, parse results, map each result to its test case.
3. Upload logs and artifacts to object storage; store keys on the result row.
4. Persist results with `CopyFrom` in one transaction after the run, not
   incrementally inside a long transaction.
5. Idempotency key from `(run_id)`. A retried execute job does not duplicate
   results.

**Done when** running 400 tests produces 400 result rows with artifact links, and
re-running the job produces no duplicates.

### BE-4.10  Concurrency limiting

**Feature** F-2.7, F-7.3 | **Requires** BE-4.9.

1. Max concurrent runs from settings, enforced with a semaphore in the worker, not
   only by queue configuration.
2. A queued run reports "waiting for a runner slot" rather than sitting silent.

**Done when** setting the limit to 2 keeps exactly two containers alive under a
burst of 10 runs.

### BE-4.11  Live log tail

**Feature** F-7.9 | **Requires** BE-4.9, BE-0.24 | **Blocks** FE-4.3

1. `GET /runs/{id}/logs` as SSE, reusing the BE-0.24 hub.
2. Backpressure: a slow client must not block the run or balloon memory. Drop with
   a marker rather than buffering unboundedly.
3. On completion, the stream ends cleanly and the full log remains available from
   object storage.

**Done when** a viewer joining mid-run sees history plus live output, and a stalled
client does not affect the run.

### BE-4.12  Cancel a run

**Feature** F-2.6 | **Requires** BE-4.9.

1. `POST /runs/{id}/cancel` cancels the job context.
2. The driver kills and reaps the container.
3. The run is marked `cancelled`, partial results retained and labelled.

**Done when** cancelling mid-run stops the container within seconds and leaves no
orphan.

### BE-4.13  Flake detection

**Feature** F-7.11 | **Requires** BE-4.9.

1. Re-run failures N times, N from settings, 0 disables.
2. Count result changes across attempts. **Arithmetic, not a model.**
3. A test whose result changes is flagged `flaky`, not reported as failed.
4. Every attempt is stored as its own `run_results` row with the `attempt` number.

**Done when** a deliberately intermittent test is reported flaky rather than failed,
and its attempts are individually inspectable.

### BE-4.14  Run history and trends

**Feature** F-12.6 | **Requires** BE-4.8 | **Blocks** FE-4.6

1. `GET /projects/{id}/runs`, cursor paginated.
2. Trend aggregate: pass rate, duration, flake count over time.
3. Dashboard counts endpoint feeding F-12.1.

**Done when** the dashboard renders from one request per panel and the numbers
reconcile.

### BE-4.15  M3 verification

**Requires** all of BE-4.

1. 400 tests run inside the platform against staging and produce the **same
   pass/fail results as the BE-3 local export**.
2. Containers confirmed gone afterwards.
3. A run against a non-allowlisted host is rejected at the API and visible in the
   audit log.
4. Reaching the metadata endpoint from inside a runner fails.

**Done when** all four hold and are recorded. This is **M3**.

---

# BE-5  Failure Analysis and Defects

Closes the loop. After this phase the platform is genuinely useful daily.

### BE-5.1  analyses table

**Requires** BE-4.8.

1. `analyses`: `run_result_id`, `reason`, `root_cause`, `suggested_fix`,
   `evidence` (`jsonb`, named type), `related_commit`, `stability_score`.
2. Evidence is a structured list of references (log line range, response field
   path, source file and line), not free text.

**Done when** an analysis row can render its evidence as clickable references
without parsing prose.

### BE-5.2  Analyse agent

**Feature** F-9.1 | **Requires** BE-5.1, BE-1.5 | **Blocks** FE-5.1

1. `agents/analyze.py`, **LangGraph**, `reasoning` tier, tool-using. Multi-step and
   can run for minutes, so checkpointing matters: a worker restart resumes.
2. Inputs: the failing test, request and response, stack trace, and when a
   repository is connected, the relevant source file and git blame.
3. Tools are read-only file and blame tools. **No write-capable tool on this agent**
   (it reads untrusted content: logs and response bodies).
4. Output is Pydantic-locked.

**Done when** an analysis is produced for a real failure and a worker restart
mid-analysis resumes rather than restarting.

### BE-5.3  Evidence enforcement

**Feature** F-9.2 | **Requires** BE-5.2.

1. **An analysis with no evidence is rejected and retried.**
2. Evidence references are validated to actually exist: the log line range is
   within the log, the source location is within the file.
3. A fabricated reference fails validation and triggers a retry with the error fed
   back.

**Done when** an analysis citing a nonexistent line number is rejected
automatically.

### BE-5.4  Stability score

**Feature** F-9.4 | **Requires** BE-4.13, BE-5.2.

1. **Never a model self-report.** Permitted signals only: re-run stability across N
   attempts, or overlap between the named file and recent commits.
2. If neither signal is available, **no number is shown**. The field is null and
   the API says why.
3. Document the formula in code and in the API description so nobody later
   substitutes a model number.

**Done when** the score is reproducible from stored data alone, and a case with no
signal returns null rather than a guess.

### BE-5.5  Defect tracker

**Feature** F-9.7, F-9.9 | **Requires** BE-0.17 | **Blocks** FE-5.3

**The built-in `DefectTracker`. Ships here, not in phase 10, because bug tracking
must work before any external tracker exists.**

1. `defects` and `defect_comments` tables per `requirements.md` §7.
2. Statuses: open, acknowledged, in progress, fixed, won't fix, duplicate.
   Assignable. Comment thread.
3. Traceability: run result, test case, requirement, analysis, and artifact links.
4. `GET /defects` with filters by project, severity, status, assignee, cursor
   paginated (another `squirrel` dynamic query with tests).
5. Registered as the built-in implementation of the `DefectTracker` capability
   interface.

**Done when** the full defect lifecycle works with **no external integration
configured at all**, and the capability contract test passes.

### BE-5.6  Promote a failure to a defect

**Feature** F-9.8 | **Requires** BE-5.5, BE-5.2.

1. `POST /run-results/{id}/promote` creates a defect from a failure that has an
   analysis, in one action.
2. Title and description pre-filled from the analysis; artifacts linked, not
   copied.
3. Idempotent: promoting twice returns the existing defect.

**Done when** one API call turns an analysed failure into a fully linked defect.

### BE-5.7  Duplicate linking

**Feature** F-9.10 | **Requires** BE-5.6.

1. A new defect from the **same test case with the same root cause** links to the
   existing open defect via `duplicate_of` rather than creating a second one.
2. Matching is deterministic on the test case plus a normalized root-cause key.
3. Linking is reversible by a user; the system proposes, it does not overrule.

**Done when** the same failure recurring across three runs produces one defect with
three linked occurrences.

### BE-5.8  Feedback capture

**Feature** F-9.6 | **Requires** BE-5.2.

1. Thumbs up or down per analysis, stored with the analysis ID, the user, and the
   prompt version.
2. Exportable, so prompt iteration has data rather than opinion.

**Done when** feedback is queryable by prompt version.

### BE-5.9  Report export

**Feature** F-12.5 | **Requires** BE-4.14, BE-5.1 | **Blocks** FE-5.5

1. `GET /projects/{id}/report?format=html|pdf`, generated in a job for anything
   large, returned as an artifact.
2. Contents: run summary, requirement coverage, failures with analyses, defects.
3. PDF generation runs in the runner image that has the toolchain, not in the API
   process.

**Done when** both formats render the same content and a large report does not
block a request.

### BE-5.10  M4 verification

**Requires** all of BE-5.

1. Introduce 10 deliberate failures of known cause.
2. The analysis names the correct root cause in at least 6, with evidence a
   reviewer agrees supports it.
3. Generate → run → fail → analyse → **file a defect** → fix works end to end on
   one real project, **with no external integration configured at all**.

**Done when** both hold and are recorded. This is **M4**. Phases 0 to 5 now deliver
a usable product.

---

# BE-6  Repository Ingest and Unit Tests

Phases 6 to 9 have no hard dependency on each other and can be reordered by which
internal team needs what first (`plan.md` sequencing rule 3).

### BE-6.1  Repository input

**Feature** F-3.4, F-3.5 | **Requires** BE-0.21 | **Blocks** FE-6.1

1. `repo_connections` table: `project_id`, `provider`, `repo_url`,
   `default_branch`, `credential_ref`.
2. Built-in paths only in this phase: archive upload (already built) and
   **clone-by-URL with an optional token**. OAuth is phase 10 and optional.
3. Clone happens **in the worker**. The token never enters the runner container.
4. Shallow clone by default, with depth as a setting.

**Done when** a real repository is available on worker disk from both an upload and
a URL clone, and no token is present in any container.

### BE-6.2  Workspace management

**Requires** BE-6.1.

1. One workspace directory per job, removed on completion including on failure.
2. **Validate every model-supplied path**: `filepath.Clean`, resolve with
   `filepath.EvalSymlinks`, confirm the result is inside the workspace root, reject
   otherwise. A prefix check on an uncleaned path is not a control.
3. Disk quota per workspace; a runaway clone must not fill the host.

**Done when** an agent asking for `../../../etc/passwd` is refused, and a symlink
escape is refused.

### BE-6.3  Framework detection

**Feature** F-6.7 | **Requires** BE-6.1.

**Deterministic code, not AI.** Read `package.json`, `pyproject.toml`, `pom.xml`,
and lockfiles.

1. Detect test framework, package manager, and language version.
2. Overridable per project from settings.
3. Unknown stack is reported as unknown with the files inspected, not guessed.

**Done when** detection is correct on five real repositories and honest about the
sixth.

### BE-6.4  Repo comprehension agent

**Feature** F-4.4 | **Requires** BE-6.2, BE-1.5.

1. `agents/repo.py`, **LangGraph**, `code` tier, with local file, grep, and glob
   tools.
2. **Not MCP.** The repository is already on worker disk; putting a supervised
   process between a worker and its own filesystem buys nothing
   (`ai-architecture.md` §5.2).
3. Output: a map of controllers, services, and data access, plus uncovered paths
   identified using the project's existing coverage report where one exists.
4. Bounded iterations from `capabilities.max_tool_iterations`.

**Done when** the map is accurate on a real Hyscaler repository and the agent stops
inside its iteration budget.

### BE-6.5  Unit test agent

**Feature** F-6.3 | **Requires** BE-6.4, BE-6.3.

1. `code` tier, generating Jest, Vitest, or pytest against the identified gaps,
   chosen by the detected stack.
2. Generated tests go through the same BE-3.4 static validation, in the image that
   owns the toolchain.

**Done when** generated unit tests for the top uncovered functions run and pass in
the runner.

### BE-6.6  Coverage execution

**Feature** F-7.14 | **Requires** BE-4.9, BE-6.3.

1. Run the project's **own** coverage tool inside the runner. Never a model
   estimate.
2. Parse line and branch coverage from its native report format.
3. Store per file and per project, per run.

**Done when** measured coverage rises after running generated tests, and the number
matches the tool's own report exactly.

### BE-6.7  Coverage API

**Feature** F-12.3 | **Requires** BE-6.6 | **Blocks** FE-6.2

1. `GET /projects/{id}/coverage/code` with per-file detail.
2. Returned **separately from requirement coverage**, never merged (FR-7.2).
3. Absent when no repository is connected, with a reason, rather than zero.

**Done when** the API returns two clearly distinct coverage numbers and never
invents the second one.

---

# BE-7  UI Tests

The second XL phase. The main risk is flake, and the retry and quarantine policy is
built **with** the phase, not after complaints.

### BE-7.1  BrowserDriver and the bundled container

**Feature** F-7.15 | **Requires** BE-0.17, BE-4.3. **Gate G3.**

1. `BrowserDriver` capability interface.
2. **Bundled Playwright container is the built-in implementation. Build this path
   first and completely.** Fully self-contained, no MCP server required.
3. Same isolation rules as every other runner container: gVisor, egress allowlist,
   limits, one-shot.

**Done when** browser control works end to end with an empty MCP settings table.

### BE-7.2  UI flow discovery agent

**Feature** F-8.1, F-8.3 | **Requires** BE-7.1, BE-1.5 | **Blocks** FE-7.1

1. `agents/uiflow.py`, LangGraph, `code` tier, browser tools.
2. **Drives a real browser through the app**, discovering routes, forms, and flows
   by interacting. This beats reading component source, which misses runtime
   behaviour and conditional rendering.
3. Records a flow graph as structured output, reviewable before spec generation.
4. Bounded interaction budget so discovery terminates.

**Done when** the recorded graph matches the real app's navigable structure on a
staging target.

### BE-7.3  Authenticated discovery

**Feature** F-8.2 | **Requires** BE-7.2, BE-2.13.

1. The configured auth mode is used during discovery.
2. Credentials come from project settings, decrypted at point of use, injected as
   environment variables into the container, never written into a generated file.
3. Redact them from every log and from the recorded trace.

**Done when** discovery reaches authenticated routes and no credential appears in a
trace, video, or log.

### BE-7.4  Playwright generation and selector policy

**Feature** F-6.4, F-6.12 | **Requires** BE-7.2.

1. Generate specs from the flow graph, `code` tier.
2. **Selector policy enforced in code, not by prompt:** `data-testid` → role or
   label → text. A positional CSS selector (`nth-child` and relatives) is
   **rejected and regenerated**, not merely discouraged.
3. The validator is a unit-testable function, applied to every generated selector.

**Done when** a spec containing `nth-child` is rejected automatically and the
regenerated version passes.

### BE-7.5  Artifact capture

**Feature** F-7.10 | **Requires** BE-7.1 | **Blocks** FE-7.2

1. Video, trace, and screenshot capture to object storage.
2. Keys stored on `run_results`. Signed URLs for retrieval.
3. Retention respects the artifact retention setting.

**Done when** a failing UI test produces a viewable video and a downloadable trace.

### BE-7.6  Flake quarantine

**Feature** F-7.12 | **Requires** BE-4.13.

1. A UI test flaking more than a threshold is **auto-quarantined** and surfaced for
   review rather than failing every run.
2. Quarantined tests still run and still record results; they just do not fail the
   run.
3. A quarantine has an owner and an age, so it does not become permanent silently.

**Done when** a chronically flaky test stops failing the suite and appears on a
quarantine list with its age.

### BE-7.7  Optional Playwright MCP driver

**Feature** F-7.16 | **Requires** BE-7.1, BE-10.1.

1. A second `BrowserDriver` implementation backed by an external Playwright MCP
   server, for teams that already run one.
2. Bridged into LangChain tools via `langchain-mcp-adapters` so it works across
   providers.
3. **The agent does not know which driver is active.**

**Done when** switching drivers in settings changes nothing about the agent code,
and removing the MCP configuration reverts cleanly to the bundled container.

---

# BE-8  Test Data and Mock Server

Mostly deterministic code. Bulk data generation is deliberately not an AI feature.

### BE-8.1  Schema-driven data generation

**Feature** F-10.1 | **Requires** BE-2.2.

1. `brianvoe/gofakeit`, **seeded from the OpenAPI schema** so a run is
   reproducible.
2. Respect type, format, pattern, enum, and length constraints from the schema.
3. Deterministic given the same seed. Free and instant.

**Done when** 100 valid records generate from a schema and re-generate identically
from the same seed.

### BE-8.2  AI assist for tricky fields only

**Feature** F-10.2 | **Requires** BE-8.1, BE-1.5.

1. `cheap` tier, used **only** where semantics matter: a realistic Indian address, a
   valid GSTIN shape.
2. Field-level opt-in, not a default. The fallback is always the faker.

**Done when** the AI path is used for a handful of fields and the bulk is free.

### BE-8.3  Boundary and invalid sets

**Feature** F-10.3 | **Requires** BE-8.1.

1. Generate empty, oversized, wrong type, and injection-shaped values per field.
2. Derived from the schema constraints, deterministically.

**Done when** 20 deliberately invalid records generate and each names which
constraint it violates.

### BE-8.4  Payment card constraint

**Feature** F-10.4 | **Requires** BE-8.1.

1. **Payment card numbers come only from published test ranges** (Stripe, Visa test
   PANs). A curated constant list.
2. **Never freshly generated Luhn-valid numbers outside those ranges.** Add a test
   asserting every generated PAN is in the allowed set.
3. No real personal data, ever, anywhere in this module.

**Done when** the test fails if anyone adds a generator that produces an
out-of-range Luhn-valid number.

### BE-8.5  Export

**Feature** F-10.5 | **Requires** BE-8.1 | **Blocks** FE-8.1

1. CSV, JSON, and SQL insert output, streamed.
2. SQL output parameterises nothing dangerous and is clearly labelled as test data.

**Done when** all three formats round-trip into a target database or client app.

### BE-8.6  Mock server

**Feature** F-10.6, F-10.7, F-10.8 | **Requires** BE-8.1, BE-4.2 | **Blocks** FE-8.3

1. Generated from the spec: schema-valid responses per endpoint.
2. Fault injection: configurable delay, 500s, timeouts, and a random failure rate.
3. Runs as **its own container per project**, with a live URL.
4. Lifecycle controls: start, stop, status. Same isolation rules as any container.
5. The URL is on the project's allowlist automatically so generated tests can reach
   it.

**Done when** a client app points at the mock and works, and turning on a 30%
failure rate produces roughly that.

### BE-8.7  Database schema input

**Feature** F-3.7, F-3.8 | **Requires** BE-8.1.

1. Built-in: SQL dump upload, parsed locally, used to shape test data.
2. Optional: read-only Postgres MCP server, scoped to one database with a
   **read-only role**, for schema-aware generation against a real database.
3. The optional path adds nothing that the built-in path cannot do without it.

**Done when** data generates from a dump with no MCP server configured.

---

# BE-9  Performance and Security Tests

**Gate G3 applies with full force.** Pointed at a host you do not own, these are
indistinguishable from an attack.

### BE-9.1  k6 generation and execution

**Feature** F-11.1, F-11.2 | **Requires** BE-4.9 | **Blocks** FE-9.1

1. `code` tier generates the k6 script from endpoints plus a load profile chosen in
   the UI (virtual users, ramp, duration).
2. k6 runner container from BE-4.3, same isolation.
3. Parse k6 metrics output; store latency percentiles and throughput per run.

**Done when** a run produces a latency and throughput series that matches k6's own
summary.

### BE-9.2  Security payload library

**Feature** F-11.3 | **Requires** nothing beyond BE-4.

1. A **curated payload library** in code: SQLi, XSS, CSRF, JWT tampering, IDOR,
   rate limit, broken auth.
2. Versioned and reviewed like any other code.
3. **The model does not invent payloads.** This split is deliberate.

**Done when** the library is complete for the seven categories and every payload has
a stated purpose.

### BE-9.3  Security probe selection

**Feature** F-11.3 | **Requires** BE-9.2, BE-1.5.

1. The agent (`code` tier) selects **which endpoints and parameters to target**.
2. It picks from the library; it never generates a payload string.
3. Output is a probe plan, schema-locked, reviewable before execution.

**Done when** the plan names endpoints and payload IDs, never raw payload text.

### BE-9.4  Findings

**Feature** F-11.4 | **Requires** BE-9.3 | **Blocks** FE-9.3

1. Findings recorded with severity, evidence, and reproduction steps.
2. A finding can be promoted to a defect through the same BE-5.6 path.

**Done when** a planted SQL injection is found and reproduces from the recorded
steps.

### BE-9.5  Approval controls

**Feature** F-11.5, F-11.6 | **Requires** BE-4.7. **Gate G3.**

1. Both test types are **disabled by default** and must be enabled per project by a
   QA Lead or Admin.
2. The first security or performance run against a **new host** requires an explicit
   confirmation naming that exact host.
3. Confirmation and enablement are both audited.
4. A run against a host outside the allowlist is rejected **before any traffic is
   sent**.

**Done when** all four hold, verified including the negative case.

---

# BE-10  Optional External Integrations

Everything here is optional and additive. **No feature built in this phase may
become a prerequisite for anything earlier.**

### BE-10.1  MCP infrastructure

**Feature** F-13.7, F-17.3 | **Requires** BE-0.17, BE-1.5.

1. `mcp_servers` and `mcp_calls` tables per `ai-architecture.md` §5.4.
2. Transport `stdio` and `http`. Credentials encrypted. Per-project scoping so
   Client A's Jira server is unreachable from Client B's project.
3. **Deny-all tool allowlist.** Test connection lists the tools; the admin opts into
   specific ones. A server's full tool set is never enabled by default.
4. Bridge MCP into LangChain tools with `langchain-mcp-adapters`, at the LangChain
   layer, not per provider. MCP-backed agents then require
   `capabilities.tool_use: true`.
5. Audit every call: server, tool, arguments with credential-shaped values
   redacted, result status, agent, job, timestamp.

**Done when** a configured server exposes only its allowlisted tools and every call
is logged.

### BE-10.2  Jira adapter

**Feature** F-13.1, F-9.11 | **Requires** BE-5.5, BE-10.1.

1. Registers behind the existing `DefectTracker` interface.
2. Push a defect to Jira with analysis and artifacts attached. Summary written by
   the `cheap` tier.
3. Store the Jira key back on the internal defect. **The internal defect remains the
   source of truth. Jira is a mirror, not a replacement.**
4. Removing the configuration reverts cleanly to the built-in with no orphaned
   records and no broken screens.

**Done when** a defect pushes with correct fields, the key is stored, and deleting
the Jira configuration leaves every defect intact and every screen working.

### BE-10.3  GitHub and GitLab adapter

**Feature** F-13.2, F-3.6 | **Requires** BE-6.1, BE-10.1.

1. OAuth app configured from settings. Registers behind `SourceProvider`.
2. Repository listing, clone, push and pull-request webhooks.
3. **Least privilege:** contents-read unless PR creation is actually enabled, then
   contents-write on named repositories only. Never organisation-wide admin.

**Done when** a connected repository works and the built-in upload path still works
with the connection removed.

### BE-10.4  Slack and SMTP notifiers

**Feature** F-13.3, F-13.5 | **Requires** BE-0.19, BE-10.1.

1. Both register behind `Notifier`, alongside the in-app channel that always runs.
2. Unconfigured is `not_configured`, never an error.
3. A send failure degrades: the in-app notification still lands, the failure is
   recorded, an admin is alerted.

**Done when** breaking the SMTP credentials mid-flight loses no notification.

### BE-10.5  CI triggers

**Feature** F-13.4 | **Requires** BE-0.20, BE-10.1.

1. GitHub Actions and Jenkins register behind `RunTrigger`.
2. A PR triggers a run and receives a result summary as a status check or PR
   comment.
3. `Idempotency-Key` honoured, because a CI system will retry.
4. Webhook signatures verified; replays rejected.

**Done when** a PR to a connected repository triggers a run and gets a result
comment, and a replayed webhook does not run twice.

### BE-10.6  Degradation and alerting

**Feature** FR-10.6 | **Requires** BE-10.2 to BE-10.5.

1. Runtime failure of any adapter degrades to the built-in, records the failure, and
   alerts an admin.
2. **A Jira outage must not lose a defect.**
3. Health status per integration, surfaced in settings.

**Done when** every adapter passes a fault-injection test proving the built-in
takes over.

### BE-10.7  Write confirmation

**Feature** F-13.7 | **Requires** BE-10.1.

1. **Write operations require confirmation** or an explicit per-project auto-write
   setting.
2. **Write-capable MCP tools stay off the agents that read untrusted content.**
   That intersection is exactly where prompt injection turns into action.
3. Enforced in the tool-binding layer, not by prompt instruction.

**Done when** an agent holding a write tool cannot be bound to an untrusted-content
agent, enforced by a test.

### BE-10.8  Zero-integration re-acceptance

**Requires** all of BE-10. **This is the phase gate.**

1. Remove **every** integration's configuration: Jira, Slack, SMTP, GitHub, GitLab,
   Jenkins, S3, every MCP server.
2. Re-run the full acceptance suite from `requirements.md` §9, **including criterion
   0**.
3. Every unconfigured integration reads "Not configured, using built-in X". No
   screen errors.

**Done when** the entire suite passes with an empty integrations table. If it does
not, the capability registry has been bypassed somewhere, and that is the bug to
fix.

---

# BE-11  Maintenance and Drift

The differentiating module. Generating tests once is table stakes; keeping them
correct as the system changes is what teams pay for.

Depends on the traceability fields that shipped in BE-0.7 and BE-2.1.

### BE-11.1  Re-ingest and version compare

**Feature** F-15.1 | **Requires** BE-0.26, BE-2.5.

1. Re-ingesting an artifact compares `sha256`. Identical means no work.
2. Different means a new `version` row, with the previous version retained.

**Done when** two versions of a spec coexist and are individually retrievable.

### BE-11.2  Structural diff

**Feature** F-15.2 | **Requires** BE-11.1, BE-2.2.

**Deterministic code, not AI.** `pb33f/libopenapi` ships `what-changed`, which is
close to what this needs; use it rather than writing a differ.

1. Diff the two parsed trees, not the two files.
2. Output a structured change list with source positions.

**Done when** a one-field change produces a diff naming exactly that field.

### BE-11.3  Change classification

**Feature** F-15.3 | **Requires** BE-11.2.

1. Classify each change: endpoint added, endpoint removed, signature changed,
   semantics changed, no change.
2. Deterministic rules, unit tested per class.
3. Map each change to the affected `test_cases` through `requirement_id`.

**Done when** each class has a passing test and the affected-case mapping is exact.

### BE-11.4  Maintenance agent

**Feature** F-15.4 | **Requires** BE-11.3, BE-1.5.

1. `reasoning` tier, single-shot, schema-locked.
2. Input is the **computed diff**, never the raw files. This is why the module is
   cheap and reliable: only one step is metered.
3. Per affected case, decide: regenerate, mark stale, or delete, with a reason.

**Done when** the proposal is sensible on a real spec change and costs one call per
affected group, not per file.

### BE-11.5  Approval and apply

**Feature** F-15.5 | **Requires** BE-11.4 | **Blocks** FE-11.1

1. **The user approves the proposal before anything is applied.** Nothing mutates on
   detection alone.
2. `GET /projects/{id}/drift` returns the proposal.
   `POST /projects/{id}/drift/{id}/apply` and `/reject`.
3. Apply runs as a job, idempotent, in one transaction per batch.
4. Reject leaves everything untouched and records the decision.

**Done when** accepting regenerates exactly the affected cases and rejecting changes
nothing at all.

### BE-11.6  History and restore

**Feature** F-15.6 | **Requires** BE-11.5.

1. Replaced and deleted cases are retained via `superseded_by`.
2. `POST /test-cases/{id}/restore` brings a wrongly-deleted case back.
3. The fingerprint unique index accounts for superseded rows so a restore does not
   collide.

**Done when** a deleted case is restored successfully and the coverage number
updates accordingly.

### BE-11.7  Scheduled drift check

**Feature** F-15.7 | **Requires** BE-0.20, BE-11.1.

1. The internal scheduler polls connected specs and repositories on a configured
   interval.
2. On drift, notify through the `Notifier` capability with a deep link to the
   proposal.
3. Per-project enable and interval, both settings.

**Done when** changing an upstream spec produces a notification without anyone
pressing anything.

---

# BE-X  Contract Freeze and Handoff

The frontend track starts here. Do not skip these five tasks; they are what make
the sequential order affordable.

### BE-X.1  Contract freeze

**Requires** all backend phases. **Blocks** every `FE-*` task.

1. `api/openapi/qavia.yaml` is complete: every endpoint, every error code, every
   enum, with descriptions and realistic `examples` on every response.
2. Generate the TypeScript client and commit it under `web/`.
3. Tag the spec version. Any change after this point is a versioned change with a
   note, not a silent edit.
4. Walk the full frontend task list in `work-frontend.md` §7 and confirm every task
   has the endpoints it needs. **A missing endpoint becomes a backend task now, not
   a frontend workaround later.**

**Done when** the generated client compiles and every `FE-*` task's endpoints exist
in the spec with examples.

### BE-X.2  Seed data and fixtures

**Requires** BE-X.1.

1. A seed command producing a realistic demo project: users in all four roles, a
   spec, requirements, test cases, generated files, runs with passes, failures and
   flakes, analyses, and defects.
2. Idempotent, re-runnable, and usable against a local stack.

**Done when** `make seed` produces a database that exercises every screen the
frontend will build, including empty and error states.

### BE-X.3  Mock API server

**Requires** BE-X.1.

1. Serve `qavia.yaml` through a mock server (Prism or equivalent) driven by the
   spec examples.
2. Add SSE fixtures for job status and log tail; the mock must fake the streams,
   because two of the hardest frontend screens depend on them.
3. `make mock` runs it. Document how the frontend points at it.

**Done when** the frontend can develop every screen against the mock with no live
backend running.

### BE-X.4  Error code catalog

**Requires** BE-0.3, BE-X.1.

1. Publish `docs/error-codes.md`: every code, the status it maps to, when it
   occurs, and the user-facing message.
2. Confirm every code in the catalog appears in the spec and vice versa; add a CI
   check for the mismatch.

**Done when** the frontend can branch on codes without reading Go source.

### BE-X.5  Handoff review

**Requires** BE-X.1 to BE-X.4.

1. Walk `work-frontend.md` end to end against the spec, the mock, and the seed data.
2. Record any gap as a new `BE-*` task and close it before the frontend starts.
3. Tick the `BE-X` row in `work.md` §10.

**Done when** the frontend track is unblocked with no known gaps.
