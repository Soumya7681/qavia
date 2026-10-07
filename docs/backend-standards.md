# Qavia — Backend Standards

Conventions the backend must follow so it stays maintainable over the life of the
project. These are rules, not suggestions. Where a rule has a reason that is not
obvious, the reason is stated — a rule nobody understands gets broken.

The backend is Go. The AI service is Python. The web app is TypeScript and is covered
by §10 only where it crosses the boundary.

---

## 1. Repository Layout

```
qavia/
├── web/                        Next.js
├── api/                        Go module — owns all persistence
├── services/
│   └── ai/                     Python FastAPI — stateless, no DB
├── infra/
│   └── docker/                 runner images, compose for local dev
└── docs/
```

```
api/
├── cmd/
│   ├── api/main.go             HTTP process: config, wiring, router, serve
│   ├── worker/main.go          job process: config, wiring, handler registration
│   ├── rotatekey/              re-encrypts stored secrets under a new key
│   ├── runnercheck/            proves a runner host's isolation, before it runs client work
│   └── adminreset/             resets a password and clears a lockout when nobody can log in
├── openapi/
│   ├── qavia.yaml              the contract, written before the handler
│   └── gen/                    oapi-codegen output, checked in, never hand-edited
├── migrations/                 goose SQL, checked in, reviewed like code
├── queries/                    sqlc source, one file per domain
├── schema.sql                  regenerated from migrations in CI, reviewed
└── internal/
    ├── platform/
    │   ├── config/             the ONLY place os.Getenv is called
    │   ├── logging/            slog handler with redaction
    │   ├── observability/      OpenTelemetry setup
    │   ├── httpx/              middleware: recovery, correlation, auth, roles, scope
    │   └── apierr/             domain error type and HTTP mapping
    ├── store/                  sqlc output and the pgx pool
    ├── capability/             interface registry (F-1.11)
    │   ├── notifier/
    │   ├── defecttracker/
    │   ├── sourceprovider/
    │   ├── objectstore/
    │   ├── browserdriver/
    │   └── runtrigger/
    ├── settings/               registry, scoped resolution, cache, secret handling
    ├── jobs/                   queue client, JobContext, the pipeline definition
    ├── runner/                 Docker driver, limits, egress policy, artifact capture
    ├── llm/                    client for the Python service, accounting persistence
    ├── auth/
    ├── users/
    ├── projects/
    ├── artifacts/
    ├── requirements/
    ├── testcases/
    ├── testfiles/
    ├── runs/
    ├── analyses/
    ├── defects/
    ├── mcp/
    ├── integrations/           optional external adapters only
    ├── notifications/
    └── audit/
```

Everything is under `internal/`. That is deliberate: no other module can import any
of it, so the package boundary is enforced by the compiler rather than by review.

### Package internal shape

Every domain package looks the same. No exceptions.

```
projects/
├── handler.go                  HTTP only, implements its slice of the generated interface
├── service.go                  business logic
├── store.go                    all database access, wraps the generated queries
├── types.go                    domain types and mappers to the generated API types
└── service_test.go
```

A package with more than one concern splits into more than one service type
(`Service` plus `MemberService`), never into a 900-line file.

**Exported surface is the smallest thing that works.** A store type is unexported
unless another file in the same package needs it. If everything in a package is
exported, the package has no boundary.

---

## 2. Layering

**Handler → Service → Store.** Strictly one direction.

| Layer | May do | Must not do |
|---|---|---|
| **Handler** | Take the generated request type, call one service method, map to a response type | Contain any business logic, touch the database, call another package's service |
| **Service** | Business logic, orchestration, transactions, call own store, call other packages' **services** through an interface | Write SQL, call `os.Getenv`, know about HTTP or `http.Request` |
| **Store** | All database access for its package's tables | Contain business logic, call another package's store |

A handler should be short enough to read at a glance:

```go
func (h *Handler) CreateProject(
	ctx context.Context,
	req api.CreateProjectRequestObject,
) (api.CreateProjectResponseObject, error) {
	user := httpx.CurrentUser(ctx)

	project, err := h.projects.Create(ctx, user, toCreateInput(req.Body))
	if err != nil {
		return nil, err
	}
	return api.CreateProject201JSONResponse(toProjectResponse(project)), nil
}
```

If a handler has an `if` that is not an error check, that `if` belongs in the service.

**No `*http.Request` or `http.ResponseWriter` below the handler.** A service must be
callable from a job worker where there is no request. The only value that crosses down
is `context.Context`.

---

## 3. Cross-Package Access

**A package may only touch its own tables.**

Need project data from inside `runs`? Declare the interface you need, in `runs`, and
accept it as a constructor argument:

```go
// runs/service.go
type Projects interface {
	Get(ctx context.Context, id uuid.UUID) (projects.Project, error)
}
```

Never `projects.Store`, never a query against the `projects` table.

Why: the owning service is where invariants live — permission checks, archived-state
handling, cascade rules. Reaching past it into the table means those checks are
silently skipped.

**Interfaces are declared by the consumer, not the provider.** `runs` declares the
two methods it needs, and `*projects.Service` happens to satisfy them. This keeps the
interface small, keeps the dependency pointing the right way, and makes the test
double one struct instead of a mock of a 20-method service.

**Import cycles are a compile error, and that is the feature.** There is no escape
hatch equivalent to `forwardRef`. If A needs B and B needs A, either the shared piece
belongs in a third package, or one direction should be an event through the queue
rather than a call. Resolve the design; the compiler will not let you defer it.

---

## 4. Input and Output Types

- **The OpenAPI specification is written before the handler.** `oapi-codegen`
  generates the request and response types and the validator middleware. Shape
  validation happens before a handler runs, and a handler that does not match the spec
  does not compile.
- **Semantic validation lives in the service.** Structurally valid is not the same as
  permitted. The service checks that the project is not archived, the host is
  allowlisted, the spend ceiling is not reached.
- **Never return a database row directly.** Every response goes through a mapper in
  `types.go`. sqlc row structs stay inside the package. Reason: without a mapper,
  adding a column silently changes the public API, and a sensitive column silently
  leaks.
- **Response types are generated from the spec, so what is omitted is reviewed.**
  Password hashes, credential blobs and encrypted settings values never appear in the
  spec, therefore they cannot appear in a response.
- **Settings values validate against the JSON Schema in the registry entry**, not
  against a hand-written check at the call site. That schema is the same one the UI
  uses (`tech-stack.md` §11).

---

## 5. Errors

One error type carries what the HTTP layer needs, and one mapper translates it.

```go
// internal/platform/apierr
type Error struct {
	Code    string // stable, machine readable, the frontend branches on this
	Status  int
	Message string // actionable, safe to show a user
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

func ProjectNotFound(id uuid.UUID) error {
	return &Error{
		Code:    "project_not_found",
		Status:  http.StatusNotFound,
		Message: "Project not found.",
		cause:   fmt.Errorf("project %s", id),
	}
}

func TargetHostNotAllowed(host string) error {
	return &Error{
		Code:    "target_host_not_allowed",
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("Host %q is not on this project's allowlist. Add it in Settings → Project → Targets.", host),
	}
}
```

Rules:

- **Services return domain errors, never an HTTP status.** A service must be callable
  from a job worker where HTTP is meaningless. The mapper in `httpx` is the only place
  that knows about status codes.
- **Every error carries a stable machine-readable `Code`.** The frontend branches on
  `code`, never on the message string.
- **Error messages are actionable.** `"No model assigned to the reasoning tier.
  Configure it in Settings → AI → Tiers."` rather than `"tier resolution failed"`.
- **Wrap, do not discard.** `fmt.Errorf("load project %s: %w", id, err)`. The chain is
  what makes a 500 diagnosable.
