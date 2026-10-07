# Qavia — Technology Choices

Every technology in the stack, why it was chosen, what was rejected, and what it
costs us. A choice with no stated trade-off has not been thought about properly.

---

## Summary

| Layer | Choice |
|---|---|
| Language (API, worker, runner) | Go |
| Language (web) | TypeScript |
| Language (AI service) | Python |
| Frontend | Next.js (App Router), React, Tailwind, shadcn/ui |
| Data fetching | TanStack Query |
| Forms | React Hook Form + Zod |
| API server | Go, `net/http` + chi |
| API contract | OpenAPI 3.1, spec-first, `oapi-codegen` server and TypeScript client |
| AI service | Python + FastAPI |
| AI orchestration | LangChain + LangGraph + Pydantic |
| Database | PostgreSQL |
| Query layer | sqlc over pgx v5 |
| Migrations | goose, checked-in SQL |
| Queue | Asynq on Redis |
| Object storage | S3-compatible (local disk, MinIO, or S3) |
| Test execution | Docker with gVisor, driven by the official Docker Go SDK |
| Spec parsing and diffing | `pb33f/libopenapi` |
| Auth | Argon2id, server-side sessions via `scs` |
| Logging | `log/slog`, structured JSON |
| Tracing | OpenTelemetry |
| Testing | `go test`, testify, testcontainers-go, Playwright, pytest |

---

## 1. Why Three Languages

The most questionable decision in the stack, so it goes first. One language would be
simpler: one toolchain, one dependency tree, one CI pipeline. Two things pushed
against that, in different directions.

### Python for the AI service

The platform must support Anthropic, Bedrock, Vertex, OpenAI, Azure OpenAI, Gemini,
and arbitrary OpenAI-compatible endpoints, with normalized structured output across
all of them (`ai-architecture.md` §3). Structured output is the hard part, with four
incompatible mechanisms:

- Anthropic: `output_config.format` with a JSON schema, or tool-based constraint
- OpenAI: `response_format` with `json_schema` and `strict`
- Gemini: `responseSchema` on the generation config
- Local models: frequently none, needing prompt-plus-repair

LangChain normalizes all four behind `.with_structured_output()`, and LangGraph gives
the tool-looping agents checkpointed state machines that survive a worker restart.
Both are Python-first. Writing and maintaining that normalization ourselves across
seven provider kinds is substantial work up front and a permanent source of bugs as
provider APIs drift. No Go equivalent is close.

### Go for the API, the worker, and the runner

Four reasons, in order of weight.

1. **The runner is the largest and riskiest phase, and Go is its native language.**
   Phase 4 is XL and is a security boundary. Docker, containerd, gVisor and
   Firecracker are all written in Go and all publish first-party Go SDKs. Driving
   containers, streaming logs, enforcing resource limits and reaping one-shot
   containers is library work in Go and FFI-shaped glue anywhere else.
2. **Concurrency is explicit and bounded.** Per-endpoint fan-out capped by a
   semaphore (F-2.7), concurrent container runs, SSE fan-out to watching browsers and
   live log tailing are all `errgroup` plus a channel. The concurrency limit becomes a
   line of code rather than a queue configuration trick.
3. **Compile-time-checked SQL.** sqlc generates Go from real SQL validated against the
   real schema. The two documented costs of the previous ORM choice, awkward
   aggregates and untyped JSON columns, stop being costs. See §5.
4. **One static binary per process.** The platform targets a single VM for up to ten
   concurrent users (NFR-9). A scratch container with one binary, a fast start and a
   small memory floor is the right shape for that, and it makes the runner host
   easier to reason about.

### What it costs

Three toolchains, three dependency trees, three CI pipelines, and no compile-time
type sharing anywhere.

The specific loss compared with TypeScript on both sides is **shared types between
the web app and the API**. That was a genuine benefit and it is gone.

### How the cost is contained

Four hard rules.

1. **The OpenAPI specification is the contract, and it is written first.**
   `oapi-codegen` generates the Go server interface from it, and `openapi-typescript`
   generates the web client. Neither side hand-writes a request or response type. A
   breaking change fails the build in both languages. This is stricter than the shared
   package it replaces, because the shared package could be edited without anyone
   regenerating anything.
2. **The Python service is stateless and never touches the database.** Go owns all
   persistence. Python receives everything it needs in the request and returns a
   result. One migration history, one place data invariants live.
