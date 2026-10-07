# Qavia Work Order

The execution document. `plan.md` says *what order the phases go in*. This says
*who does which task, in which sequence, and what has to be true before the next
task starts*.

Two tracks, and they are **not** concurrent:

1. **Backend track** runs first and runs to completion. See
   **[work-backend.md](./work-backend.md)**.
2. **Frontend track** starts only after the backend contract freeze (task `BE-X.1`).
   See **[work-frontend.md](./work-frontend.md)**.

Everything below wires those two documents together: the ID scheme, the seam
between them, the dependency matrix, and the status board.

---

## 1. Document Map

| Document | Role in the work order |
|---|---|
| **work.md** (this) | Sequencing, the backend/frontend seam, dependency matrix, status board, gates |
| **[work-backend.md](./work-backend.md)** | Every backend task, `BE-*`, in build order, with steps |
| **[work-frontend.md](./work-frontend.md)** | Every frontend task, `FE-*`, in build order, with its backend dependency |
| [requirements.md](./requirements.md) | The contract. When a task and this disagree, requirements wins |
| [features.md](./features.md) | Feature IDs (`F-x.y`) that tasks reference |
| [plan.md](./plan.md) | Phase ordering, milestones M1 to M4, gates G1 to G3 |
| [ai-architecture.md](./ai-architecture.md) | Tier model, gateway shape, caching, MCP rules |
| [tech-stack.md](./tech-stack.md) | Chosen libraries and the rejected alternatives |
| [backend-standards.md](./backend-standards.md) | How code must be written. §15 is the PR checklist |

---

## 2. The Hard Rule: Backend Completes First

**No frontend task starts until `BE-X.1` (contract freeze) is signed off.**

Reasoning:

- The web app has no database credentials and no queue access
  (`backend-standards.md` §10). Every screen is a thin renderer of an API
  response, so a screen built against a guessed response shape is rebuilt when the
  real one lands.
- `openapi/qavia.yaml` is the only shared artifact between the two tracks
  (`tech-stack.md` §11). It is written before every handler, so by the time the
  backend is done, the whole frontend contract already exists and is generated,
  not hand-written.
- The settings UI is rendered from the registry served at
  `GET /api/v1/settings/registry` (`F-1.6`). Building that screen before the
  registry is populated means building against a fixture that will not match.

### The stated cost of this order

Sequential tracks mean nothing is demoable to a stakeholder until the backend is
complete, and any bad API shape is discovered late, in frontend integration,
rather than early. Three mitigations are built into the backend track so that cost
stays contained. They are not optional:

- **`BE-X.3` ships a mock API server** driven by `qavia.yaml` with realistic
  examples, so frontend work is never blocked on a live backend.
- **Every backend phase writes its OpenAPI paths first** (`BE-*` task step 1 in
  every case), so the contract accumulates continuously instead of at the end.
- **`BE-X.4` publishes the error-code catalog**, so the frontend branches on
  stable codes rather than on message strings.

If a demo is needed before the backend finishes, demo the API through the mock
server and the job pipeline, not a half-built UI.

---

## 3. ID Scheme and Wiring

| Prefix | Meaning | Lives in |
|---|---|---|
| `BE-S.n` | Backend setup, before phase 0 | work-backend.md |
| `BE-p.n` | Backend task in phase `p` (0 to 11) | work-backend.md |
| `BE-X.n` | Backend contract freeze and handoff | work-backend.md |
| `FE-S.n` | Frontend setup | work-frontend.md |
| `FE-p.n` | Frontend task for the screens of phase `p` | work-frontend.md |
| `FE-Q.n` | Frontend cross-cutting quality task | work-frontend.md |

Every task in both documents carries the same five fields:

```
### BE-0.13  Settings registry
Feature:    F-1.5                       ← features.md
Requires:   BE-0.6, BE-0.12             ← other tasks that must be done first
Blocks:     BE-0.14, BE-0.16, FE-0.4    ← what is waiting on it
Steps:      1..n                        ← the actual work
Done when:  a verifiable statement      ← not "code written"
```

**Wiring rule:** a task's `Blocks` list and its dependents' `Requires` lists must
agree. If you add a task, update both sides in the same PR. A frontend task always
names the backend task and the concrete endpoints it consumes.

---

## 4. The Seam

Six things cross from the backend track to the frontend track. Nothing else does.
If a frontend task needs something not in this list, the seam is wrong and the
backend track gets a task, not the frontend a workaround.

| # | Artifact | Produced by | Consumed by |
|---|---|---|---|
| S1 | `api/openapi/qavia.yaml`, complete and frozen | `BE-X.1` | `FE-S.3` generated TypeScript client |
| S2 | `GET /api/v1/settings/registry`, entries as JSON Schema | `BE-0.16` | `FE-0.4` settings UI, converted to Zod at runtime |
| S3 | SSE stream for job status and log tail | `BE-0.24`, `BE-4.11` | `FE-0.6`, `FE-4.3` |
| S4 | Error envelope `{ code, message, details? }` and the code catalog | `BE-0.3`, `BE-X.4` | Every `FE-*` task that renders an error |
| S5 | Session cookie semantics, role claims, project membership rules | `BE-0.10` | `FE-S.4` route guards |
| S6 | Cursor pagination shape `?limit=&cursor=` with `nextCursor` | `BE-0.9` convention | Every list screen |

Two conventions that are part of the seam and easy to forget:

- **Long operations return `202` with a job ID** (`backend-standards.md` §11). No
  frontend screen ever waits on a synchronous generate or run.
- **Secrets read back as `{ isSet, updatedAt, hint }`** (`F-1.8`). No secret input
  is ever pre-filled; it is a Replace action.

---

## 5. Backend Track Summary

Full detail in [work-backend.md](./work-backend.md).

