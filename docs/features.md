# Qavia — Feature Catalog

Every feature the platform ships, with its module, delivery phase, AI involvement,
and whether it is built-in or an optional external integration.

**Reading the columns**

| Column | Meaning |
|---|---|
| **Kind** | `built-in` = always available, no configuration. `optional` = external platform, off until configured (`requirements.md` §5.4). |
| **AI** | The abstract tier used, or `none`. Tiers map to providers and models in settings (`ai-architecture.md` §3.2). No feature names a model. |
| **Phase** | Delivery phase from `plan.md`. |
| **Req** | Requirement group in `requirements.md`. |

---

## F1 — Platform and Access

Foundation. Everything else assumes this exists.

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-1.1 | Local email + password login, Argon2id hashing, admin-invited users, no self-registration | built-in | none | 0 | §3 |
| F-1.2 | Login rate limiting per account and per IP, lockout after repeated failures | built-in | none | 0 | §8.4 |
| F-1.3 | Four roles: Admin, QA Lead, QA Engineer, Viewer, enforced per route | built-in | none | 0 | §3 |
| F-1.4 | Project create, list, archive, membership | built-in | none | 0 | FR-1 |
| F-1.5 | Settings registry — one declaration per setting drives storage, validation, and UI | built-in | none | 0 | §5 |
| F-1.6 | Settings UI rendered from the registry — new settings need zero UI work | built-in | none | 0 | §5 |
| F-1.7 | Scoped settings resolution: user → project → global → default | built-in | none | 0 | §5.3 |
| F-1.8 | Secret storage, AES-256-GCM. API returns `{isSet, updatedAt, hint}`, never plaintext | built-in | none | 0 | §5.3 |
| F-1.9 | Settings audit trail — who changed what, when, old and new value | built-in | none | 0 | §5.3, §8.4 |
| F-1.10 | First-run setup wizard — only genuinely required settings, rest skippable | built-in | none | 0 | §5.3 |
| F-1.11 | Capability interface registry — `Notifier`, `DefectTracker`, `SourceProvider`, `ObjectStore`, `BrowserDriver`, `RunTrigger` | built-in | none | 0 | §5.4 |

### F-1.5 / F-1.6 — Why the registry matters

Every setting is declared once: key, label, help text, type, default, validation,
scope, secret flag, restart-required flag, minimum role. That single declaration
drives the database write, the validation, the UI control, and the permission check.

Adding a setting later is one registry entry. No migration, no new screen, no new
form component. This is what makes the UI-first configuration requirement
(`requirements.md` §5) affordable rather than a permanent tax.

### F-1.11 — Why the interface registry matters

The built-in implementation of each capability registers unconditionally at boot.
External adapters register only when their settings are present and valid.
Application code resolves a capability and never asks whether a specific vendor is
configured.

This is the mechanism behind "every external platform is optional". Without it,
`if (jiraConfigured)` spreads through ten screens and the guarantee quietly breaks.

---

## F2 — Job Pipeline

The user submits and leaves. This module is what makes that true.

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-2.1 | Background job queue with chaining: ingest → generate → execute → analyse | built-in | none | 0 | FR-1.6, NFR-1 |
| F-2.2 | Job status, progress percentage, and stage list | built-in | none | 0 | NFR-3 |
| F-2.3 | Live event log streamed over SSE | built-in | none | 0 | NFR-3 |
| F-2.4 | Idempotency keys — a retried job never duplicates generated rows | built-in | none | 0 | NFR-4 |
| F-2.5 | Automatic retry with backoff, up to 3 attempts | built-in | none | 0 | NFR-5 |
| F-2.6 | Cancel a running job or run | built-in | none | 0, 4 | FR-5.6 |
| F-2.7 | Concurrency limiting from settings | built-in | none | 0, 4 | FR-5.4 |
| F-2.8 | Internal scheduler for recurring runs and drift checks | built-in | none | 0, 11 | FR-9.4 |
| F-2.9 | Generic inbound webhook to trigger a run from anything | built-in | none | 0 | FR-10.2 |