3. **The Go to Python boundary is the same mechanism.** FastAPI publishes its schema,
   Go generates a typed client from it in CI.
4. **The Python service holds no business logic.** It runs agents. Deciding what to
   generate, when, and what to do with the result is Go's job.

Revisit the Python split if the Go AI ecosystem reaches parity on provider coverage
and structured output. That is not close today.

---

## 2. Frontend

### Next.js — App Router

**Why.** Server components keep the initial payload small for data-heavy screens
(test case tables, run results, coverage views). File-based routing keeps a
20-screen app navigable. The team already knows it.

**Rejected.** Plain Vite + React SPA — would work, but every list view then needs
client-side pagination and loading orchestration we get free from server components.
Remix — comparable, smaller ecosystem for the component libraries we want.

**Cost.** App Router's server/client boundary is a real learning curve, and the
`"use client"` split causes confusion. Mitigation: one convention, documented in
`backend-standards.md` — data fetching in server components, interactivity in leaf
client components, no data fetching in client components except through TanStack
Query.

**One change from the TypeScript-everywhere plan.** Server actions are no longer used
for mutations. Every mutation goes to the Go API through the generated client, so
there is exactly one write path and one place authorisation is enforced. A server
action that talks to the database directly cannot exist, because Next.js has no
database credentials.

### Tailwind CSS + shadcn/ui

**Why.** shadcn/ui is copied into the repo rather than installed, so components are
ours to modify — important because the settings UI is generated from a registry
(F-1.6) and needs component variants that no library ships by default. Tailwind
keeps styling colocated, which matters when screens are built by two people.

**Rejected.** Material UI or Ant Design — faster initial build, but heavy theming
and a distinct visual identity we would fight. Chakra — good, less momentum.

**Cost.** Verbose class strings. Accept it; use `cn()` and extract repeated
patterns into components rather than `@apply`.

### TanStack Query

**Why.** Job status polling, SSE-driven cache invalidation, optimistic updates on
test case edits. Solves cache invalidation, request dedup, and background refetch
properly rather than by hand. Wraps the generated OpenAPI client.

**Cost.** A second source of truth alongside server components. Rule: server
components for initial render, TanStack Query for anything that changes after load.

### React Hook Form + Zod

**Why.** The settings UI renders forms from the registry (F-1.5). Since the registry
now lives in Go, its entries travel to the browser as JSON Schema and the web
converts them to Zod at runtime. One declaration, still two enforcement points, no
drift. See §10.

### Recharts

**Why.** Coverage trends, k6 latency and throughput charts, spend breakdown.
Composable, declarative, no canvas layer to fight.

**Rejected.** Chart.js — imperative, awkward in React. D3 directly — far more
control than these charts need.

### Shiki for code display

**Why.** Generated test files are read, not edited, in the UI. Shiki renders
server-side to static HTML with real TextMate grammars — accurate highlighting and
no client bundle.

**Rejected.** Monaco — a full editor, hundreds of kilobytes, needed only if we add
in-browser editing. Revisit then.

### React Flow

**Why.** Rendering the UI flow graph from F-8.3 (routes, forms, transitions
discovered by the browser agent). Purpose-built for interactive node graphs.

---

## 3. API Server

### Go with `net/http` and chi

**Why.** The reason is long-term maintainability and fitness for the runner, not
raw throughput.

- **The standard library is the framework.** `net/http`, `context`, `log/slog`,
  `database/sql`, `testing` and `encoding/json` cover most of what a service like
  this needs, and they do not churn. A codebase maintained for years by people who
  did not write it benefits more from a stable base than from a rich one.
- **chi is a router, not a framework.** It is `http.Handler` all the way down, so
  every piece of middleware in the ecosystem composes, including the OpenTelemetry
  instrumentation. Route groups give one place each for the auth middleware, the role
  check and the project-scope check.
- **Explicit wiring instead of dependency injection.** Every dependency is a
  constructor argument assembled in `main.go`. The capability registry (F-1.11) is a
  map of interface implementations populated at boot. There is no container, no
  decorator and no runtime resolution order to reason about. Reading `main.go` tells
  you the entire object graph.
- **Interfaces are defined by the consumer.** A package that needs to send a
  notification declares the one-method interface it needs. This is what keeps the
  capability abstraction honest, and it is a compile-time boundary rather than a
  convention.