- **Inspect with `errors.Is` and `errors.As`.** Never compare error strings.
- **Never ignore an error.** `_ = f()` requires a comment explaining why the failure
  is genuinely irrelevant. `errcheck` runs in CI.
- **An unmapped error is a 500 with a generated incident ID**, logged with the full
  wrapped chain, and returned to the client as the ID and nothing else.
- **No panic across a package boundary.** The recovery middleware exists so one bad
  request does not kill the process; it is a backstop, not control flow.
- **Never put a secret, credential, or raw provider response in an error message.**
  It ends up in a log, a notification, and a screenshot.

### Fail early, not inside a worker

Validate everything checkable at enqueue time — provider configured, tier assigned,
target host allowlisted, spend ceiling not reached, project approved for external AI.
A user must learn about a misconfiguration when they press submit, not twenty minutes
later from a failed job.

---

## 6. Configuration Access

**`os.Getenv` is called in exactly one package: `internal/platform/config`.** Anywhere
else is a review failure.

Only the bootstrap variables from `requirements.md` §5.1 exist: `DATABASE_URL`,
`REDIS_URL`, `APP_ENCRYPTION_KEY`, `APP_URL`, plus `PORT` and `APP_ENV`.

**Everything else comes from the settings service.** Adding a new environment
variable is a design error until proven otherwise. The question to answer is "why can
this not be a UI setting?", and the only acceptable answers are the ones already in
§5.1: it is needed before the database is reachable, or it decrypts the settings
themselves.

```go
// correct
timeout, err := s.settings.Duration(ctx, "runner.timeout_seconds",
	settings.Scope{ProjectID: projectID})

// review failure
timeout := 900 * time.Second
if v := os.Getenv("RUNNER_TIMEOUT"); v != "" {
	timeout, _ = time.ParseDuration(v)
}
```

### Settings rules

- Every setting is declared in the registry with one `Declare` call. An undeclared key
  returns an error, never a zero value. A zero value that silently means "off" is the
  failure mode this rule exists to prevent.
- Resolution is always `user → project → global → registry default`. Never
  hand-rolled.
- Secrets are decrypted only at the point of use, never held in a long-lived
  variable, never logged.
- A settings write invalidates the cache across processes. The API and the worker are
  separate binaries, so invalidation goes through Redis, not a local map. A stale read
  after a write is a bug.

---

## 7. Capability Registry

The mechanism behind "every external platform is optional"
(`requirements.md` §5.4).

```go
// internal/capability/notifier/notifier.go
type Notifier interface {
	ID() string
	Available(ctx context.Context) bool
	Send(ctx context.Context, target Target, msg Message) error
}

type Registry struct {
	mu     sync.RWMutex
	byID   map[string]Notifier
}

func (r *Registry) Register(n Notifier)
func (r *Registry) SendToAll(ctx context.Context, u users.User, msg Message) error
```

Rules:

- **The built-in implementation registers unconditionally in `main.go`.** External
  adapters register only when their settings are present and valid. `main.go` is the
  only file that knows which vendors exist.
- **Application code resolves from the registry and never branches on a vendor.**

```go
// correct
if err := s.notifiers.SendToAll(ctx, user, msg); err != nil { ... }

// review failure
if token, _ := s.settings.Secret(ctx, "slack.token"); token != "" {
	return s.slack.Post(ctx, msg)
}
return s.inApp.Create(ctx, msg)
```

- **A failing adapter degrades to the built-in**, records the failure, and alerts an
  admin (FR-10.6). An external outage must never lose a defect or a notification.
- **Adding a vendor means adding one type that satisfies one interface**, plus one
  registration line in `main.go`. If it means touching a handler or a service, the
  abstraction is in the wrong place.

---

## 8. Jobs

Every job type follows the same shape.

```go
type Handler[T any] interface {
	Type() string
	IdempotencyKey(payload T) string
	Handle(ctx context.Context, payload T, jc JobContext) error
}

type JobContext interface {
	Progress(percent int)
	Event(format string, args ...any)
	JobID() uuid.UUID
}
```

