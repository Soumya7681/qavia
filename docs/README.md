# Qavia — Master Document

Internal Hyscaler platform that acts as an AI QA engineer.

A user logs in, creates a project, picks which kinds of tests they want, uploads what
the system needs, and submits. The platform works in the background: understands the
input, generates test cases and runnable test code, executes those tests in isolated
containers, analyses failures, and notifies the user when it is done. The user does
not babysit it.

This document is the entry point. Every other document is linked from here.

---

## Document Map

| Document | Answers | Read it when |
|---|---|---|
| **[requirements.md](./requirements.md)** | *What* must be built. Scope, roles, functional requirements, settings rules, data model, security gates, acceptance criteria. | Before writing anything. It is the contract. |
| **[features.md](./features.md)** | *Which* features exist. 17 modules, every feature with its phase, AI tier, and built-in vs optional status. | Planning a sprint, or checking whether something is in scope. |
| **[ai-architecture.md](./ai-architecture.md)** | *How* the AI layer works. Where AI is and is not used, multi-provider design, LangChain and LangGraph roles, MCP, cost handling. | Touching anything that calls a model. |
| **[tech-stack.md](./tech-stack.md)** | *Why* each technology. Every choice with its rejected alternatives and its cost. | Questioning a dependency, or onboarding. |
| **[backend-standards.md](./backend-standards.md)** | *How* to write the backend. Layout, layering, error handling, jobs, database, API, security, testing, definition of done. | Every PR. Section 15 is the review checklist. |
| **[plan.md](./plan.md)** | *In what order*. 12 phases, milestones, blocking gates, sequencing rules, relative effort. | Sequencing work, or reporting status. |
| **[work.md](./work.md)** | *Who does what, next*. The execution order: two tracks, the backend/frontend seam, the cross-track dependency matrix, gate owners, status board. | Picking up work, or checking what is blocked. |
| **[work-backend.md](./work-backend.md)** | Every backend task `BE-*`, in build order, with steps and a verifiable "done when". Runs to completion first. | Building anything server-side. |
| **[work-frontend.md](./work-frontend.md)** | Every frontend task `FE-*`, with the backend task and endpoints it consumes. Blocked until the contract freeze, `BE-X.1`. | Building anything in `web/`. |
| **[competitors.md](./competitors.md)** | *Who else does this*. Top 10 competitors by scope overlap, all 145 features scored against the market, gaps, and build-buy-wrap calls. Also as **[competitors.html](./competitors.html)**, standalone and filterable. | Justifying the build, setting the M2 baseline, or scoping a phase that a vendor already covers. |
| **[presentation.html](./presentation.html)** | All of the above, condensed into a readable walkthrough with architecture and pipeline diagrams. Standalone file, no internet needed. | Walking a stakeholder or a new joiner through the whole plan in one pass. |

The original brainstorm is `../plan.txt`, kept for history. It is superseded by these
documents wherever they disagree.

### Reading order

**New developer** — `README.md` → `tech-stack.md` → `backend-standards.md` →
`requirements.md` §5 (settings) and §8 (security) → the phase in `plan.md` you are
working on → your task in `work-backend.md` or `work-frontend.md`.

**Picking up work**: `work.md` first. It says which track is active, what is
blocked, and which gate is still open.

**Reviewer** — `backend-standards.md` §15, then the feature entry in `features.md`.

**Stakeholder** — this document, then `requirements.md` §1–3 and §9, then the phase
order in `plan.md`.

---

## Six Governing Principles

Cross-cutting rules. Each is owned by one document and referenced everywhere else. A
change that breaks one of these needs an explicit decision, not a shortcut in a PR.

### 1. Configuration lives in the UI, not the environment

Five bootstrap environment variables exist: `DATABASE_URL`, `REDIS_URL`,
`APP_ENCRYPTION_KEY`, `APP_URL`, plus `PORT` and `APP_ENV`. Everything else is a
setting in the database, edited from the UI, resolved
`user → project → global → default`.