F-2.8 and F-2.9 are the built-in `RunTrigger`. GitHub Actions and Jenkins (F-13.4)
are optional additions, not the only way to trigger work.

---

## F3 — Input and Ingestion

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-3.1 | OpenAPI / Swagger upload (JSON, YAML), `$ref` resolved, validated | built-in | none | 2 | FR-1.3 |
| F-3.2 | Postman collection import | built-in | none | 2 | FR-1.3 |
| F-3.3 | Requirement text pasted directly | built-in | none | 2 | FR-1.3 |
| F-3.4 | Source archive upload (zip, tar) | built-in | none | 6 | FR-1.3 |
| F-3.5 | Clone-by-URL with an optional token | built-in | none | 6 | FR-1.3 |
| F-3.6 | GitHub / GitLab OAuth connection, repo browser | optional | none | 10 | FR-10.1 |
| F-3.7 | SQL dump upload for schema-aware test data | built-in | none | 8 | FR-1.3 |
| F-3.8 | Read-only Postgres inspection via MCP | optional | none | 8 | — |
| F-3.9 | PDF / DOCX requirement documents | built-in | `reasoning` | post-v1 | FR-1.3 |
| F-3.10 | SHA-256 checksum per artifact; identical re-upload skips regeneration | built-in | none | 0 | FR-1.4 |
| F-3.11 | Upload guards: size cap, MIME allowlist, archive-bomb rejection | built-in | none | 0 | §8.4 |
| F-3.12 | Test-type selection at project creation; unimplemented types visibly disabled, not hidden | built-in | none | 0 | FR-1.2 |
| F-3.13 | Optional target base URL and auth config; without it, generation works and execution is disabled with a stated reason | built-in | none | 2 | FR-1.5 |

**Spec parsing is deterministic code, not AI.** `pb33f/libopenapi`.
A model is never asked to read JSON structure (`ai-architecture.md` §2).

---

## F4 — Understanding

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-4.1 | Requirement extraction — features, business rules, validation rules, auth model, edge cases | built-in | `reasoning` | 2 | FR-2.1 |
| F-4.2 | Every extracted item traces back to its artifact and source location | built-in | none | 2 | FR-2.2 |
| F-4.3 | Extraction review screen before generation, per project toggle | built-in | none | 2 | FR-2.3 |
| F-4.4 | Repository comprehension — map controllers, services, data access; find uncovered paths | built-in | `code` | 6 | FR-2.1 |

F-4.1 output is schema-locked to a Pydantic model. F-4.4 is a tool-using LangGraph
agent reading local files — not MCP, because the repo is already on worker disk.

---

## F5 — Test Case Management

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-5.1 | Test case design per requirement: happy path, each validation rule, each auth state, boundaries, negatives | built-in | `reasoning` | 2 | FR-3.1 |
| F-5.2 | Structured output validated against a schema; malformed responses retried with the error fed back | built-in | none | 2 | FR-3.3 |
| F-5.3 | Deterministic fingerprint per case from normalized method, path, and assertion kind | built-in | none | 2 | FR-3.5 |
| F-5.4 | Semantic dedupe for near-misses the fingerprint cannot catch | built-in | `cheap` | 2 | FR-3.5 |
| F-5.5 | Test case table: filter, inline edit, bulk approve and reject, add manually | built-in | none | 2 | FR-3.4 |
| F-5.6 | Traceability — every case links to its requirement; requirements with no cases are highlighted | built-in | none | 2 | FR-3.2 |
| F-5.7 | Pre-submit cost estimate using the assigned provider's real caching behaviour | built-in | none | 2 | — |

### F-5.7 — Why the estimate is provider-specific