- **Two binaries, one module.** `cmd/api` serves HTTP, `cmd/worker` runs jobs. They
  share domain code and are deployed and scaled separately. Under the previous plan
  the worker was a second instance of the same framework process.

**Rejected.** Gin and Echo — both popular and fine, both introduce a custom context
type that spreads through every handler and complicates using stdlib-shaped
middleware. Fiber — fast, but built on `fasthttp` rather than `net/http`, which cuts
it off from the standard middleware and tracing ecosystem for a throughput win we do
not need. Standard-library `ServeMux` alone — Go 1.22 routing is genuinely good
enough for the routes, but chi's groups and middleware chaining are worth one small
dependency. NestJS, the previous choice — enforced module structure and first-class
queue integration, given up because it cannot drive the runner natively and would
have kept the SQL and JSON typing costs described in §5.

**Cost.** More explicit code. There are no decorators, so role checks and validation
are middleware that a reviewer must confirm is present rather than a compile-time
annotation. Mitigation: role and project-scope middleware are applied to route
*groups*, never to individual routes, so a new route inside a group inherits them and
forgetting is not possible. A route defined outside a group is a review failure.

**Cost.** Team ramp. Go is a small language and the ramp is short, but it is not
zero, and idiomatic error handling and interface placement take a few weeks to
settle. Mitigation: `backend-standards.md` is the written convention, and CI runs
`golangci-lint` so style is not a review conversation.

---

## 4. AI Service

### FastAPI

**Why.** Async-native for concurrent provider calls. Pydantic models are already the
contract for every schema-locked agent (`ai-architecture.md` §4.2), and FastAPI
turns those same models into request validation and an OpenAPI schema. One
definition serving three purposes, and the Go client is generated from it.

**Rejected.** Flask — sync by default, no native validation. Django — an ORM and
admin we explicitly do not want, since Python never touches the database.

### LangChain

**Why.** Structured output normalization across providers, and `init_chat_model` as
one construction path for every provider kind. See §1.

**Scoped usage.** LangChain is used for the chat model interface, provider
construction, and structured output. Not for caching (provider-specific, lives in
our gateway), not for memory (Postgres), not for retrievers or vector stores (not
needed).

**Cost.** Fast-moving ecosystem with a history of interface churn. Mitigation: pin
exact versions of `langchain-core`, `langgraph`, and each provider package. Upgrade
deliberately with the agent test suite as the gate. Keep all LangChain imports
inside `llm/` and `agents/` so an upgrade has a bounded blast radius.

### LangGraph

**Why.** Three agents need multi-step tool loops that can run for minutes: failure
analysis, repository comprehension, UI flow discovery. LangGraph gives explicit
state machines with checkpointing, so a worker restart resumes rather than
restarting.

**Not used** for single-shot agents — extraction, test case design, dedupe, code
generation. One call in, one validated object out. A graph there is indirection for
nothing.

### Pydantic

**Why.** One model per schema-locked agent generates the JSON schema sent to the
provider, validates the response, and types the Python code. One definition, three
uses.

---

## 5. Database

### PostgreSQL

**Why.**

- **`jsonb` with indexing.** Settings values, capability matrices, evidence
  payloads, test case steps, and MCP arguments are all naturally semi-structured.
  Postgres indexes and queries them properly.
- **Real transactions.** A job that writes 400 test cases plus a status update
  must be atomic. Idempotency (NFR-4) depends on it.
- **`timestamptz`** so UTC storage and local display (NFR-7) is not hand-rolled.
- Boring and well understood. Every ops question has an answer.

**Rejected.** MySQL — weaker JSON support. MongoDB — the data is highly relational
(test case → requirement → artifact → project), and losing joins and constraints
would cost far more than schema flexibility gains.

**Not using pgvector.** The original vision listed it. There is no retrieval use
case in v1: specs are parsed structurally, repositories are read by tool-using
agents, and dedupe is a fingerprint plus a cheap model call. An unused extension is
a dependency with no payoff. Add it when a concrete retrieval need appears.

### sqlc over pgx v5

**Why.**

- **The SQL is the source of truth and the compiler checks it.** Queries live in
  `.sql` files. sqlc parses them against the migrated schema and generates typed Go
  functions. A column rename that breaks a query fails `go generate` and then the
  build, not a request at runtime.