Adding a sixth environment variable is a design error until proven otherwise.

> Owned by `requirements.md` §5. Enforced by `backend-standards.md` §6.
> Mechanism: the settings registry, F-1.5.

### 2. Every external platform is optional

The platform works completely on its own infrastructure. Jira, Slack, SMTP, GitHub,
GitLab, Jenkins, S3, and every MCP server are optional enhancements. An unconfigured
integration is never an error — the UI reads "Not configured — using built-in *X*".

The one genuine dependency is a model, and that is satisfiable entirely on Hyscaler
hardware via a local provider.

> Owned by `requirements.md` §5.4. Enforced by `backend-standards.md` §7.
> Mechanism: the capability interface registry, F-1.11.

### 3. AI is used in nine places, and deliberately nowhere else

Spec parsing, spec diffing, framework detection, coverage numbers, confidence scores,
flake detection, mock responses, bulk test data, security payloads, and cost
accounting are all deterministic code. Reaching for a model there costs money and
loses determinism.

> Owned by `ai-architecture.md` §1 and §2.

### 4. Agents ask for a tier, never a provider or model

Prompts reference `reasoning`, `code`, `cheap`, or `vision`. Settings map each tier
to a concrete provider and model. Seven provider kinds are supported, including any
OpenAI-compatible endpoint. Switching providers is a dropdown, not a deploy.

> Owned by `ai-architecture.md` §3. Enforced by `backend-standards.md` §10 and
> `plan.md` sequencing rule 5.

### 5. Build the built-in path first, in full

No feature may ship with an external integration as its only implementation.
Internal defect tracker before Jira. Bundled Playwright container before Playwright
MCP. In-app notification before SMTP. Local disk before S3. If a feature cannot work
with an empty integrations table, it is not done.

> Owned by `plan.md` sequencing rule 6. Verified by acceptance criterion 0.

### 6. Traceability goes in at the foundation

`artifacts.sha256`, `artifacts.version`, `test_cases.requirement_id`, and
`test_cases.fingerprint` ship in Phase 0, before anything needs them. They are what
make deduplication, requirement coverage, and the entire maintenance module possible.
Retrofitting them later is a rewrite.

> Owned by `requirements.md` §7. Depended on by F15.

---

## Architecture at a Glance

```
                    ┌──────────────────────────────┐
   Browser ────────▶│  Next.js  (App Router)       │
                    └───────────────┬──────────────┘
                                    │
                    ┌───────────────▼──────────────┐
                    │  Go API + worker             │  owns ALL persistence
                    │  auth · settings · jobs      │
                    │  capability registry         │
                    └──┬────────┬────────┬─────────┘
                       │        │        │
        ┌──────────────▼─┐  ┌───▼────┐  ┌▼─────────────────┐
        │ PostgreSQL     │  │ Redis  │  │ Object store     │
        │ (sqlc + pgx)   │  │ Asynq  │  │ disk/MinIO/S3    │
        └────────────────┘  └────────┘  └──────────────────┘
                       │
        ┌──────────────▼──────────────┐   ┌──────────────────────┐
        │ Python FastAPI  (stateless) │   │ Docker + gVisor      │
        │ LangChain · LangGraph       │   │ one-shot test runner │
        │ LLMGateway ─▶ any provider  │   └──────────────────────┘
        └─────────────────────────────┘
```

Three boundaries that matter:

- **Go owns all persistence.** The Python service has no database connection, and the
  web app has no credentials. One migration history, one place invariants live.
- **The runner is a security boundary.** It executes model-generated code. gVisor,
  default-deny egress, one-shot containers, no route to production.
- **`LLMGateway` is the only place providers are constructed.** No agent knows which
  provider it is talking to.

---

## Status

Phases are ordered by dependency. Effort is relative, not a duration — see
`plan.md` for what drives each one.