Fan-out is the cost story: one spec, then a call per endpoint sharing that spec as a
prefix. Prompt caching is what makes it affordable, and providers implement it in
four incompatible ways (`ai-architecture.md` §3.6). The same 40-endpoint spec can
differ by roughly an order of magnitude in cost between providers. A single
provider-agnostic number would be misleading, so the estimate is computed from the
assigned provider's actual mechanism.

---

## F6 — Code Generation

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-6.1 | API tests — Supertest | built-in | `code` | 3 | FR-4.2 |
| F-6.2 | API tests — Postman collection | built-in | `code` | 3 | FR-4.2 |
| F-6.3 | Unit tests — Jest, Vitest, pytest, chosen by detected stack | built-in | `code` | 6 | FR-4.2 |
| F-6.4 | UI tests — Playwright | built-in | `code` | 7 | FR-4.2 |
| F-6.5 | Performance scripts — k6 | built-in | `code` | 9 | FR-4.2 |
| F-6.6 | Security tests — endpoint and parameter selection by AI, payloads from a curated library | built-in | `code` | 9 | FR-4.2 |
| F-6.7 | Framework auto-detection from manifest files, overridable per project | built-in | none | 6 | FR-4.3 |
| F-6.8 | Post-generation static validation: parses, imports resolve, no placeholder assertions | built-in | none | 3 | — |
| F-6.9 | File tree browser with syntax highlighting | built-in | none | 3 | FR-4.4 |
| F-6.10 | Export per file or full suite as zip | built-in | none | 3 | FR-4.4 |
| F-6.11 | Each generated file records the test case IDs it covers | built-in | none | 3 | FR-4.5 |
| F-6.12 | Selector policy enforced in code: `data-testid` → role/label → text. Positional CSS rejected and regenerated | built-in | none | 7 | — |

F-6.6 is deliberately split: the model decides *where* to probe, a curated payload
library decides *what* to send. A model does not invent security payloads.

F-6.12 is a hard rule, not a prompt suggestion. AI proposes a selector, code
validates it, and a positional CSS selector is rejected.

---

## F7 — Execution

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-7.1 | Isolated container per run; gVisor or Firecracker runtime, configurable | built-in | none | 4 | FR-5.1, §8.2 |
| F-7.2 | One-shot containers, destroyed after every run, never reused | built-in | none | 4 | NFR-6 |
| F-7.3 | Resource limits: CPU, memory, processes, wall clock, disk writes | built-in | none | 4 | FR-5.4 |
| F-7.4 | Default-deny egress; project allowlist plus required registries only | built-in | none | 4 | §8.2 |
| F-7.5 | Cloud metadata endpoint blocked at the network layer | built-in | none | 4 | §8.2 |
| F-7.6 | Non-root user, read-only root filesystem, small writable tmpfs | built-in | none | 4 | §8.2 |
| F-7.7 | Per-project target host allowlist, enforced server-side before enqueue | built-in | none | 4 | FR-5.5, §8.3 |
| F-7.8 | SSRF guard — private, loopback, and link-local targets rejected unless explicitly opted in | built-in | none | 4 | §8.3 |
| F-7.9 | Live log tail during a run | built-in | none | 4 | FR-5.6 |
| F-7.10 | Artifact capture: logs, screenshots, videos, traces | built-in | none | 4, 7 | FR-5.3 |
| F-7.11 | Flake detection by re-running failures and counting result changes | built-in | none | 4 | FR-5.7 |
| F-7.12 | Flake quarantine — repeatedly flaky tests surfaced for review, not failing every run | built-in | none | 7 | FR-5.7 |
| F-7.13 | Pinned runner images by digest, updatable from settings | built-in | none | 4 | — |
| F-7.14 | Coverage tool executed inside the runner; line and branch coverage parsed and stored | built-in | none | 6 | FR-7.2 |
| F-7.15 | Bundled Playwright container as the built-in browser driver | built-in | none | 7 | — |
| F-7.16 | Playwright MCP server as an alternative driver behind the same interface | optional | none | 7 | — |