- **Aggregates are ordinary.** Requirement coverage, coverage trend, and spend
  grouped by provider and project and agent are genuine SQL aggregates. Under the
  previous ORM they were a documented escape hatch with hand-written result types and
  a rule that each one needed its own integration test. Here they are the same as
  every other query, with a generated result struct.
- **`jsonb` columns get real types.** sqlc maps a `jsonb` column to a named Go type
  that we define, so settings values, capability matrices, evidence payloads and MCP
  arguments unmarshal into structs. The previous plan's rule, that every JSON field
  needs a schema and every read must parse rather than cast, is now the default
  behaviour of the generated code rather than a discipline enforced in review.
- **N+1 is not reachable by accident.** There is no lazy relation traversal to
  trigger inside a loop. Fetching related rows means writing the join.
- **pgx underneath** gives native Postgres types, prepared statement caching,
  `COPY` for bulk insert (400 generated test cases in one statement) and
  `LISTEN`/`NOTIFY`, which the settings cache uses for invalidation.

**Two real costs, and how each is handled:**

1. **Dynamic queries do not fit.** sqlc generates one function per static query.
   A list endpoint with four optional filters cannot be expressed as one generated
   query without writing sixteen of them.

   Handling: those few endpoints build SQL with `squirrel` and execute on the same
   pgx pool, inside the owning package's store, never in a service. The rule is
   parameterised placeholders only, never string concatenation of a value, and an
   integration test per dynamic query. This is the mirror image of the previous
   plan's raw-SQL exception, and it is smaller: dynamic filtering is a handful of
   list endpoints, whereas aggregates were spread across all of reporting.

2. **No schema graph and no drift detection out of the box.** A declarative schema
   file that a new developer reads top to bottom is genuinely useful, and goose
   migrations are a chronological list instead.

   Handling: a generated `schema.sql` is checked in, rebuilt by applying all
   migrations to a scratch database in CI, and reviewed as part of any migration PR.
   It gives the readable single source of truth and fails CI if a migration was
   hand-edited out of sync. Atlas can be added later if drift detection against a
   live environment becomes a real need.

**Rejected.** `ent` — the closest runner-up, a code-generated ORM with a real schema
DSL, a type-safe query builder and graph traversal, and it would have solved the
readable-schema cost. Not chosen because it owns the schema and the migrations, which
puts a framework between us and SQL exactly where this system is most interesting,
and because the generated API is large. GORM — reflection-driven, stringly-typed
query surface, silent N+1 through association loading, and the failure modes the
previous plan spent two pages mitigating. `sqlx` — thin and pleasant, but no
codegen, so result structs are hand-maintained and drift. Prisma's Go client —
unmaintained.

### goose for migrations

**Why.** Plain SQL up and down files, numbered, checked in and reviewed as SQL.
Embeddable in the binary with `embed`, so a deploy carries its own migrations and
there is no separate migration image. It does one thing.

**Rejected.** `golang-migrate` — equivalent and fine, marginally more awkward
embedding story. Atlas — more powerful, with declarative schemas and real drift
detection, and a plausible future addition; more concepts than a two-person team
needs to adopt on day one.

---

## 6. Queue

### Asynq on Redis

**Why.** The job pipeline (F2) needs retries with backoff, concurrency limits,
scheduled and periodic jobs, cancellation, and a durable record of what ran. Asynq
has retries with exponential backoff, per-queue and per-worker concurrency, priority
queues, unique tasks by key, a built-in scheduler for recurring work, context
cancellation, and Asynqmon as a web inspector. Redis is already justified for the
settings cache. It is the closest Go analogue to the previous choice, which keeps the
five-bootstrap-variable contract in `requirements.md` §5.1 unchanged.

**Two gaps compared with the previous choice, both with a stated handling:**

1. **No native parent-child flows.** Chaining is explicit: each handler enqueues the
   next stage. Handling: the chain is declared in one `pipeline` package rather than
   scattered across handlers, and the `jobs` table in Postgres is the durable record
   of stage and status. F-2.1 does not change, only where the chain is written.

2. **No built-in progress field.** Handling: progress writes to the `jobs` row and
   streams over SSE, which F-2.2 and F-2.3 require regardless. Storing progress in the
   queue rather than the database was never the right place for it.