| Phase | Effort | Theme | Gate |
|---|---|---|---|
| 0 | L | Foundation, settings, job pipeline, capability registry | — |
| 1 | M | AI provider layer | — |
| 2 | L | Ingest, extraction, test case generation | **G1** |
| 3 | S | Code generation and export | — |
| 4 | XL | Execution engine | **G2, G3** |
| 5 | M | Failure analysis, defect tracker | — |
| 6 | L | Repository ingest, unit tests | — |
| 7 | XL | UI tests | G3 |
| 8 | M | Test data, mock server | — |
| 9 | L | Performance, security testing | G3 |
| 10 | M | Optional external integrations | — |
| 11 | L | Maintenance and drift | — |

Phases 0–5 deliver the usable product: spec in, reviewed test cases out, code
generated, tests executed, failures explained, defects filed. Everything after is
expansion.

Phases 4 and 7 are the largest and carry the most risk — one is a security boundary,
the other fights flake. Neither should be treated as an average phase.

### Milestones

| | After | Proves |
|---|---|---|
| **M1** | Phase 0 | A user can submit, walk away, and be notified |
| **M2** | Phase 2 | A real QA engineer says the generated test cases are usable |
| **M3** | Phase 4 | Platform-generated tests run in the platform and match a local run |
| **M4** | Phase 5 | Generate → run → fail → analyse → file defect → fix, end to end |

**M2 is the real go/no-go.** If a working QA engineer says the generated cases are
generic, wrong, or slower to fix than to write from scratch, the answer is prompt and
schema iteration — not building Phase 3 on a weak foundation.

---

## Blocking Decisions

Three decisions must be **recorded before** the phase that needs them starts.

| Gate | Blocks | Needed |
|---|---|---|
| **G1** — Client data and external AI | Phase 2 | Contract review on NDAs and subprocessors. Per-project approval process. Provider data-residency policy. |
| **G2** — Runner isolation | Phase 4 | Container runtime chosen. Network policy defined. Runner host placement agreed. |
| **G3** — Target allowlisting | Phases 4, 7, 9 | Allowlist enforcement design. SSRF rules. Security-test approval flow. |

G1 is cheaper than it looks: because the platform is multi-provider, a client that
refuses external processing gets pinned to a local provider rather than being
unsupported. The contract review still has to happen — it just no longer blocks the
whole product on one answer.

> Detail: `requirements.md` §8. Sequencing: `plan.md`.

---

## Open Items

| # | Item | Needed by |
|---|---|---|
| 1 | First real project for Phase 2, plus a named QA engineer to review output | Phase 2 |
| 2 | AI spend ceiling — decides whether Phase 3 fans out per endpoint or batches | Phase 1 |
| 3 | Which provider kinds to support at launch — all seven is more surface than needed initially | Phase 1 |
| 4 | Contract review outcome for G1 | Phase 2 |
| 5 | Runner host environment — existing VM, new VM, or managed. Decides G2 options | Phase 4 |
| 6 | Local-model support at launch or later | Phase 1 |
| 7 | Queue on Redis (Asynq) or on Postgres (River). River enqueues inside the write transaction, which makes NFR-4 structural rather than disciplined, and removes `REDIS_URL`. It only pays off if the settings cache and session store move off Redis too (`tech-stack.md` §6) | Phase 0 |

---

## Maintaining These Documents

- **`requirements.md` is the contract.** Behaviour changes are agreed here first.
  Code that contradicts it is a bug in one of the two.
- **A new feature adds an entry to `features.md`** with its ID, phase, kind, and AI
  tier. A feature not in the catalog is not in scope.
- **A new setting adds a registry entry**, and the settings section of
  `requirements.md` if it is a new category.
- **A new dependency adds an entry to `tech-stack.md`** with its rejected
  alternatives and its cost. An unjustified dependency is a future migration.
- **No status banners, dates, or version stamps in these documents.** They go stale
  and become noise. Git history is the changelog.
- **When two documents disagree, `requirements.md` wins**, and the other is fixed in
  the same PR.