**This module is a security boundary.** It executes model-generated code derived
from user-uploaded files. F-7.1 through F-7.8 are not hardening to add later; they
are the reason this is the largest phase. Gates G2 and G3 in `plan.md` must be
closed before it starts.

---

## F8 — UI Flow Discovery

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-8.1 | Agent drives a real browser through the app, discovering routes, forms, and flows by interacting | built-in | `code` | 7 | FR-2.1 |
| F-8.2 | Configured auth flow handled during discovery | built-in | none | 7 | — |
| F-8.3 | Flow graph recorded as structured output, reviewable before spec generation | built-in | `code` | 7 | — |
| F-8.4 | Screenshot understanding for visual layout checks | built-in | `vision` | 7 | — |

Driving the real app beats reading component source, which misses runtime
behaviour, conditional rendering, and real auth. F-8.4 requires a model with vision
capability; the UI hides models that lack it from the `vision` tier (`ai-architecture.md` §3.4).

---

## F9 — Failure Analysis and Defects

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-9.1 | Root cause analysis per failure: reason, probable cause, suggested fix | built-in | `reasoning` | 5 | FR-6.1 |
| F-9.2 | Mandatory evidence — specific log lines, response fields, or source locations. An analysis with no evidence is rejected and retried | built-in | none | 5 | FR-6.1 |
| F-9.3 | Source file and git blame included when a repository is connected | built-in | none | 5 | FR-6.2 |
| F-9.4 | Stability score from re-run consistency or blame overlap — never a model self-report | built-in | none | 5 | FR-6.3 |
| F-9.5 | Analysis panel with evidence highlighted in context | built-in | none | 5 | FR-6.1 |
| F-9.6 | Thumbs up / down feedback stored for prompt iteration | built-in | none | 5 | — |
| F-9.7 | Internal defect tracker: severity, status, assignee, comments | built-in | none | 5 | FR-11 |
| F-9.8 | Promote a failure with an analysis to a defect in one action | built-in | none | 5 | FR-11.1 |
| F-9.9 | Defect traceability to run result, test case, requirement, analysis, artifacts | built-in | none | 5 | FR-11.2 |
| F-9.10 | Duplicate linking — same test case, same root cause links to the open defect | built-in | none | 5 | FR-11.4 |
| F-9.11 | Push a defect to Jira; Jira key stored back on the internal defect | optional | `cheap` | 10 | FR-11.6, FR-10.3 |

### F-9.4 — No fabricated confidence

The original vision sketched a "Confidence 92%" field. A model asked how confident
it is produces a number with no calibration behind it, and showing it invites
decisions it cannot support. F-9.4 replaces it with a stability score derived from
something measurable: how consistently the failure reproduces across re-runs, or
how recently the named file was touched. If neither signal is available, no number
is shown.

### F-9.7 — Built-in first

The defect tracker ships in Phase 5, alongside analysis, not in Phase 10 with the
integrations. Bug tracking must work before any external tracker exists. When Jira
is configured it becomes a mirror; the internal defect stays source of truth inside
Qavia.

---

## F10 — Test Data and Mocking

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-10.1 | Schema-driven test data via seeded faker — deterministic, free, instant | built-in | none | 8 | FR-4.2 |
| F-10.2 | AI assist for semantically tricky fields only (realistic address, valid GSTIN shape) | built-in | `cheap` | 8 | — |
| F-10.3 | Boundary and invalid data sets: empty, oversized, wrong type, injection-shaped | built-in | none | 8 | — |
| F-10.4 | Payment card numbers drawn only from published test ranges | built-in | none | 8 | §8.4 |
| F-10.5 | Export as CSV, JSON, SQL | built-in | none | 8 | — |
| F-10.6 | Mock server generated from the spec, schema-valid responses | built-in | none | 8 | — |
| F-10.7 | Mock fault injection: delay, 500s, timeouts, configurable random failure rate | built-in | none | 8 | — |
| F-10.8 | Mock server runs as its own container per project with a live URL | built-in | none | 8 | — |