**Rejected.** River — a Postgres-backed queue, and technically the better fit on one
important axis: it enqueues inside the same transaction as the write, so "persist 400
test cases and enqueue the execution job" becomes atomic rather than idempotent-by-
discipline, and it would remove Redis and one bootstrap variable entirely. Not chosen
now because Redis also backs the settings cache and session store, so removing it is a
wider change than swapping a queue, and because Asynq maps one-to-one onto a pipeline
already designed around Redis semantics. **Recorded as an open question**: if the
settings cache moves in-process with `LISTEN`/`NOTIFY` invalidation, River becomes the
better choice and NFR-4 gets easier. Machinery — older, heavier, less active.
Temporal — genuinely better for long-running workflows and worth revisiting if the
pipeline grows much more complex; too much operational weight for a two-person
internal tool at this stage.

**Cost.** Redis is a second stateful service to run and back up. Job state is
recoverable: `jobs` rows in Postgres are the durable record, Redis holds only
in-flight coordination. Losing Redis loses in-flight jobs, which retry. It does not
lose data.

---

## 7. Object Storage

### S3-compatible, three drivers

**Why.** One `ObjectStore` interface (F-1.11), three implementations: local disk,
MinIO, S3. Local disk is the built-in default and needs no configuration
(`requirements.md` §5.4), which is what makes a zero-integration install work.
MinIO and S3 use the same AWS SDK for Go v2 client with a different endpoint.

**Cost.** Local disk does not survive a multi-node deployment. Acceptable: the
platform targets a single VM for up to 10 concurrent users (NFR-9), and any
multi-node deployment configures MinIO or S3 anyway.

---

## 8. Test Execution

### Docker + gVisor

**Why.** This is the security boundary. The runner executes model-generated code
derived from user-uploaded files — arbitrary code execution by design.

Docker alone is not a sufficient boundary. gVisor intercepts syscalls in userspace,
so a container escape must first defeat the gVisor kernel. The runtime is a setting
(F-7.1) so it can be changed, but the default is the hardened option, and the plain
`runc` option exists for local development only.

**Why Go matters here.** The runner is driven with `github.com/docker/docker/client`,
the same first-party SDK the Docker CLI itself uses. Container creation with an
explicit `HostConfig` (runtime, cgroup limits, read-only rootfs, tmpfs, network mode),
log streaming, and reaping are direct API calls. Firecracker, if it is ever enabled as
the alternative runtime, has `firecracker-go-sdk` from the same ecosystem. This is the
single largest practical reason the API is written in Go.

**Rejected.** Plain Docker as the production default — insufficient isolation for
this threat model. Firecracker — stronger isolation via real microVMs, and a
legitimate choice; heavier to operate and slower to start, which matters when a run
spins up a container per execution. Keep it as a configurable option for teams that
want it. Kubernetes Jobs — real orchestration, but a control plane to run for a
workload that is a handful of concurrent containers.

**Cost.** gVisor adds syscall overhead, roughly 10–30% on syscall-heavy workloads.
Test execution is I/O bound on network calls, so the practical impact is small.
Worth it.

---

## 9. Spec Parsing and Diffing

### `pb33f/libopenapi`

**Why.** Spec parsing is deterministic code, never a model (`ai-architecture.md` §2),
so the parser is load-bearing. libopenapi handles OpenAPI 3.0 and 3.1 with full `$ref`
resolution, including circular references, and keeps a low-level model with line and
column positions, which is what F-4.2 needs to trace an extracted item back to its
source location.

**The unplanned win.** libopenapi ships a structural diff. F-15.2 requires a
deterministic diff between two spec versions and F-15.3 requires classifying each
change as endpoint added, removed, signature changed or semantics changed. That is
close to what `what-changed` already produces. The two hardest deterministic pieces of
the differentiating module arrive as a library rather than as a build.

**Rejected.** `kin-openapi` — widely used and the basis of the request validator we
use for generated routes, but its diff story is nonexistent and its 3.1 support lags.
Writing our own — this was the plan under Node, using `@apidevtools/swagger-parser`
plus a hand-written diff.

**Postman collections** have no mature Go library. The format is plain JSON and we
decode the subset we need into structs. It was never going to be more than that.

---

## 10. Auth

### Argon2id, server-side sessions via `scs`

**Why.** Local email and password only. Sessions in Postgres, not JWTs. Revocation
must be immediate — an offboarded employee's access ends when the row is deleted,
with no token TTL to wait out. `alexedwards/scs` is a session manager with a Postgres
store, automatic expiry and rotation on privilege change, and it is ordinary
`http.Handler` middleware, so it sits in the chi chain with everything else.