Rules:

- **Idempotency is mandatory.** Every handler declares a key, and every write path is
  safe to run twice. Jobs *will* retry, and NFR-4 is not optional. Use
  `ON CONFLICT DO NOTHING` or an existence check inside the transaction, never a
  check-then-write across two statements.
- **Cancellation is `ctx`.** There is no separate `isCancelled` call. Every blocking
  operation takes the context and honours it, and long loops check `ctx.Err()` between
  units of work. A handler that ignores the context cannot be cancelled and will be
  killed instead, mid-write.
- **Never start a goroutine you do not wait for.** Use `errgroup.Group`, and derive
  its context from the job's. A goroutine that outlives its job writes to the database
  after cancellation and after the run is marked complete.
- **Report progress.** A job over roughly ten seconds calls `jc.Progress` and
  `jc.Event`. A user watching a silent bar assumes the system is broken. Progress
  writes to the `jobs` row, which is what the SSE stream reads.
- **No long transactions.** Never hold a transaction open across a model call, an
  HTTP request, or a container run. Do the slow work, then open a short transaction to
  persist. A transaction held across a 90-second provider call exhausts the pool.
- **No unbounded fan-out.** A job that spawns one unit per endpoint uses
  `errgroup.SetLimit` with the configured concurrency, resolved from settings. A
  400-endpoint spec must not launch 400 concurrent provider calls.
- **Chain through the queue, not by calling the next handler directly.** Each stage is
  independently retryable and independently observable. The chain is declared in the
  `jobs/pipeline.go` file, in one place, not spread across handlers.
- **Every job logs with its job ID and correlation ID.** Both travel on the context.
  Tracing one user action across five stages depends on it.
- **Bulk writes use `CopyFrom`.** Persisting 400 generated test cases is one
  statement, not 400 round trips inside a transaction that is now long.

---

## 9. Database

### Migrations

- **All schema changes go through a checked-in goose migration.** No manual SQL
  against any shared environment, ever.
- **Migrations are reviewed as SQL**, read rather than skimmed. There is no generator
  between the author and the statement, which is the point.
- **Migrations are forward-only and additive where possible.** To remove a column:
  stop writing it, deploy, then drop it in a later migration. A drop in the same deploy
  as the code change makes rollback impossible.
- **Every migration has a `-- +goose Down`** that is correct, and CI applies it. A
  down migration that was never run is not a rollback plan.
- **No destructive migration without an explicit written note** in the PR describing
  what data is lost and why that is acceptable.
- **`schema.sql` is regenerated in the same PR.** CI applies every migration to a
  scratch database, dumps the schema, and fails on a diff. That file is the readable
  source of truth a new developer reads top to bottom, and the check is what stops it
  drifting.
- **Migrations are embedded in the binary** with `embed`, so a deploy carries the
  migrations it needs and there is no separate migration image to keep in step.

### Schema conventions

| Rule | Detail |
|---|---|
| Primary keys | `uuid`, generated in the database |
| Timestamps | `timestamptz`, always UTC. Never `timestamp` |
| Every table | `created_at`; `updated_at` where rows are mutable |
| Naming | `snake_case` tables and columns, plural table names |
| Foreign keys | Always declared, with an explicit `ON DELETE` choice |
| Enums | Postgres enum for closed sets, `text` with a check constraint where values may grow |
| Money | `numeric`, never float |
| Indexes | Every foreign key, and every column in a `WHERE` on a hot query |
| Deletes | Soft-delete only where history is a requirement (`archived_at`, `superseded_by`). Everything else hard-deletes. |

### Queries

- **All database access lives in a store.** No `pgxpool` handle in a service, handler
  or job handler. The pool is injected into stores only.
- **Generated queries by default.** Query text lives in `queries/*.sql`, sqlc
  generates the function, and the compiler checks it against the schema.
- **Explicit column lists. `SELECT *` is a review failure.** An added column must not
  silently change the API surface or pull a credential column into memory.