**No real personal data, ever.** F-10.4 is a hard constraint: test-range PANs only,
never freshly generated Luhn-valid numbers outside those ranges.

Bulk data generation is deliberately not an AI feature. Faker seeded from the schema
is deterministic and costs nothing; a model is used only where semantics matter.

---

## F11 — Performance and Security Testing

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-11.1 | k6 script generation from endpoints plus a load profile chosen in the UI | built-in | `code` | 9 | FR-4.2 |
| F-11.2 | Latency and throughput charts from parsed k6 metrics | built-in | none | 9 | FR-7 |
| F-11.3 | Security probes: SQLi, XSS, CSRF, JWT tampering, IDOR, rate limit, broken auth | built-in | `code` | 9 | FR-4.2 |
| F-11.4 | Findings with severity, evidence, and reproduction steps | built-in | none | 9 | — |
| F-11.5 | Both test types disabled by default; enabled per project by QA Lead or Admin | built-in | none | 9 | §8.3 |
| F-11.6 | Explicit host confirmation before the first run against a new target | built-in | none | 9 | §8.3 |

Pointed at a host you do not own, these are indistinguishable from an attack.
F-11.5 and F-11.6 exist for that reason and are not conveniences to skip.

---

## F12 — Reporting

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-12.1 | Project dashboard: requirements, generated tests, passed, failed, skipped, flaky, last run, job status | built-in | none | 2–5 | FR-7.1 |
| F-12.2 | Requirement coverage — requirements with at least one passing test | built-in | none | 2 | FR-7.2 |
| F-12.3 | Code coverage — line and branch, from the language's own tool | built-in | none | 6 | FR-7.2 |
| F-12.4 | Recent failures with direct links to analysis | built-in | none | 5 | FR-7.3 |
| F-12.5 | Report export as HTML and PDF | built-in | none | 5 | FR-7.4 |
| F-12.6 | Run history and trend over time | built-in | none | 4 | FR-7 |
| F-12.7 | AI spend dashboard by provider, project, and agent | built-in | none | 1 | §5.2 |

### F-12.2 / F-12.3 — Two numbers, never one

The original vision showed a single "Coverage 91%". That number is ambiguous —
requirement coverage and code coverage measure different things and can diverge
sharply. They are displayed as two separate, separately-labelled metrics. Code
coverage appears only when a repository is connected, rather than being estimated.

---

## F13 — Optional External Integrations

Every feature here is optional and additive. Removing its configuration reverts
cleanly to the built-in equivalent. Runtime failure degrades to the built-in,
records the failure, and alerts an admin (FR-10.6).

| ID | Feature | Kind | AI | Phase | Built-in equivalent |
|---|---|---|---|---|---|
| F-13.1 | Jira — create and sync defects | optional | `cheap` | 10 | F-9.7 internal defect tracker |
| F-13.2 | GitHub / GitLab — repo access, PR diff reading, PR creation with a suggested fix | optional | none | 10 | F-3.4, F-3.5 upload and clone-by-URL |
| F-13.3 | Slack — run summaries to a channel | optional | none | 10 | F-14.1 in-app notification |
| F-13.4 | GitHub Actions / Jenkins — trigger a run, receive a status check or PR comment | optional | none | 10 | F-2.8, F-2.9 scheduler and generic webhook |
| F-13.5 | Email via SMTP | optional | none | 10 | F-14.1 in-app notification |
| F-13.6 | MinIO / S3 object storage | optional | none | 0 | Local disk |
| F-13.7 | MCP servers — Jira, GitHub, Slack, Postgres, Playwright | optional | none | 7, 8, 10 | Built-in local tools and drivers |

### F-13.7 — MCP guardrails