| Phase | Tasks | Theme | Gate | Milestone |
|---|---|---|---|---|
| `BE-S` | S.1 to S.6 | Toolchain, repo skeleton, CI, local compose | | |
| `BE-0` | 0.1 to 0.30 | Foundation, settings, capabilities, job pipeline | | **M1** |
| `BE-1` | 1.1 to 1.15 | AI provider layer, gateway, accounting | | |
| `BE-2` | 2.1 to 2.14 | Ingest, extraction, test case generation | **G1** | **M2** |
| `BE-3` | 3.1 to 3.7 | Code generation and export | | |
| `BE-4` | 4.1 to 4.15 | Execution engine | **G2, G3** | **M3** |
| `BE-5` | 5.1 to 5.10 | Failure analysis, defect tracker | | **M4** |
| `BE-6` | 6.1 to 6.7 | Repository ingest, unit tests | | |
| `BE-7` | 7.1 to 7.7 | UI tests, browser driver | G3 | |
| `BE-8` | 8.1 to 8.7 | Test data, mock server | | |
| `BE-9` | 9.1 to 9.5 | Performance and security testing | G3 | |
| `BE-10` | 10.1 to 10.8 | Optional external integrations | | |
| `BE-11` | 11.1 to 11.7 | Maintenance and drift | | |
| `BE-X` | X.1 to X.5 | Contract freeze, fixtures, mock server, handoff | | **Frontend unblocked** |

**M2 is the go/no-go.** If a working QA engineer says the generated cases are not
usable, `BE-2` iterates. `BE-3` does not start (`plan.md` sequencing rule 1).

---

## 6. Frontend Track Summary

Full detail in [work-frontend.md](./work-frontend.md). Starts at `BE-X.1`.

| Phase | Tasks | Screens |
|---|---|---|
| `FE-S` | S.1 to S.6 | App scaffold, design system, generated client, auth guards |
| `FE-0` | 0.1 to 0.9 | Shell, auth, setup wizard, settings, projects, upload, jobs, notifications |
| `FE-1` | 1.1 to 1.5 | AI providers, models, tiers, budget, spend dashboard |
| `FE-2` | 2.1 to 2.6 | Extraction review, test case table, coverage, cost estimate |
| `FE-3` | 3.1 to 3.3 | File tree, code viewer, export |
| `FE-4` | 4.1 to 4.6 | Run trigger, live tail, results, artifacts, history |
| `FE-5` | 5.1 to 5.5 | Analysis panel, defect list and detail, reports |
| `FE-6` | 6.1 to 6.3 | Repository browser, coverage heat map, gap list |
| `FE-7` | 7.1 to 7.4 | Flow graph, Playwright artifacts, quarantine list |
| `FE-8` | 8.1 to 8.3 | Test data preview and export, mock server controls |
| `FE-9` | 9.1 to 9.3 | Load profile, k6 charts, security findings, host confirmation |
| `FE-10` | 10.1 to 10.3 | Integration settings, MCP settings, "Not configured" states |
| `FE-11` | 11.1 to 11.2 | Drift diff review and approval |
| `FE-Q` | Q.1 to Q.6 | States, accessibility, timezone, theme, e2e, performance |

---

## 7. Cross-Track Dependency Matrix

Read as: this frontend task cannot be built until these backend tasks are done and
their endpoints are in the frozen spec.

| Frontend task | Requires backend | Key endpoints |
|---|---|---|
| `FE-S.3` client generation | `BE-X.1` | whole spec |
| `FE-S.4` auth guards | `BE-0.10` | `POST /auth/login`, `POST /auth/logout`, `GET /me` |
| `FE-0.1` app shell | `BE-0.9`, `BE-0.10` | `GET /me` |
| `FE-0.2` login and lockout messaging | `BE-0.10`, `BE-0.11` | `POST /auth/login` |
| `FE-0.3` first-run wizard | `BE-0.28` | `GET /setup/status`, `POST /setup/admin` |
| `FE-0.4` settings UI from registry | `BE-0.13`, `BE-0.16` | `GET /settings/registry`, `GET/PUT /settings` |
| `FE-0.5` projects and members | `BE-0.25` | `/projects`, `/projects/{id}/members` |
| `FE-0.6` upload and submit | `BE-0.26`, `BE-0.22` | `POST /projects/{id}/artifacts`, `POST /projects/{id}/jobs` |
| `FE-0.7` job status and event log | `BE-0.22`, `BE-0.24` | `GET /jobs/{id}`, `GET /jobs/{id}/events` (SSE) |
| `FE-0.8` notification centre | `BE-0.19` | `/notifications` |
| `FE-0.9` audit log viewer | `BE-0.27` | `/audit` |
| `FE-1.1` to `FE-1.4` AI settings | `BE-1.13`, `BE-1.10` | `/ai/providers`, `/ai/models`, `/ai/tiers`, `/ai/budget`, `POST /ai/providers/{id}/test` |
| `FE-1.5` spend dashboard | `BE-1.14` | `GET /ai/spend` |
| `FE-2.1` extraction review | `BE-2.6` | `/projects/{id}/requirements` |
| `FE-2.2` test case table | `BE-2.10` | `/projects/{id}/test-cases` |
| `FE-2.4` coverage view | `BE-2.11` | `GET /projects/{id}/coverage/requirements` |
| `FE-2.5` cost estimate | `BE-2.12` | `POST /projects/{id}/estimate` |
| `FE-3.1` to `FE-3.3` code browser | `BE-3.5`, `BE-3.6` | `/projects/{id}/test-files`, `GET .../export` |
| `FE-4.1` run trigger | `BE-4.7`, `BE-4.9` | `POST /projects/{id}/runs` |
| `FE-4.3` live tail | `BE-4.11` | `GET /runs/{id}/logs` (SSE) |
| `FE-4.4` results table | `BE-4.9` | `/runs/{id}/results` |
| `FE-5.1` analysis panel | `BE-5.2`, `BE-5.3` | `GET /run-results/{id}/analysis` |
| `FE-5.3` defect tracker | `BE-5.5`, `BE-5.6` | `/defects`, `POST /run-results/{id}/promote` |
| `FE-5.5` report export | `BE-5.9` | `GET /projects/{id}/report` |
| `FE-6.1` to `FE-6.3` repo views | `BE-6.1`, `BE-6.7` | `/projects/{id}/repo`, `/projects/{id}/coverage/code` |
| `FE-7.1` flow graph | `BE-7.2` | `GET /projects/{id}/ui-flows/latest`, `GET /ui-flows/{id}` |
| `FE-7.2` Playwright artifacts | `BE-7.5` | `/runs/{id}/artifacts` |
| `FE-8.1` to `FE-8.3` data and mocks | `BE-8.5`, `BE-8.6` | `/projects/{id}/test-data`, `/projects/{id}/test-data/invalid`, `/projects/{id}/mock-server` |
| `FE-9.1` to `FE-9.3` perf and security | `BE-9.1`, `BE-9.4`, `BE-9.5` | `/runs?type=performance`, `/runs/{id}/metrics`, `/runs/{id}/findings`, `POST /projects/{id}/performance-tests`, `POST /projects/{id}/security-scans` |
| `FE-10.1` to `FE-10.3` integrations | `BE-10.1` to `BE-10.5` | `/settings/integrations`, `/mcp/servers` |
| `FE-11.1` to `FE-11.2` drift review | `BE-11.5` | `/projects/{id}/drift`, `POST /projects/{id}/drift/{id}/apply` |