- **Dynamic filtering is the one exception**, built with `squirrel`, inside the store,
  parameterised placeholders only. Never concatenate a value into SQL. Every dynamic
  query has an integration test, because the compiler is not checking that one.
- **Every list endpoint is paginated** with a hard maximum page size, using cursor
  pagination. No unbounded list, even where "it will only ever be a few rows".
- **Multi-write operations run in one transaction** via the generated `WithTx`.
  Writing 400 test cases plus a job status update is one transaction, or idempotency
  breaks.
- **`defer rows.Close()` on every manual query.** `sqlclosecheck` runs in CI and a
  leaked connection under load is not a bug you want to find in production.

### `jsonb` columns

`jsonb` covers settings values, capability matrices, test case steps, evidence
payloads and MCP arguments, which is a large part of this schema.

- **Every `jsonb` column maps to a named Go type**, declared in the owning package and
  wired through the sqlc type overrides. Never `map[string]any` in a domain type, and
  never `json.RawMessage` past the store boundary.
- **Every read unmarshals into that type and every write marshals from it.** Where the
  shape is closed, the decoder uses `DisallowUnknownFields` so a drifted payload is an
  error rather than a silently missing field.
- **A settings value additionally validates against its registry JSON Schema**, since
  its shape depends on the setting rather than the column.

This is the same guarantee the previous plan enforced by rule and review. Here the
generated code produces the typed value directly, so the failure mode is a compile
error rather than a missed review comment.

---

## 10. The Boundaries

### Python AI service

Three rules, restated because breaking any one of them undoes the reason for having
two runtimes at all (`tech-stack.md` §1).

1. **Stateless. No database access.** No connection string, no ORM, no migrations.
   Everything the service needs arrives in the request; everything it produces goes
   back in the response.
2. **The boundary is a versioned OpenAPI contract.** FastAPI publishes the schema, Go
   generates a typed client in CI. A breaking change fails the build.
3. **No business logic.** The service runs agents. What to generate, when, and what to
   do with the result is Go's decision.

Internal structure:

```
services/ai/src/
├── main.py
├── llm/
│   ├── gateway.py       ONLY place providers are constructed
│   ├── providers.py     one adapter per kind; credential decryption
│   ├── caching.py       three strategies from capabilities.prompt_caching
│   ├── accounting.py    usage returned to Go for persistence
│   └── schemas/         Pydantic models, one per schema-locked agent
├── agents/
│   ├── extract.py       single-shot
│   ├── design.py        single-shot
│   ├── dedupe.py        single-shot
│   ├── codegen.py       single-shot
│   ├── analyze.py       LangGraph
│   ├── repo.py          LangGraph
│   └── uiflow.py        LangGraph
└── tools/               local file, grep, git tools for LangGraph agents
```

Rules:

- **An agent never names a provider or model.** It calls `gateway.get_client(tier)`.
  This is what keeps provider switching a settings change forever (`plan.md`
  sequencing rule 5).
- **All LangChain imports stay inside `llm/` and `agents/`.** Bounds the blast radius
  of a version upgrade.
- **Every schema-locked agent validates its response** against its Pydantic model and
  retries with the validation error fed back, up to the configured limit.
  `.with_structured_output()` reduces malformed output; it does not eliminate it.
- **No prompt contains a timestamp, UUID, or per-call ID above the cache boundary.**
  It silently destroys prompt caching. There is a unit test; do not defeat it.
- **Usage is returned, never persisted.** Go writes `llm_calls`.

### Web app

- **The web app has no database credentials and no queue access.** Every read and
  write goes through the API. A Next.js server action that reaches around the API
  cannot exist, which is the point.
- **No hand-written request or response type.** The client is generated from
  `openapi/qavia.yaml`. A field the spec does not describe does not exist.

### Language-specific work runs in the runner

Go cannot parse TypeScript or Python. Anything that needs a language toolchain, most
importantly the F-6.8 static validation that generated files parse, resolve imports
and contain no placeholder assertions, runs inside the runner image that owns that
toolchain and reports a structured result. The API orchestrates it; it does not
perform it.