An MCP server is a channel through which model output reaches a system that can
change state. Required controls:

- **Deny-all tool allowlist.** An admin opts into specific tools; a server's full
  tool set is never enabled by default.
- **Write operations need confirmation** or an explicit per-project auto-write
  setting. An agent reasoning over failures must not silently file twenty tickets.
- **Least privilege credentials.** Read-only Postgres role. GitHub contents-read
  unless PR creation is actually enabled, then contents-write on named repositories
  only.
- **Prompt injection is a live risk.** Agents read untrusted content — uploaded
  specs, client source, HTTP response bodies, test logs. Any of it can contain text
  shaped like an instruction. An agent holding a write-capable tool *and* reading
  untrusted content is the combination that turns injection into action. Keep write
  tools off those agents.
- **Full audit.** Every MCP call logged with server, tool, arguments
  (credential-shaped values redacted), agent, and job.

---

## F14 — Notifications

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-14.1 | In-app notification centre — always available, zero configuration | built-in | none | 0 | FR-8.2 |
| F-14.2 | Notify on job completion, failure, or needing input | built-in | none | 0 | FR-8.1 |
| F-14.3 | Per-user, per-channel toggles | built-in | none | 0 | FR-8.4 |
| F-14.4 | Deep link to the result in every notification | built-in | none | 0 | FR-8.5 |
| F-14.5 | Unconfigured channels shown as "Not configured", never as an error | built-in | none | 0 | FR-8.3 |

---

## F15 — Maintenance and Drift

The differentiating module. Generating tests once is table stakes; keeping them
correct as the system changes is what teams actually pay for.

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-15.1 | Artifact versioning by SHA-256; re-ingest on change | built-in | none | 11 | FR-9.1 |
| F-15.2 | Structural diff between spec versions — deterministic code, not AI | built-in | none | 11 | FR-9.1 |
| F-15.3 | Change classification: endpoint added, removed, signature changed, semantics changed, no change | built-in | none | 11 | FR-9.2 |
| F-15.4 | Per-case decision — regenerate, mark stale, or delete — from the computed diff | built-in | `reasoning` | 11 | FR-9.3 |
| F-15.5 | User approves the proposed diff before anything is applied | built-in | none | 11 | FR-9.3 |
| F-15.6 | History retained via `superseded_by`; a wrongly-deleted case can be restored | built-in | none | 11 | FR-9.4 |
| F-15.7 | Scheduled drift check with notification | built-in | none | 11 | FR-9.4 |

The diff itself is code. The model only interprets a diff it is handed, and only to
decide what to do about affected test cases. This split is why F-15.2 is reliable
and cheap while F-15.4 is the only metered step.

F-15 depends on traceability fields (`artifacts.sha256`, `artifacts.version`,
`test_cases.requirement_id`) that ship in Phase 0. Retrofitting them later is a
rewrite, which is why they are in the foundation rather than here.

---

## F16 — AI Configuration

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-16.1 | Seven provider kinds: Anthropic, Bedrock, Vertex, OpenAI, Azure OpenAI, Gemini, OpenAI-compatible | built-in | none | 1 | §5.2 |
| F-16.2 | Add any provider from the UI with base URL, key, and model name — no deploy | built-in | none | 1 | §5.2 |
| F-16.3 | Abstract tiers — `reasoning`, `code`, `cheap`, `vision` — mapped to models in settings | built-in | none | 1 | — |
| F-16.4 | Capability matrix per model: tool use, vision, structured-output mode, caching mode, effort support | built-in | none | 1 | — |
| F-16.5 | Test connection probe that detects real capabilities and overwrites declared defaults | built-in | none | 1 | — |
| F-16.6 | Three prompt-caching strategies selected per provider | built-in | none | 1 | — |
| F-16.7 | Structured output normalized across providers, with validation and retry | built-in | none | 1 | FR-3.3 |
| F-16.8 | Cross-provider fallback on retryable errors, recording which provider served each call | built-in | none | 1 | — |
| F-16.9 | Per-call accounting: tokens, cache reads, cost, latency, agent, job, project | built-in | none | 1 | NFR-8 |
| F-16.10 | Spend ceiling per provider and total, with block or warn behaviour | built-in | none | 1 | §5.2 |
| F-16.11 | Editable price table per model — pricing changes without a deploy | built-in | none | 1 | — |
| F-16.12 | Per-project provider pinning and data-residency enforcement | built-in | none | 1 | §8.1 |
| F-16.13 | `external_ai_approved` project flag, admin-set and audited | built-in | none | 1 | §8.1 |