---

## 8. Blocking Gates

A gate is a **recorded decision**, not a discussion. Record it in this table before
the first task of the blocked phase is picked up.

| Gate | Blocks | Decision needed | Owner | Recorded |
|---|---|---|---|---|
| **G1** Client data and external AI | `BE-2.*` | Contract review on subprocessors. Per-project approval process. Provider `data_residency` policy | | ☐ |
| **G2** Runner isolation | `BE-4.*` | Container runtime chosen. Network policy defined. Runner host placement agreed | | ☐ sign-off; design recorded below |
| **G3** Target allowlisting | `BE-4.*`, `BE-7.*`, `BE-9.*` | Allowlist enforcement design. SSRF rules. Security-test approval flow | | ☐ sign-off; design recorded below |

### G2 runner isolation, as built (BE-4.1)

The design half of G2 and G3 is recorded here. The **owner sign-off and the host
provisioning are not**, and nothing below substitutes for them: BE-4.1 steps 2 and 3
(provision a segment with no route to Hyscaler production, then prove it from the
host by testing) are deployment work that has to happen against real infrastructure
before this platform runs anything for a real client. The code assumes that
isolation; it does not create it.

**Runtime.** gVisor (`runsc`) is the default, set in `runner.runtime`. It is a
user-space kernel, so a container escape has to get through `runsc` before it
reaches the host kernel, and the workload here is model-generated code derived from
a file a client uploaded. `runc` is selectable for local development only and the
setting's help text says it is unsafe. `internal/runner.Runtime.Unsafe()` reports
that to anything that wants to warn.

**Network policy.** Deny by default, at the network layer. A run with an empty
allowlist gets `NetworkMode: "none"` — no interface, so no route, no DNS, and no
metadata endpoint to block. A run with an allowlisted target gets a bridge, and one
`ExtraHosts` entry per allowed host pinned to the address the platform already
resolved and checked, so a second lookup inside the container cannot move the
target. The cloud metadata names are pinned to `127.0.0.1` on top of that. The
application-level guard (`net.Dialer.Control`, BE-4.7) is the second layer, not the
control.

**Host placement.** One runner host, or pool, on a segment of its own, reachable
from the worker only, with no route to Hyscaler production or corporate systems, and
outbound egress filtered at the segment boundary rather than only per container. The
worker reaches Docker over a socket or a TLS endpoint that is not exposed to the
runner's own network. Until that host exists, `runner.driver` stays `docker`
pointing at a local development daemon and every run carries that risk.

**Container shape, per run.** One-shot, destroyed after the run and again by a
label-filtered sweeper that catches what a worker crash left behind. Non-root
(`1000:1000`), read-only root filesystem, a capped `tmpfs` for `/workspace` and
`/tmp`, all capabilities dropped, `no-new-privileges`, no host mounts at all (the
workspace is copied in), CPU, memory and pids limits from settings, and a wall clock
the driver enforces itself rather than trusting the test framework.

G1 is cheaper than it looks: a client refusing external processing gets pinned to a
local provider (`ai-architecture.md` §3.5) rather than being unsupported.

### Open items with a deadline

| # | Item | Needed by | Owner | Answered |
|---|---|---|---|---|
| 1 | First real client spec plus a named QA engineer to review output | `BE-2.14` | | ☐ |
| 2 | AI spend ceiling. Decides whether `BE-3.2` fans out per endpoint or batches | `BE-1.8` | | ☑ |
| 3 | Which provider kinds ship at launch. All seven is more surface than needed | `BE-1.4` | | ☐ |
| 4 | Contract review outcome for G1 | `BE-2.1` | | ☐ |
| 5 | Runner host environment. Decides G2 options | `BE-4.1` | | ☐ |
| 6 | Local model support at launch or later | `BE-1.4` | | ☐ |
| 7 | Queue on Redis (Asynq) or Postgres (River) | `BE-0.22` | | ☑ |

**Item 2 is decided: `BE-3.2` fans out per endpoint.** The specification is a cached
prefix either way, so batching saves no input tokens; what it would cost is the
per-endpoint retry, and one bad file failing a whole suite is the outcome that
matters. A spend ceiling is enforced per call regardless
(`internal/llm/gateway.go`), so the fan-out cannot outrun the budget.

**Item 7 is decided: Asynq on Redis**, per `tech-stack.md` §6. The settings cache
stays on Postgres `LISTEN`/`NOTIFY`, and so does the SSE fan-out, so Redis carries
only in-flight queue coordination plus webhook replay protection. Losing Redis
loses in-flight jobs, which retry, and loses no data.

The original note follows, because the argument for River still holds if Redis is
ever removed for another reason.

Item 7 is the only one that is expensive to change later. River enqueues inside the
write transaction, which makes NFR-4 structural rather than disciplined, and removes
`REDIS_URL`. It only pays off if the settings cache and session store move off Redis
too (`tech-stack.md` §6). Decide before `BE-0.22`, not after.

---

## 9. Working Agreement

- **Trunk based**, short-lived branches, one logical change per PR
  (`backend-standards.md` §16).
- **Conventional commits:** `feat:`, `fix:`, `refactor:`, `docs:`, `test:`,
  `chore:`. Reference the task ID in the body: `refs BE-0.12`.
- **Spec before handler, always.** Step 1 of every backend task that adds a route
  is editing `api/openapi/qavia.yaml`.
- **CI gates merge:** `golangci-lint`, `go vet`, `go test -race ./...`, integration
  tests, migration up and down, clean `go generate`, `pnpm lint` and `vitest` on
  `web/`, `ruff` and `pytest` on `services/ai/`.