Password storage: Argon2id via `golang.org/x/crypto/argon2`, parameters in the
settings registry. Rate-limit login attempts per account and per IP, and lock an
account after repeated failures.

**SSO is deferred.** Google Workspace SSO is a natural later addition and stays a
contained change: `golang.org/x/oauth2` plus `coreos/go-oidc` behind the same session
middleware, so it is one new login handler and a settings group, with no change to the
role or project-scope middleware and no change to any route.

**Rejected.** JWT-only — no clean revocation, and there is no scaling reason to
avoid a session lookup at this size. Auth0 or Clerk — a hosted dependency for an
internal tool, contradicting the self-contained principle. NextAuth — the web app
must not hold credentials or a database connection; Go owns auth because it owns
every API route. Authboss — a full auth framework, more surface than four roles and
one login form need.

---

## 11. Shared Code and Repository Shape

There is no shared source package. Three languages cannot have one, and pretending
otherwise is how types drift. Everything crosses a boundary as a generated artifact.

### The OpenAPI specification is the shared code

`api/openapi/qavia.yaml` is written first and is the contract between the web app and
the API.

- `oapi-codegen` generates the Go server interface, request and response types, and a
  `kin-openapi` request validator middleware. An endpoint that does not match the spec
  does not compile.
- `openapi-typescript` plus `openapi-fetch` generates the browser client. The web app
  hand-writes no request or response type.
- CI regenerates both and fails on a diff, so an edited spec with stale generated code
  cannot merge.

The same mechanism, in the other direction, produces the Go client for the Python
service from FastAPI's published schema.

### The settings registry moves to Go, and gains something

The registry (F-1.5) is a Go package: one `Declare` call per setting with key, label,
help text, type, default, validation, scope, secret flag, restart-required flag and
minimum role. It is served to the browser at `GET /api/v1/settings/registry`, with
validation expressed as JSON Schema.

Under the previous plan the registry was a TypeScript module imported by both sides at
build time. Serving it at runtime is better in two ways: the UI cannot drift from a
stale build of the registry, and a setting added by a plugin or an admin-visible
capability appears without a frontend deploy. The web converts the JSON Schema to Zod
at runtime for React Hook Form, so client-side validation and server-side validation
still come from one declaration.

### Layout

```
qavia/
├── web/                Next.js, its own pnpm project
├── api/                Go module: cmd/api, cmd/worker, internal/, migrations/, queries/, openapi/
├── services/ai/        Python FastAPI
└── infra/docker/       runner images, compose for local dev
```

**Rejected.** pnpm workspaces plus Turborepo, which was the previous plan. With one
TypeScript project left there is nothing to link and nothing to orchestrate. A root
`Makefile` with `make dev`, `make gen`, `make test` and `make lint` covers what
Turborepo was doing, and each language keeps its own native tooling: `go build`,
`pnpm`, and `uv`.

---

## 12. Observability

### `log/slog`

**Why.** Structured JSON logging in the standard library, so it is one less
dependency and one less thing that churns. A custom `slog.Handler` wraps the JSON
handler and redacts by key pattern (`*key*`, `*token*`, `*secret*`, `*password*`,
`credentials`, `authorization`) before anything is written. NFR-10 is enforced by the
logger, not by discipline at call sites.

`slog` carries attributes on the `context.Context`, which is already threaded through
every Go call, so the correlation ID propagates without an async-context mechanism.

**Rejected.** `zap` and `zerolog` — both faster, and the difference does not matter
for a service whose latency is dominated by model calls and container runs. Standard
library wins on stability.

### OpenTelemetry

**Why.** A single user action spans Next.js, the Go API, the queue, the Python
service, a provider API, and a Docker container. Without distributed tracing, "why did
this take 11 minutes" is unanswerable. Instrument from Phase 0; retrofitting tracing
is far more work than adding it early. `otelhttp` for inbound and outbound HTTP,
`otelpgx` for the database, and manual spans around job execution and container runs.

**Cost.** Setup time and a collector to run. Start with console export in
development and add a backend when someone needs it.

### Sentry — optional

Error aggregation. Optional per the self-contained rule; errors are always written
to the structured log and surfaced in-app regardless.

---

## 13. Testing