---

## 11. API Conventions

- **Spec first.** A route exists in `openapi/qavia.yaml` before it exists in Go. A
  handler without a spec entry has nothing to implement.
- **REST, plural nouns, nested where ownership is real:** `/projects/{id}/test-cases`.
- **Versioned prefix from day one:** `/api/v1/...`. Adding versioning later means
  touching every route.
- **Cursor pagination** on every list: `?limit=&cursor=`, response carries
  `nextCursor`. Offset pagination breaks under concurrent writes.
- **Consistent envelope for errors**, `{ code, message, details? }`, produced only by
  the mapper in `httpx`.
- **`Idempotency-Key` header supported** on every mutating endpoint that a CI system
  or webhook can retry.
- **Long operations return `202` with a job ID**, never block. NFR-1 requires a
  response inside 500 ms.
- **Role checks are applied to route groups, never to individual routes.** A new route
  added inside a group inherits the check, so forgetting is not possible. A route
  registered outside a group is a review failure.

```go
r.Route("/projects", func(r chi.Router) {
	r.Use(httpx.RequireRole(role.QAEngineer, role.QALead, role.Admin))
	r.Post("/", h.CreateProject)

	r.Route("/{projectID}", func(r chi.Router) {
		r.Use(httpx.RequireProjectMembership) // authorisation, not a service's memory
		r.Get("/test-cases", h.ListTestCases)
	})
})
```

- **Never re-check a role inside a service.** Authorisation belongs in one layer.
  Business rules that happen to involve a role, such as "only a Lead may approve", are
  a different thing and do belong in the service.

---

## 12. Logging and Tracing

- **Structured JSON via `log/slog`.** No `fmt.Println` and no `log.Printf` in
  committed code.
- **Correlation ID** generated per request in middleware, placed on the
  `context.Context`, propagated into jobs through the payload and to the Python
  service as a header. Every log line carries it, because the logger reads it from the
  context.
- **Log levels mean something.** `error` = needs a human. `warn` = degraded but
  handled. `info` = state transitions. `debug` = development only.
- **Secret redaction is configured in the `slog.Handler`**, not left to call sites.
  Redact by key pattern: `*key*`, `*token*`, `*secret*`, `*password*`, `credentials`,
  `authorization`. NFR-10 is enforced by the logger, not by discipline.
- **Never log a full provider request or response body at `info`.** Prompts contain
  client source code.
- **OpenTelemetry spans** around: HTTP request, job execution, provider call,
  container run, database transaction. "Why did this take 11 minutes" must be
  answerable from a trace.

---

## 13. Security Practices

- **Encrypt secrets at rest** with AES-256-GCM using `APP_ENCRYPTION_KEY`. Store
  ciphertext, nonce and tag. Never plaintext, never reversible encoding.
- **Never return a secret over the API.** Return `{ isSet, updatedAt, hint }`.
- **Runner credentials are scoped and short-lived.** A git token used to clone must
  never enter the runner container; clone in the worker, mount the result read-only.
- **Validate every model-supplied path** before a file operation: `filepath.Clean`,
  resolve with `filepath.EvalSymlinks`, confirm the result is inside the workspace
  root, and reject anything else. Agents read untrusted content and can be induced to
  name any path. A prefix check on an uncleaned path is not a control.
- **Target host allowlist is enforced server-side** before enqueue, and again after
  DNS resolution. Do the second check in a `net.Dialer.Control` hook, which sees the
  resolved address immediately before connection and closes the DNS-rebinding window
  that a resolve-then-dial check leaves open. A browser-side check is not a control.
- **Write-capable MCP tools stay off agents that read untrusted content.** Prompt
  injection turns into action exactly at that intersection.
- **Audit every privileged action:** login, settings change, run trigger, allowlist
  change, project approval change, secret rotation.

---

## 14. Testing Requirements