- **Every PR is reviewed by the other developer.** Extra scrutiny on
  `internal/capability/`, `internal/platform/config/`, `migrations/`,
  `openapi/qavia.yaml`, `services/ai/src/llm/gateway.py`.
- **A task is done when its "Done when" statement is demonstrably true**, not when
  the code compiles. Check the box in §10 in the same PR.
- **Definition of done for any backend PR is `backend-standards.md` §15.** Copy it
  into the PR description.

---

## 10. Status Board

Tick a phase only when every task in it is done and its "Done when" holds.

### Backend

| Phase | Tasks | Status | Notes |
|---|---|---|---|
| `BE-S` Setup | 6 | ☑ | Go 1.25 toolchain; compose on 55432 / 56379 / 59000 so it coexists with a local Postgres or Redis |
| `BE-0` Foundation | 30 | ☑ | **M1 reached 2026-10-07.** All 30 done. **0.30 M1 verification, recorded**: run on a throwaway Postgres and Redis with the built `api` and `worker` binaries and no AI service. (1) Fresh database and exactly the six bootstrap variables: `/readyz` ok on database, queue, and local-disk storage. (2) Setup status reported no admin, `POST /setup/admin` created one (201), and the endpoint then answered 404. (3) Project created, OpenAPI file uploaded (201), `noop` chain submitted (202). (4) Session cookies dropped and no client attached while the chain ran. (5) Chain succeeded, exactly one in-app notification ("Pipeline check finished") whose link `/projects/{id}/jobs/{id}` resolves to that job. (6) Worker killed with SIGKILL while `noop.work` was running; on restart the chain succeeded with three stages, none duplicated, `noop.work` at attempts=2, and one notification. Recovery took about 70 seconds, which is the Asynq lease expiring, not a fault. (7) `jobs.max_attempts` changed from 3 to 7 through the API; the stages the worker enqueued next used 7 within 8 seconds, inside the 30 second cache TTL, so the LISTEN/NOTIFY invalidation did it, not expiry. Neither process log contained the encryption key, the admin password, or a password hash. **0.29 done**: `main.go` now assembles the API through one `build` function, and the integration harness (`cmd/api/harness_test.go`) boots exactly that against a testcontainers Postgres and Redis (`storetest.URL`, `storetest.Redis`), so no test runs against a hand-wired subset of handlers. `storetest.Truncate` reads the table list from the catalog; the hand-kept list had fallen 27 tables behind the migrations. `jobstest.RunTwice` runs a handler twice the way a redelivered task would and requires the same end state, first used on the retention sweep. Standing tests: every harness captures each response body and log line and fails at cleanup on a registered secret or a credential-shaped value (provider keys, AWS, GitHub, GitLab, Slack, private keys, argon2 hashes); `TestSecretsNeverLeaveThroughTheAPI` writes every credential the API accepts and reads back every surface; `TestZeroIntegrationInstallIsComplete` runs setup, upload, and a submitted chain on an empty settings and MCP table. **Verified**: making the settings hint return the plaintext failed the leakage test with 20+ reports, each naming the response without printing the secret. CI records coverage in the step summary and as an artifact, not as a gate |
| `BE-1` AI provider layer | 15 | ☐ | Done: 1.1 to 1.11, 1.13 to 1.15. Remaining: 1.12 prefix-stability test. Verified against a stub OpenAI-compatible provider: probe corrected declared capabilities, cost matched the price list to the eighth decimal, `/ai/spend` reconciled against a direct `SUM`, a ceiling below current spend refused `ai_smoke` at enqueue while `noop` still ran, an unapproved project was refused an external provider and allowed after approval, and switching the `cheap` tier to another provider changed which model served the next job with no deploy |
| `BE-2` Ingest and generation | 14 | ☐ | G1, M2. Built: 2.1 to 2.13. Remaining: 2.14 M2 evaluation, which needs a real client spec and a named QA reviewer (open items 1 and 4). **G1 was not recorded before this phase was built** — the code is in place, and the gate still has to be signed off before a client specification is processed. Verified against a stub provider: upload to test cases with no second submit, source refs traced to line:column, re-run produced zero new rows, review gate paused the chain, filters and bulk status covered, spend attributed per agent |
| `BE-3` Code generation | 7 | ☐ | Built: 3.1 to 3.6. Remaining: 3.7 phase verification, which needs a staging API to run the exported suite against. Verified against a stub provider: approved cases became a Supertest file reading its target from the environment, a deterministic Postman v2.1 collection with variables rather than literals, a byte-identical zip carrying a scaffold and a traceability README, and both traceability directions. **3.4 is done and verified end to end**: the stub was made to emit a file with a `// TODO` and `expect(true).toBe(true)`, the runner image's own toolchain caught both by line number, the file was regenerated with those findings appended to the instruction rather than to the cached specification prefix, and the correction passed and was marked validated. A file that fails twice is kept and carries the findings in its validation note, because a rejected file a reviewer can read beats a silent gap in the suite |
| `BE-4` Execution | 15 | ☐ | G2, G3, M3. Built: 4.1 (design half) to 4.14. Remaining: 4.15 M3 verification, which needs a staging target and the 400-test suite from BE-3.7; the isolation half of 4.1 (a runner host on its own segment, proven unreachable from production) is deployment work and is **not** done. **G2 and G3 were not signed off before this phase was built** — the design is recorded in section 8 and the code enforces it, and an owner still has to sign both before a client's suite runs against a client's environment. Verified on a real Docker host with `cmd/runnercheck`: workspace delivered over stdin, report collected, no egress by default, the metadata endpoint unreachable with and without a target allowlist, a non-allowlisted host rejected while the allowlisted one connected, an infinite loop killed by the driver's wall clock, a memory hog reported as a memory kill, a fork bomb capped by the pids cgroup, and nothing left behind. End to end through the API: a suite ran against a live target and reported 7 tests with 6 passed and 1 flaky, a deliberately intermittent test was reported flaky rather than failed with every attempt its own row, cancelling mid-run stopped the container in two seconds and kept partial results, a burst of ten runs held at exactly two containers with the rest reporting "waiting for a runner slot", a non-allowlisted target was refused at the API and audited as `target_rejected`, the SSE log tail replayed history then streamed live output and closed with `done`, and the dashboard counts reconciled with the newest run |
| `BE-5` Analysis and defects | 10 | ☐ | M4. Built: 5.1 to 5.9. Remaining: 5.10 M4 verification, which needs ten deliberate failures of known cause and a reviewer to agree the analyses name them; that is a judgement about model output and cannot be self-certified against a stub. **Deviation on 5.2**: the analyse agent is a single schema-locked call on the reasoning tier rather than a LangGraph graph with checkpointing. With one call, a worker restart re-runs it, which is what resuming would have achieved; the graph and its checkpoints become worth their complexity when the agent gains read-only repository tools in BE-6, and the endpoint is shaped so that change is internal to the Python service. Verified end to end against a stub provider: a deliberately invented citation ("log line 9999, and the log has 69 lines") was rejected, the correction passed and was stored, the stability score computed from stored results (7 of 10 recent results passed with 3 status changes across 2 attempts in the run), promoting the failure filed a defect with the analysis and run result linked, promoting it again returned the same defect with 200 rather than filing a second, the same test failing the same way in a later run was linked as a duplicate and unlinking it reopened it, the thread carried the system comment for the link, a status change to `fixed` set the resolution time by itself, feedback grouped by prompt version, and both report formats rendered the same content — HTML 11 KB and a three-page PDF printed by the browser in the runner image, each returned as a download rather than blocking a request |
| `BE-6` Repo and unit tests | 7 | ☑ | Built: 6.1 to 6.7. All seven delivered. **Deviation on 6.4**: the exploration loop runs in Go and the agent answers one step at a time, rather than the tools living beside the agent in `agents/repo.py`. The plan's own argument leads here — the repository is on worker disk and a supervised process between a worker and its own filesystem buys nothing (`ai-architecture.md` §5.2) — and Go is where that disk and its path validation are; the two processes do not share a filesystem, so tools in Python would have needed either a shared volume or a sandbox this platform would have had to build. The properties the plan asked for are kept (real tools over the real checkout, a bounded budget) and one is gained: every path a model asks for is refused by the code that owns the filesystem. Verified live: a scripted exploration ran glob, read, a deliberate `../../../etc/passwd`, and grep, answered on the fifth step, and produced a stored map whose trail records the traversal attempt as refused; a file the map named that does not exist at that revision was dropped and the drop recorded in `unknowns`, on the same principle as evidence validation in phase 5. Verified live for 6.5 to 6.7 on a two-file TypeScript repository: the map's untested paths became two vitest files, each compiled and scanned in the runner image before being kept; measured coverage rose from 2 of 4 lines to 4 of 4 as the second file was added, every figure parsed from istanbul's own `coverage-final.json` rather than scraped from output; the coverage endpoint returns the tool name and the exact command beside the numbers and reports a null rate rather than zero where nothing is measurable; and a repository that declares no coverage tool is refused with `no_coverage_tool` rather than measured with instrumentation this platform added, because a number that does not match the client's CI is worth nothing. Verified live for 6.1 to 6.3: a public repository cloned from GitHub with the commit recorded (`7fd1a60b`) and its stack honestly reported as unknown with the list of files read; the same project synced from an uploaded archive detected `typescript 22.11.0 vitest via npm` with the test command and coverage tool taken from the repository's own `package.json` and the three files it read named; the clone was refused before it started while `github.com` was not on the project's target allowlist, which is the same check a run's target goes through; and the workspace rules refused `../../../etc/passwd`, `/etc/passwd`, a symlink to `/etc` reached as `escape/passwd`, the same symlink reached as `src/../escape/passwd`, and a Windows-style path, while allowing `src/index.ts`. The token is stored as a project secret, read at the moment of the clone, and passed to git in its environment rather than in argv or a URL |
| `BE-7` UI tests | 7 | ☐ | Built: 7.1 to 7.6. Remaining: 7.7 the optional MCP driver, which requires BE-10.1 and is deliberately last, per the phase's own ordering. Verified live for 7.1: the bundled Playwright container drove a real browser against a stub app with an empty MCP settings table, and the trace, video, and screenshot all came back. Verified live for 7.2 and 7.3 against a stub app behind a form login: a discovery walked `/login`, filled both fields with the placeholders, signed in, and reached the gated page — which the stub only serves for the right password, so the substitution happened inside the container and the model never saw the credential; the recorded graph names three pages and one flow, the stored password appears nowhere in it, and the URL that carried it reads `pass=[redacted]`. Three refusals are enforced in code rather than asked for in the prompt: a click on `delete-all` was refused as destructive, a `goto` to `evil.example.com` was refused as off-target, and a page the agent recorded but never opened (`/admin`) plus the flow that navigated to it were dropped, on the same principle as dropping invented files from a repository map. Verified live for 7.4: the first generated spec used `div:nth-child(3) > button`, the selector policy rejected it before anything was stored or a container started, the regenerated spec located everything by test id, and the runner image's own toolchain accepted it — which also surfaced a real gap, that the Playwright image had no `@types/node`, so every spec reading `process.env` would have been rejected. Verified live for 7.5: a failing UI test produced a 43 KB WebM video, a 64 KB trace, and a 10 KB screenshot, their keys landed on `run_results` (with a new `trace_key` column), the list endpoint returned all three, each streamed back with the right content type, and a passing run produced none — capture is on failure, because a trace per passing test is megabytes nobody opens. Retention now covers them: a run backdated past the cutoff had all four stored files deleted and its keys cleared. Verified live for 7.6: a deliberately intermittent test flaked in three consecutive runs, was auto-quarantined with the arithmetic in its reason ("3 of the last 3 runs, above the threshold of 2"), and the next run recorded its failure and its pass as before while reporting the run as passed with `quarantined: 1` — the statuses still say what happened, and the count is separate from passed because a suite where six tests are excused is not a suite where they all pass. Claiming an owner, releasing with a note, and a manual quarantine all work, released rows are kept as history, and all three are audited. **Design notes.** A browser needs a container that survives across steps, so `internal/runner` gained sessions: one container per session, line-delimited JSON on stdin and stdout, destroyed at the end, every other property of the phase-4 boundary unchanged. Artifacts leave a container over that same channel base64-encoded rather than being copied out, because Docker cannot copy a file out of a tmpfs mount and the workspace is a tmpfs precisely because the root filesystem is read-only — the run artifacts use the same trick, through a manifest beside the report. The discovery loop runs in Go for the same reason the exploration loop does: the process that owns the container is the one that can hold the budget, check an action against the vocabulary, and substitute a credential without showing it to a model. And the Playwright configuration is baked into the image rather than generated beside the specs, so a generated suite cannot change how it is run — only what it does. **One latent bug fixed on the way:** every scheduled job had been failing since BE-0.20 with "job 00000000-0000-0000-0000-000000000000 not found", because the scheduler enqueues a bare task and the worker required a row that nothing created; retention had therefore never run on any installation. The worker now adopts a scheduled task into a row keyed by what the handler declares |
| `BE-8` Data and mocks | 7 | ☑ | Built: 8.1 to 8.7. All seven delivered. Generation is a seeded walk over the specification's own schemas in `internal/datagen`, and **bulk generation is not an AI feature**: the same seed and count reproduce a set byte for byte, which is what makes a failure on record 63 reproducible. Verified live: two identical requests returned identical bytes, an unseeded one reported the seed it was given so the same data can be asked for again, and CSV, JSON, and SQL all streamed — the SQL carrying a header that says it is test data and which seed produced it, every string quoted by the writer, and a table name of `tickets; drop table users` filtered to `ticketsdroptableusers` rather than merely quoted. Verified for 8.3: fifteen invalid records from a four-field schema, each breaking exactly one constraint and naming it — `title allows 200 characters, this is 201`, `priority must be one of low, high`, `id declares format uuid` — with the injection cases expecting "stored and returned verbatim, never executed" rather than "rejected", because an API may legitimately accept an apostrophe in a surname. Verified for 8.4 with an operator check over 200 records: 800 card values across four differently-named card fields, every one from the published test ranges, plus the documented decline cards for the failure path — **and the check found a real gap** on its first run, `creditCardNo` matching none of the exact field names and getting a sentence, which is why detection is now substring-based on the separator-stripped name. Verified for 8.2 end to end: a single named field came back from the model while the other three columns stayed with the faker, and the response said so ("3 value(s) across 1 field(s) came from the model"); with the provider unreachable and with the project unapproved for external AI, the same request succeeded with every value from the faker and the reason stated. Verified for 8.7: a `pg_dump --schema-only` upload parsed into two tables with types, lengths, nullability, and the `IN (...)` check read as an enumeration; generating from the `orders` table produced a `numeric(10,2)` amount, a `bigserial` id, a `date`, and a card number from the test ranges, and asking without naming a table when the dump declares two is refused with both names. Verified for 8.6 on a real Docker host: the mock came up in its own container with a published port and a URL, served three routes generated from the spec with schema-valid bodies rotating across samples, matched `/tickets/42` against `/tickets/{id}`, returned the endpoint's own documented 201 for a POST, 404'd an undeclared route with a reason, and had its host added to the project's target allowlist automatically; with a 30% failure rate configured, 21 of 60 requests failed and the mock's own counters agreed; stopping it left no container and corrected the row. **Design notes.** The package is `internal/datagen`, not `internal/testdata`, because the Go tool ignores any directory named `testdata` — `go mod tidy` silently dropped the faker dependency until the rename. A mock server is the third container shape after a run and a session, and the only one that listens: it keeps every other property of the phase-4 boundary and adds a published port and no egress, since a mock answers requests rather than making them. Its configuration arrives as one line on stdin and stdin stays open — **closing the write half of a hijacked Docker stream tears down the whole connection**, which showed up as a mock that started correctly and reported no output. **One latent bug fixed on the way:** the Playwright image had no `@types/node`, so any generated spec reading `process.env` would have been rejected by static validation |
| `BE-9` Perf and security | 5 | ☑ | G3. Built: 9.1 to 9.5. **Gate G3 still needs owner sign-off** — the code enforces the controls and they are verified below, but the sign-off is a person's, not the platform's. Verified live against a stub target and provider on a real Docker host. **9.1**: a `code`-tier agent wrote a k6 script from the endpoints under an authorised profile (5 VUs, 2s ramp, 8s hold), the script was validated by `k6 archive` before a request went out, ran in the k6 container, and stored a real series — 348 requests, 43/s throughput, p50 0.49ms / p95 0.91ms / p99 1.0ms, thresholds passed — read back from `/runs/{id}/metrics`; a functional run returns `no_performance_metrics`. **9.2**: the payload library is seven categories, fifteen reviewed payloads, each with a stated purpose and a default severity, checked complete by an operator pass; **the model never produces a payload string** — the library is a Go constant and the catalogue handed to the agent withholds the strings. **9.3**: the probe-select agent named endpoints and payload IDs only; the resolver drops any ID the library does not have and refuses a destructive method, so no attack string in the traffic was model-chosen. **9.4**: a scan against a `/tickets` endpoint with no auth check found the planted broken-auth vulnerability (critical), stored with the evidence the detection rule matched ("a request that should have been refused returned 200") and a reproduction ("GET …/tickets, no credential sent"); the finding is a failed `run_result` with security detail, and it **promoted to a defect (201, critical, linked to the result)** through the same path a failed test uses, because the scan writes a minimal analysis per finding whose root cause is the reviewed payload's own purpose. **9.5**: all four controls verified including the negative case — a performance and a security run were each refused with `test_kind_disabled` until a lead enabled the kind per project, then refused with `host_confirmation_required` naming the exact host until resent with `confirmHost`, and the confirmation is remembered per host; the allowlist check that rejects an off-list target before any traffic is the run path's existing target check, reused. **Design notes.** A run now carries a `kind` (functional / performance / security) so `/runs?type=…` filters without inferring from the framework, and a performance run stores its metrics in a `run_metrics` row and its authorised profile on the run. Performance and security are focused execute paths rather than the shared one: a load run has no retries (re-running under load is twice the load, not flake detection) and a scan has no per-test flake logic. The security probe runner is a fourth container shape — Node, no dependencies, the probe list delivered as a workspace file, deny-by-default egress, the same isolation as every other runner. **Two latent issues fixed on the way:** the k6 summary-export threshold shape is a bare boolean in this k6 version, not an object with `ok`, so the normaliser now handles both (found by a metrics-less first run); and the `ALTER TYPE test_framework ADD VALUE 'security'` needed a `-- +goose NO TRANSACTION` migration because an enum value cannot be added inside a transaction |
| `BE-10` Integrations | 8 | ☐ | In progress. **Built and verified: 10.4 and the core of 10.1.** **10.4 (Slack + SMTP notifiers)**: both register behind the `Notifier` interface beside the always-on in-app channel, read their config live from settings so a credential change takes effect on the next send, and report `Available()` false when unconfigured — so an unconfigured channel is the normal state, not an error. The service already fans out with degradation (in-app always lands, an external failure raises an admin alert), which is what makes "breaking the SMTP credentials mid-flight loses no notification" hold; the SMTP adapter refuses a plaintext fallback and sanitises the subject header, the Slack adapter reads `{ok:false}` bodies as failures rather than trusting the 200. **10.1 (MCP infrastructure)**: `mcp_servers` and `mcp_calls` tables per ai-architecture §5.4; a Go registry with per-project scoping (a project sees the global servers plus its own and no other project's), credentials encrypted with the same per-row-keyed cipher as every secret and never returned, a **deny-all** allowlist (a new server starts with zero tools enabled — the safe reading of the schema doc's "empty means all"), and a call audit that redacts credential-shaped argument values. The protocol is bridged in the Python service via `langchain-mcp-adapters` (0.2 / mcp 1.29, resolved against langchain-core 1.x), at the LangChain layer so every provider gets MCP through one path; the connection test delegates there. **Verified live on a real Docker host with a stdio FastMCP stub**: a created server started deny-all with its token stored-but-redacted, the connection test listed both tools with correct read/write flags, opting into one tool stored exactly that allowlist, a project-scoped server was invisible to another project, and an unreachable server returned 502 with health `unreachable` rather than a 500. **Remaining in 10.1**: agents do not yet load allowlisted MCP tools during a run, so the "every call is logged" half of the done-condition (the per-call audit from an actual tool invocation) is wired for storage but not yet exercised by an agent — that couples with 10.7's write-tool binding safety. **10.2 (Jira adapter) built and verified**: it registers behind the built-in `DefectTracker`, which stays the source of truth; a filed defect is written internally first and always, then a `Mirror` pushes it to the active external tracker and stores where it landed. Verified live against a Jira Cloud REST stub — filing a defect created a Jira issue (project key, issue type, ADF description carrying the internal id and the links back), stored `{provider, key: QA-1801, url}` on `external_ref` and returned it on the defect; **removing the Jira configuration left every defect intact with its `external_ref` kept as history, the list and detail screens working, and a new defect filing to the built-in with no error** — the BE-10.2.4 clean-removal condition. A Jira `Create` failure degrades to an admin alert through the notification service rather than losing the defect (BE-10.6.2). Promotion mirrors too, so a security finding reaches Jira through the same path. **Deferred within 10.2**: the summary is the defect's own title rather than a cheap-tier-written one (the summariser is wired as an optional dependency, currently nil), and status/comment mirroring on a defect status change is implemented in the adapter but not yet triggered from a status update. **10.5 inbound half already in place from BE-0.20**: the generic signed webhook trigger (HMAC over timestamp+body, per-project token, replay guard, tolerance window) registers behind `RunTrigger` and starts a run, which covers 10.5.1/10.5.3/10.5.4 — a CI system POSTs a signed delivery and gets a run; a replay is rejected. **10.6 (degradation surfacing) built and verified**: `GET /integrations` reports one line per adapter across every capability — notifier channels, defect trackers, source providers, run triggers, object store — with state `builtin`, `active`, or `not_configured`, read from each adapter's own `Available()` rather than computed here so it cannot drift. Verified live: builtins marked, `smtp`/`slack`/`jira` `not_configured`, the webhook `active`; configuring Jira flipped it to `active` and clearing it back to `not_configured`. The runtime degradation itself (external failure → built-in + admin alert) is the registries' existing `Attempt`/fan-out path from earlier phases, exercised by the Jira-outage case in 10.2. **10.7 (write confirmation / untrusted-content tool binding) built and verified**: the tool-binding layer in the Python service (`src/mcp/binding.py`) enforces the rule that a write-capable MCP tool is **never** bound to an agent that reads untrusted content — the intersection where prompt injection turns into action — and that any other write tool requires confirmation unless the server is set to `auto_write`. `load_tools` applies the allowlist filter and then this binding, both deny-by-default and independent. Verified by the acceptance test the done-condition names: 17 cases proving every untrusted-content agent (extract, design, codegen, analyse, repo, unittest, uiflow, uispec, probeselect, testdata, perf, dedupe) is denied every write tool even with `auto_write` on, read tools bind freely, and no current agent is trusted for writes. **Not started: 10.3 GitHub/GitLab adapter (OAuth app, PR webhooks, least-privilege token — clone-by-URL for a github/gitlab URL already works through the git source provider), 10.5.2 the outbound result-back (status check / PR comment, which needs 10.3's API client), 10.8 zero-integration re-acceptance (the phase gate — the `/integrations` report already shows a clean all-builtin baseline).** Deferred within done items: the cheap-tier Jira summary and status/comment mirroring on status change (adapter supports both); agents do not yet load MCP tools during a run, so the binding is enforced and tested but not yet exercised end-to-end by a live agent call |
| `BE-11` Maintenance | 7 | ☐ | |
| `BE-X` Handoff | 5 | ☐ | unblocks frontend |

### Frontend

| Phase | Tasks | Status | Notes |
|---|---|---|---|
| `FE-S` Setup | 6 | ☑ | **Started ahead of `BE-X.1` at the owner's request (2026-10-07)**, accepting the §2 cost: screens built before the contract freeze may need rework. Built: S.1. Next.js 16.4 App Router, TypeScript strict, pnpm; route groups `(auth)`, `(setup)`, `(app)`; the API address is one public build-time variable, validated and refused if it carries credentials; the browser calls a relative `/api/*` that Next rewrites to the API, so the session cookie needs no CORS. Verified against `make mock`: `/login` served and `/api/v1/setup/status` answered through the rewrite. CI and `make lint-web` run ESLint, Prettier, `tsc`, Vitest, and `next build`. **S.2 built**: tokens in `web/src/app/globals.css` extend the palette already in `docs/presentation.html` (deep green primary, green-black ink, rust for failure), defined for light and dark together, with every text/fill pair contrast-checked at definition (body text 5:1 or better, input borders 3:1); state colours (`success`, `warning`, `info`, `destructive`, each with a wash) are reserved for state; border-led separation, 6px radius, system sans and mono faces with no font download. shadcn/ui (Radix base) primitives are copied into `src/components/ui`, plus `EmptyState`, `ErrorState` (shows the API message and incident ID), status `Badge` variants, a `next-themes` provider defaulting to system, and a hydration-safe `ThemeSwitch` for FE-0.1. Verified by screenshot in light and dark at 1280px and 390px on a dev-only `/design` page, which returns 404 in production. **S.3 built**: `openapi-typescript` output (it had fallen about 9,000 lines behind the spec) is now part of `make gen`, and CI regenerates it in the stale-code check. `createApiClient` (openapi-fetch) adds an `X-Correlation-ID` per request and throws every failure as one `ApiError { code, message, details, status, incidentId, correlationId }`; a response that is not the envelope becomes `unexpected_response` and a fetch failure `network_error`, an abort stays an abort. Hooks come from `openapi-react-query`, so query keys are `[method, path, init]` built from the request itself. TanStack Query: 30s stale time, retries only on `network_error`, mutations never retried; `unauthenticated` clears the cache and goes to login, `forbidden`, `role_required`, and `not_project_member` go to `/forbidden`, whose copy is chosen from the code and never read from the URL; other 403s stay with the screen. ESLint rejects `fetch` outside `src/lib/api`. 18 Vitest tests. **S.4 built**: `src/proxy.ts` sends a request with no `qavia_session` cookie straight to `/login?next=…` (an optimistic check only); the `(app)` layout asks the API `GET /me` on every request behind a Suspense boundary and redirects on 401, so nothing is trusted from the cookie itself; server calls forward only the session cookie and the correlation ID. `projects/[projectID]/layout.tsx` guards the whole project subtree: unknown is the 404 page, not a member is a no-access state. `hasRole` and `ShowForRole` are presentation only. `next` is followed only for same-site paths, never `//host` or a scheme. **Verified against a real API through the web app's own `/api` rewrite**: no cookie redirected with the path kept; a forged cookie rendered nothing and redirected; an admin saw the app and their project; an unknown project rendered the 404 page (status 200, because the response is already streaming when the guard decides); an invited viewer who is not a member got the no-access state; and after the admin changed that viewer's role, the viewer's very next request was sent to sign in again. 24 Vitest tests. **S.5 built**: `openEventStream` reads SSE over fetch so a reconnect it starts can send `Last-Event-ID`, backs off with jitter, reports "reconnecting" rather than going quiet, and stops on a 4xx; `useSSE` closes it on unmount; `useJob` replays history then follows live, de-duplicates by event id, and invalidates the queries a finished job affects; `useCursorList` is typed to the endpoints that return `{ items, nextCursor }`; `optimistic()` rolls back a refused edit and refetches when settled; `src/lib/format.ts` plus `useFormat()` format in the user's timezone. **Contract gap for the backend**: the SSE `status` frame (`jobId`, `status`, `progress`, `stages`) is not described in `qavia.yaml`, so `useJob` re-reads `GET /jobs/{id}` on it instead of trusting an undescribed shape. **S.6 built**: Vitest with Testing Library and happy-dom for components; MSW with handlers generated from `qavia.yaml` (`src/test/spec-handlers.ts`, via openapi-sampler) so stubs follow the contract; Playwright runs the production build against the pinned Prism mock locally (`make e2e-web`) and in a new `e2e-web` CI job. 49 unit tests, 2 end-to-end tests |
| `FE-0` Shell and foundation | 9 | ☐ | **0.1 built**: sidebar (collapsible to icons, state kept in a cookie) with a project switcher over the paginated project list, role-filtered navigation that lists a screen only once it exists, and a user menu with theme (saved to `preferences.theme`), account, and sign out; breadcrumbs derived from the URL, showing a cached project's name; skip link; `(app)/error.tsx` shows an ApiError's incident ID or a server digest, never a server message; `/` follows `preferences.landing_page`, and since the API has no cross-project run or defect list, "runs" and "defects" land in the last project opened in this browser; `preferences.timezone` drives `useFormat`. Checked by screenshot in both themes against the mock; 4 end-to-end tests |
| `FE-1` AI settings | 5 | ☐ | |
| `FE-2` Generation review | 6 | ☐ | |
| `FE-3` Code browser | 3 | ☐ | |
| `FE-4` Runs | 6 | ☐ | |
| `FE-5` Analysis and defects | 5 | ☐ | |
| `FE-6` Repo and coverage | 3 | ☐ | |
| `FE-7` UI tests | 4 | ☐ | |
| `FE-8` Data and mocks | 3 | ☐ | |
| `FE-9` Perf and security | 3 | ☐ | |
| `FE-10` Integrations | 3 | ☐ | |
| `FE-11` Maintenance | 2 | ☐ | |
| `FE-Q` Quality | 6 | ☐ | |

---

## 11. Final Acceptance

v1 is accepted when every criterion in `requirements.md` §9 passes on one real
Hyscaler project. Mapping to tasks:

| Criterion | Proven by |
|---|---|
| 0. Zero-integration install | `BE-10.8`, `FE-10.3` |
| 1. Setup entirely through the UI | `BE-0.28`, `FE-0.3` |
| 2. Create project, upload spec, submit | `FE-0.5`, `FE-0.6` |
| 3. Close browser, notified within 15 minutes | `BE-0.22`, `BE-0.19`, `FE-0.8` |
| 4. 90% of endpoints have test cases | `BE-2.14` |
| 5. QA engineer confirms cases are usable | `BE-2.14` (M2) |
| 6. Exported suite passes against staging | `BE-3.7` |
| 7. In-platform run matches local run | `BE-4.15` (M3) |
| 8. 6 of 10 root causes correct | `BE-5.10` (M4) |
| 9. Spec change updates exactly the affected cases | `BE-11.5` |
| 10. No secret in any log, response, or notification | `BE-0.2`, `BE-0.29` standing test |

Criterion 0 is re-run **after** `BE-10`, with every integration deleted. If it
fails, the capability registry has been bypassed somewhere and that is the bug.