### F-16.12 / F-16.13 — The compliance mechanism

Hyscaler is a services company; client repositories and specs are likely under NDA.
These two features turn that from a blocker into a configuration choice.

A project without `external_ai_approved` may only be assigned providers marked
`data_residency: local`. Enforcement is a server-side check before any AI job is
enqueued, not a UI hint. So a client who refuses external processing gets their
project pinned to a local Ollama or vLLM instance, and the platform physically
cannot send their code elsewhere.

The trade-off is real and should be stated to the client rather than papered over:
a local 32B model will be materially worse than a frontier model at extraction and
analysis. The assigned provider is recorded on every generated artifact so output
quality can be traced back to what produced it.

---

## F17 — Audit and Governance

| ID | Feature | Kind | AI | Phase | Req |
|---|---|---|---|---|---|
| F-17.1 | Audit log: login, settings change, run trigger, allowlist change, project approval change, secret rotation | built-in | none | 0 | §8.4 |
| F-17.2 | Runner command log, keyed by run ID | built-in | none | 4 | §8.2 |
| F-17.3 | MCP call log with redacted arguments | built-in | none | 10 | §8.4 |
| F-17.4 | Secrets never in logs, API responses, error messages, or notification bodies | built-in | none | 0 | NFR-10 |
| F-17.5 | Artifact retention policy in days, configurable | built-in | none | 0 | §5.2 |

---

## Feature Count by Phase

| Phase | Features | Theme |
|---|---|---|
| 0 | 30 | Foundation, settings, pipeline, capability interfaces |
| 1 | 14 | AI provider layer |
| 2 | 16 | Ingest, extraction, test case generation |
| 3 | 6 | Code generation and export |
| 4 | 16 | Execution engine |
| 5 | 12 | Failure analysis and defects |
| 6 | 7 | Repository and unit tests |
| 7 | 9 | UI tests |
| 8 | 9 | Test data and mocking |
| 9 | 6 | Performance and security |
| 10 | 9 | Optional integrations |
| 11 | 7 | Maintenance and drift |

Phases 0 through 5 — 94 features — deliver the usable product:
spec in, reviewed test cases out, code generated, tests executed, failures explained,
defects filed. Everything after that is expansion.

---

## Deferred Beyond v1

Recorded so scope stays honest.

| Feature | Reason |
|---|---|
| Google Workspace SSO | Local login covers v1. Contained addition later — one OIDC login handler plus a settings group, behind the same session middleware, no change to the role or project-scope middleware and no change to routes |
| Bruno, REST Assured, NUnit, JUnit exporters | Supertest and Postman cover v1 need |
| Cypress, Selenium generation | Playwright covers v1 |
| JMeter, Artillery | k6 covers v1 |
| PDF / DOCX requirement ingestion | Lower signal than OpenAPI; parsing risk |
| Mobile testing (Appium) | Different runner and device infrastructure |
| Contract testing (Pact) | Separate discipline |
| Accessibility audits | Separate discipline |
| Qavia as an MCP server, for use from an IDE | Genuinely useful; keep generate and analyse callable as clean service functions so it stays easy to add |
| Multi-tenancy, billing, public sign-up | Internal tool, not a SaaS |