| Layer | Requirement |
|---|---|
| Service | Unit tested. Business rules, permission logic, and edge cases. Dependencies are the small consumer-declared interfaces from §3, satisfied by a hand-written fake. |
| Store | Integration tested against real Postgres via testcontainers-go. Never a mocked database — settings resolution and idempotency are precisely where mocks lie. |
| Handler | Covered by API integration tests through the real router and the real middleware chain, using `httptest`. Not unit tested separately. |
| Job handler | Integration tested, including **running it twice** to prove idempotency. |
| Capability adapter | Contract test suite that every implementation of the interface must pass, built-in and external alike. |
| Agent | pytest with recorded provider responses. Deterministic, no spend per CI run. |
| Migration | Applied and rolled back in CI against a scratch database, then `schema.sql` regenerated and diffed. |

### Go-specific gates

- **`go test -race ./...`** on every package. The API drives containers, fans out SSE
  and runs bounded worker pools. The race detector is the price of real concurrency and
  it is not optional.
- **`golangci-lint`** with at least `errcheck`, `staticcheck`, `govet`, `bodyclose`
  and `sqlclosecheck`.
- **`go generate ./...` produces no diff.** sqlc and oapi-codegen output is checked in
  and CI regenerates it.

### Required standing tests

- **Prompt prefix stability** — render twice, assert byte-identical prefixes. Guards
  prompt caching permanently.
- **Zero-integration acceptance** — the full suite from `requirements.md` §9 with an
  empty integrations table, including criterion 0.
- **Secret leakage** — assert no response body or log line from the API test suite
  matches a secret pattern.

---

## 15. Definition of Done

A change is not done until all of these hold. Use as the PR checklist.

- [ ] Layering respected: no logic in handlers, no SQL outside stores, no cross-package table access
- [ ] Route added to `openapi/qavia.yaml` first, generated code regenerated and committed
- [ ] No new environment variable, or a written justification against §6
- [ ] Every configurable value read through the settings service, declared in the registry
- [ ] Any new external dependency sits behind a capability interface with a built-in equivalent
- [ ] Response produced by an explicit mapper; no sqlc row type crosses the package boundary
- [ ] Domain errors returned with stable codes and actionable messages; every error wrapped with `%w`, none ignored
- [ ] Schema change is a reviewed goose migration with a working `Down`, and `schema.sql` regenerated
- [ ] Every `jsonb` read unmarshals into a named type; no `map[string]any` in a domain type
- [ ] Any dynamic query uses parameterised placeholders, lives in a store, and has an integration test
- [ ] New job handler declares an idempotency key, honours `ctx` cancellation, and is tested by running twice
- [ ] No goroutine started without a `WaitGroup` or `errgroup` that waits for it
- [ ] No transaction held across a provider call, HTTP request, or container run
- [ ] List endpoints paginated; explicit column lists; no `SELECT *`
- [ ] Correlation ID propagated; no `fmt.Println`; no secret reachable by a logger
- [ ] `go test -race` passes, `golangci-lint` is clean, `go generate` produces no diff
- [ ] Docs updated when behaviour, settings, or features changed — `features.md` for a new feature, `requirements.md` for a new setting

---

## 16. Git and Review

- **Trunk-based** with short-lived branches. Long branches and a multi-phase plan do
  not coexist.
- **Conventional commits:** `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`.
- **One logical change per PR.** A migration plus a feature plus a refactor in one PR
  cannot be reviewed properly and cannot be reverted cleanly.
- **CI gates merge:** lint, `go vet`, unit, integration, race, migration up and down,
  and a clean `go generate`.
- **Every PR is reviewed by the other developer.** With a team of two, an unreviewed
  merge means one person is the only one who understands a subsystem.
- **A PR touching `internal/capability/`, `internal/platform/config/`, `migrations/`,
  `openapi/qavia.yaml`, or `services/ai/src/llm/gateway.py` gets extra scrutiny.**
  Those five are the load-bearing abstractions; a shortcut there costs far more later.