| Layer | Tool | Why |
|---|---|---|
| Unit (Go) | `go test` + `testify/require` | Standard library runner, table-driven tests, one assertion helper and nothing else |
| Store and API integration | testcontainers-go | Real Postgres and Redis in a container per suite. No mocked database, because settings resolution and idempotency are exactly where mocks lie |
| HTTP handler | `net/http/httptest` | Real router, real middleware chain, no framework harness |
| Frontend component | Vitest + Testing Library | Behaviour, not implementation |
| End-to-end | Playwright | Already a dependency for the product itself |
| Python | pytest | Standard |
| Agent output | pytest with recorded fixtures | Real provider responses recorded once, replayed in CI. Deterministic, and no spend per test run |

### Go-specific CI gates

- **`go test -race` on every package.** The API runs concurrent container drivers,
  SSE fan-out and bounded worker pools. The race detector is not optional and it is
  the main thing Go gives back in exchange for real concurrency.
- **`golangci-lint`** with `errcheck`, `staticcheck`, `govet`, `sqlclosecheck` and
  `bodyclose`. Unchecked errors and leaked rows or response bodies are the two most
  common Go defects and both are mechanically detectable.
- **`go generate` is clean.** sqlc and oapi-codegen output is checked in, regenerated
  in CI, and a diff fails the build.

### Three non-obvious required tests

**Prompt prefix stability.** Render the same prompt twice, assert the prefixes are
byte-identical. Prompt caching is the difference between an affordable and an
unaffordable fan-out (`ai-architecture.md` §3.6), and a stray timestamp or UUID
above the cache boundary silently destroys it. A unit test makes that invariant
permanent instead of a comment nobody reads.

**Zero-integration acceptance.** The full acceptance suite (`requirements.md` §9)
runs with an empty integrations table. Guards the self-contained guarantee against
slow erosion.

**Generated-code validation runs in a container.** F-6.8 requires that generated test
files parse, resolve their imports and contain no placeholder assertions. Go cannot
parse TypeScript or Python. The check therefore runs inside the runner image that owns
that language toolchain and reports back, rather than in the API process. This was
implicit when the API was a Node process and is explicit now.

---

## 14. Deliberately Not Used

| Technology | Why not |
|---|---|
| pgvector | No retrieval use case in v1. An unused extension is a dependency with no payoff. |
| Kubernetes | A control plane to operate for a handful of concurrent containers on one VM. |
| GraphQL | The API is REST-shaped, consumed by one client we control. GraphQL adds a schema layer and N+1 risk for no gain here. |
| gRPC between Go and Python | Attractive, and the wrong trade here. FastAPI already publishes OpenAPI, the call volume is low and the payloads are large JSON documents, so HTTP plus a generated client costs one dependency instead of a second IDL and a build step. |
| Kafka | Asynq handles the throughput. Kafka is for event streaming we do not have. |
| An ORM | sqlc plus pgx covers it with the compiler checking the SQL. See §5. |
| A DI container (`wire`, `fx`) | The object graph is assembled in `main.go` and fits on a screen. A container hides the one file worth reading. |
| Microservices beyond the three processes | API, worker and AI service is already the cost we justified. A fourth needs its own argument. |
| Elasticsearch | Postgres full-text search covers searching test cases and logs at this scale. |
| LangChain in the Go layer | Provider handling belongs in one place. Two AI abstractions in two languages is exactly the split-brain to avoid. |
| CGO | Every dependency stays pure Go so the binary is statically linked and the runner image can be `scratch` or `distroless`. A CGO dependency needs an explicit decision. |

---

## 15. Version Policy

- **Pin exact versions** for LangChain, LangGraph, and provider SDKs. History of
  interface churn; upgrade deliberately with the agent suite as the gate.
- **Pin runner images by digest**, not tag. A run must be reproducible, and a moving
  tag makes results non-comparable over time. Digests are settings (F-7.13) so an
  admin updates them without a deploy.
- **Go modules pin exactly by default** and `go.sum` is committed. `go mod tidy` runs
  in CI and a diff fails the build.
- **Caret ranges** are fine for the web app, with a lockfile committed.
- **Renovate or Dependabot** weekly, grouped, with CI as the gate.
- **Go toolchain version pinned in `go.mod`**, Node LTS pinned in `.nvmrc`, and
  Python 3.12+ pinned in `pyproject.toml`, so local and CI match.
