# Qavia — Competitive Analysis

Who else does what Qavia does, how much of it they do, and what that means for the
build order in `plan.md`.

---

## 1. How to read this

Qavia is scoped as an internal Hyscaler platform (`requirements.md` §2.1), not a
product for sale. So "competitor" means two different things, and both matter:

| Framing | Question it answers | Who wins it today |
|---|---|---|
| **Build vs buy** | Could Hyscaler buy this instead of building 12 phases? | Katalon, KaneAI, mabl, QA Wolf |
| **Market position** | If Qavia is ever commercialised, who is already there? | Tricentis, LambdaTest, mabl, testRigor |

The ranking below is by **scope overlap with Qavia**, not by company size. A large
vendor that only does UI testing overlaps less than a small one that does spec to
executed test.

**Qavia's declared surface**, for comparison against each row:

```
spec ingest (OpenAPI, Postman, repo, requirement doc)
  -> test case generation with requirement traceability
  -> code generation (Playwright, Jest/Vitest, Supertest, k6, security payloads)
  -> execution in isolated containers (gVisor, one-shot, default-deny egress)
  -> failure analysis with root cause and suggested fix
  -> defect filed in built-in tracker
  -> drift detection when the spec changes
```

No single vendor below covers that whole line. Several cover one segment of it far
better than Phase 4 Qavia will.

---

## 2. Top 10

Ranked by overlap. "Overlap" = share of Qavia's surface the tool already ships.

| # | Competitor | Category | Overlap | Threat to the build case |
|---|---|---|---|---|
| 1 | **LambdaTest KaneAI** | Full-lifecycle AI agent | High | Highest. Same arc, already shipping, $15/mo entry |
| 2 | **Tricentis** (Tosca, Testim, agents) | Enterprise incumbent | High | Highest at enterprise scale, weakest on price |
| 3 | **Katalon** | All-in-one AI suite | High | Free tier makes "just use this" the default counter-argument |
| 4 | **mabl** | Agentic SaaS E2E + API | Med-High | Strong on execution and self-healing, weak on spec ingest |
| 5 | **QA Wolf** | Managed QA service | Med-High | Replaces the outcome, not the tool. Hands back plain Playwright |
| 6 | **testRigor** | NL test authoring | Medium | Owns "requirement in plain English becomes a test" |
| 7 | **Testsigma** | Codeless NL platform | Medium | Broad type coverage, cloud grid included |
| 8 | **Momentic** | Agent-native E2E | Medium | Dev-facing, free entry, fast-moving |
| 9 | **Qodo** (ex CodiumAI) | Repo-native unit tests + PR review | Medium | Directly pre-empts Qavia Phase 6 |
| 10 | **Keploy** | API test generation, open source | Medium | Directly pre-empts Qavia Phase 2/3 API path, and it is free |

---

## 3. Competitor detail

### 1. LambdaTest KaneAI

The closest thing to Qavia that exists. Requirement in natural language, test
authored, executed at cloud scale, code handed back in your framework, self-healing,
AI root cause analysis, CI hooks. One platform, same story.

- **Pricing:** from ~$15/month, billed per agent (one agent ≈ 500 agentic sessions
  per month), not per seat.
- **Where Qavia differs:** no unit test generation, no k6 performance path, no
  security payload generation, no built-in defect tracker, no local-model option.
  Client code and specs leave the building.
- **Read:** this is the row to put in front of anyone who asks "why are we building
  this". The honest answer is data residency plus test-type breadth, not novelty.

### 2. Tricentis (Tosca, Testim, Tricentis agents)

Enterprise incumbent that shipped agentic AI test creation in 2025-26. Deepest
coverage for SAP and packaged enterprise apps, regulated industries, model-based
testing.

- **Pricing:** sales-led, enterprise contract sizes.
- **Where Qavia differs:** cost and setup weight. Tosca is a programme, not a tool.
- **Read:** not a build-vs-buy threat for an internal Hyscaler tool. It is the
  ceiling if Qavia ever goes commercial upmarket.

### 3. Katalon

Feature-complete across web, mobile, API and desktop, with AI-assisted generation,
self-healing and visual testing built in. Has a free tier.

- **Where Qavia differs:** Katalon does not read a requirement document or a
  repository and derive a test plan with requirement traceability. It also keeps
  tests in its own format.
- **Read:** the most likely internal counter-proposal, precisely because it is free
  to start. Any Qavia pitch needs an answer to "we could pilot Katalon on Monday".

### 4. mabl

Agentic SaaS for E2E plus API testing. Credit-metered cloud runs, self-healing,
strong CI integration and reporting.

- **Pricing:** starter around $60/month, credit-metered, enterprise on quote.
- **Where Qavia differs:** tests live inside mabl. No repo ingest, no unit tests, no
  performance or security generation, no on-prem model.
- **Read:** the benchmark for execution reliability and reporting polish. Phase 4 and
  Phase 7 should be measured against mabl, not against "does it run".

### 5. QA Wolf

Sells an outcome, not a tool: a managed service that builds and maintains your test
suite and hands you standard Playwright. Coverage guarantees, human plus AI.

- **Pricing:** managed-services contract economics, not SaaS.
- **Where Qavia differs:** Qavia is the same promise as software instead of a
  vendor's people, and it keeps the code and the specs in-house.
- **Read:** the real substitute for the business problem Qavia solves. If leadership
  frames the problem as "we do not have enough QA capacity", QA Wolf answers it
  without a 12-phase build.

### 6. testRigor

Tests written as plain English statements, generated from requirements, executed with
very low maintenance. Owns the "no locators, no code" position.

- **Where Qavia differs:** proprietary syntax, tests locked in the platform, no unit
  or performance testing, no repository understanding.
- **Read:** direct competitor for Phase 2 (requirement to test case). Worth reading
  their generation prompts and output shape before finalising Qavia's schema.

### 7. Testsigma

Codeless natural-language authoring across web, mobile web, iOS, Android and REST
API, with a large browser/device grid included.

- **Pricing:** unpublished, sales conversation for both tiers.
- **Where Qavia differs:** same pattern as testRigor and mabl, tests stay inside the
  vendor. No code generation into your repo, no unit tests.

### 8. Momentic

Agent-native E2E platform. Low-code editor, plain-English test steps, intent-based
self-healing locators, an autonomous agent that generates tests from the app itself.
Free self-serve entry, demo-led sales above that.

- **Where Qavia differs:** Momentic explores a running application. Qavia reads a
  spec or a repository. Different input, similar output.
- **Read:** the strongest signal for Phase 7. An agent that crawls the running app
  finds flows a spec never describes. Worth considering as a Phase 7 input mode.

### 9. Qodo (formerly CodiumAI)

Repo-native. The only major tool combining automated PR review with unit test
generation. Multi-agent review architecture as of Qodo 2.0 (February 2026), broad
language coverage, transparent pricing.

- **Adjacent:** **Diffblue Cover** does the same for Java only, using symbolic
  analysis rather than an LLM, producing deterministic regression tests. Higher
  quality within Java, no PR review.
- **Where Qavia differs:** Qavia's Phase 6 (repo ingest, unit tests) is Qodo's core
  product, already mature and already in developers' IDEs.
- **Read:** the case for building Phase 6 is weak on its own merits. It is only
  justified as part of one traceable pipeline with the other test types.

### 10. Keploy

Open source. Generates API test suites from OpenAPI specs, Postman collections, curl
commands or recorded traffic. Container-friendly, CI-native, claims high coverage
with no flake.

- **Adjacent:** **Schemathesis** (property-based fuzzing from an OpenAPI/GraphQL
  schema), **Akto** (API security testing), **Postman Postbot** (in-editor
  assistance, not a pipeline).
- **Where Qavia differs:** Keploy is API-only and has no test case layer, no
  requirement traceability, no UI or unit or performance testing.
- **Read:** the cheapest possible substitute for one slice of Phase 2/3. If Qavia's
  generated API tests are not clearly better than Keploy's, that slice is not paying
  for itself.

---

## 4. Adjacent players worth tracking

Not top-10 by overlap, but each owns something Qavia claims.

| Tool | Owns | Why it matters |
|---|---|---|
| **Applitools Autonomous** | Visual and autonomous UI validation | Visual assertion quality Qavia will not match in Phase 7 |
| **Functionize, ACCELQ, Virtuoso QA** | Enterprise codeless suites | The bracket Qavia sits in on paper, all sales-led |
| **Meticulous** | Record real traffic, auto-generate regression tests | Zero-authoring model, free entry |
| **Rainforest QA, Autify** | Managed and low-code E2E | Same substitute logic as QA Wolf, smaller |
| **TestCollab QA Copilot, TestRail, TestFiesta** | Test management with AI generation | Owns requirement traceability, the thing Qavia builds in Phase 0 |
| **BrowserStack, Sauce Labs** | Grid plus AI features | Execution infrastructure Qavia is building itself |
| **Shiplight, Bug0, Qodex, TestStory** | 2026 agent-native startups | The cohort Qavia is actually in. Fast-moving, thin |
| **Claude Code, Cursor, Copilot agents** | Writing Playwright and Jest tests directly in the repo | The genuine zero-cost alternative. See §6 |

---

## 5. Feature matrix

Rough coverage against Qavia's declared surface. `Y` = shipping, `P` = partial,
`-` = absent.

| | Spec ingest | Test case layer | Code you own | Exec | Unit | API | UI | Perf | Sec | RCA | Defects | On-prem model |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **Qavia (planned)** | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| KaneAI | P | Y | Y | Y | - | P | Y | P | - | Y | - | - |
| Tricentis | Y | Y | P | Y | - | Y | Y | Y | P | Y | Y | P |
| Katalon | P | Y | P | Y | - | Y | Y | P | - | P | P | - |
| mabl | P | P | - | Y | - | Y | Y | P | - | Y | - | - |
| QA Wolf | P | Y | Y | Y | - | Y | Y | - | - | P | - | - |
| testRigor | Y | Y | - | Y | - | P | Y | - | - | P | - | - |
| Testsigma | P | Y | - | Y | - | Y | Y | - | - | P | - | - |
| Momentic | - | P | P | Y | - | P | Y | - | - | P | - | - |
| Qodo | P | - | Y | P | Y | P | - | - | - | P | - | P |
| Keploy | Y | - | Y | Y | P | Y | - | - | - | - | - | Y |

Read the Qavia row as ambition, not status. Today it is a plan document.

---

## 6. What the landscape says about the plan

Five conclusions that should change how Phases 2 to 7 are sequenced.

**1. Breadth is the only defensible claim.** Every individual capability in Qavia
exists in a more mature product. Nine test types under one traceable pipeline, with
one requirement ID linking a spec line to a failed run to a filed defect, is the
thing no competitor sells. That argues for finishing the thin end-to-end line early
rather than making any one test type excellent.

**2. Data residency is the second claim, and it is the stronger one commercially.**
Principle 4 (tier not provider) plus a local provider means a client that cannot send
code to OpenAI or Anthropic gets a working platform. None of the top 10 offer that.
This makes Open Item 6 (local models at launch or later) more important than it looks
in `README.md`. It is not a nice-to-have, it is half the differentiator.

**3. Phase 6 and the API slice of Phase 3 are the weakest build cases.** Qodo and
Keploy already do those, well, cheaply or free. Neither should be built before the
end-to-end line works, and both should be benchmarked against the incumbent before
being called done.

**4. M2 should be measured against a competitor, not against nothing.** The current
go/no-go is "a real QA engineer says the generated test cases are usable"
(`README.md`, M2). Strengthen it: run the same spec through testRigor or KaneAI's
trial and have the reviewer compare blind. "Usable" is a low bar. "Better than the
$15/month tool" is the real one.

**5. The uncomfortable competitor is not in the table.** A QA engineer with Claude
Code and a repo can produce Playwright and Jest tests today, no platform, no build.
Qavia beats that on orchestration, repeatability, sandboxed execution, traceability
and the fact that it runs unattended in the background. It does not beat it on raw
generation quality. Any pitch that ignores this gets asked about it.

---

## 7. Open questions this raises

| # | Question | Needed by |
|---|---|---|
| 1 | Is a free Katalon or Keploy pilot an acceptable answer to the problem Qavia solves? Decide before Phase 2 sinks effort | Phase 2 |
| 2 | Is Qavia ever intended for external clients? Changes whether multi-tenancy stays out of scope | Phase 0 |
| 3 | Should Phase 7 add Momentic-style live-app crawling as a second input mode alongside specs? | Phase 7 |
| 4 | Which competitor is the blind-comparison baseline at M2? | Phase 2 |

---

## 8. Feature-by-feature: every module against the market

All 145 rows of `features.md`, scored against what the top 10 and the adjacent tools
in §4 actually ship.

**Verdicts**

| Verdict | Meaning |
|---|---|
| `UNIQUE` | No competitor ships this. Real differentiation |
| `AHEAD` | Others do it, Qavia's design is stricter, safer, or more traceable |
| `PARITY` | Matched by mainstream tools. Neither a win nor a loss |
| `STAKES` | Competitors get it free by being multi-tenant SaaS. Cost to us, no advantage |
| `BEHIND` | A competitor does it materially better, today, for less effort |

---

### F1 — Platform and Access

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-1.1 | Local login, Argon2id, admin-invited | BEHIND | Every rival ships SSO/SAML/SCIM. v1 has none (deferred). Fine internally, blocking commercially |
| F-1.2 | Login rate limit, lockout | STAKES | Universal |
| F-1.3 | Four roles, per-route | PARITY | Katalon and Tricentis have finer-grained enterprise RBAC |
| F-1.4 | Project CRUD, membership | STAKES | Universal |
| F-1.5 | Settings registry | UNIQUE | No market equivalent because no rival needs it. Engineering win, zero customer-visible value |
| F-1.6 | Settings UI from registry | UNIQUE | Same. Counts as velocity, not differentiation |
| F-1.7 | Scoped resolution user → project → global | AHEAD | Rare. Matters because it lets one project pin a local model (F-16.12) |
| F-1.8 | AES-256-GCM secret storage | STAKES | SaaS rivals use a managed vault and never expose the problem |
| F-1.9 | Settings audit trail | PARITY | Enterprise tiers of Tricentis, Katalon |
| F-1.10 | First-run wizard | STAKES | Meaningless to SaaS. Pure self-host tax |
| F-1.11 | Capability interface registry | UNIQUE | The mechanism behind "every integration optional". No rival attempts this, they just require their cloud |

**Module verdict:** 11 features, ~9 of them are the price of self-hosting. F-1.7 and
F-1.11 are the only two that buy anything a competitor cannot match.

---

### F2 — Job Pipeline

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-2.1 | Chained queue ingest → generate → execute → analyse | STAKES | Every SaaS has this invisibly |
| F-2.2 | Job status, progress, stages | STAKES | Universal |
| F-2.3 | SSE live event log | PARITY | mabl, Katalon, KaneAI all stream run progress |
| F-2.4 | Idempotency keys | AHEAD | Rarely explicit. Directly protects the AI spend line |
| F-2.5 | Retry with backoff | STAKES | Universal |
| F-2.6 | Cancel a run | PARITY | Universal |
| F-2.7 | Concurrency limit from settings | PARITY | Rivals sell concurrency as a pricing lever instead |
| F-2.8 | Internal scheduler | PARITY | mabl, Katalon, Testsigma all schedule runs |
| F-2.9 | Generic inbound webhook | PARITY | Universal |

**Module verdict:** entirely table stakes. Necessary, not differentiating. Do not
over-invest past what Phase 0 needs.

---

### F3 — Input and Ingestion

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-3.1 | OpenAPI upload, `$ref` resolved | PARITY | Keploy, Schemathesis, Postman, testRigor |
| F-3.2 | Postman collection import | PARITY | Keploy ingests the same |
| F-3.3 | Requirement text pasted | PARITY | testRigor, KaneAI, TestCollab QA Copilot |
| F-3.4 | Source archive upload | PARITY | Qodo, Diffblue |
| F-3.5 | Clone by URL with token | PARITY | Qodo, QA Wolf |
| F-3.6 | GitHub/GitLab OAuth, repo browser | PARITY | Universal among repo-aware tools |
| F-3.7 | SQL dump for schema-aware data | AHEAD | Rare in QA tools. Tonic.ai and GenRocket do it as a separate product |
| F-3.8 | Read-only Postgres via MCP | UNIQUE | No QA vendor exposes a live DB inspection channel |
| F-3.9 | PDF / DOCX ingestion | BEHIND | Deferred post-v1, but Tricentis, Katalon and TestCollab already read requirement documents. This is the input a non-technical stakeholder actually has |
| F-3.10 | SHA-256 dedupe, re-upload skips regeneration | UNIQUE | Pure cost control. Rivals bill per run and have no incentive |
| F-3.11 | Size cap, MIME allowlist, archive-bomb reject | STAKES | Universal |
| F-3.12 | Test-type selection, unimplemented types disabled not hidden | PARITY | Honest UX, not a moat |
| F-3.13 | Generation works without a target URL | AHEAD | Momentic, mabl, Applitools Autonomous all need a running app before they do anything |

---

### F4 — Understanding

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-4.1 | Requirement extraction to features, rules, auth, edge cases | PARITY | testRigor, KaneAI, Tricentis agents do this. It is the price of entry, not the product |
| F-4.2 | Every item traces to artifact and source location | AHEAD | Rare. Most tools lose the link between the sentence and the test |
| F-4.3 | Review screen before generation | AHEAD | Most rivals auto-generate and let you delete afterwards. A pre-gate is better for trust and for spend |
| F-4.4 | Repository comprehension, uncovered paths | BEHIND | Qodo, Cursor, Claude Code and Sourcegraph are years ahead. Do not build a code-understanding engine, use one |

---

### F5 — Test Case Management

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-5.1 | Case design per requirement, boundaries, negatives, auth states | PARITY | Every AI QA tool claims this |
| F-5.2 | Schema-locked output, retry with the error fed back | AHEAD | Engineering discipline most vendors do not document |
| F-5.3 | Deterministic fingerprint per case | UNIQUE | Nobody else has a stable identity for a generated case. Everything in F15 depends on it |
| F-5.4 | Semantic dedupe for near-misses | AHEAD | Rivals ship duplicates and let you prune |
| F-5.5 | Case table, inline edit, bulk approve | PARITY | TestRail, TestCollab and Xray do this better, it is their whole product |
| F-5.6 | Requirement traceability, uncovered requirements highlighted | PARITY | Parity with test management tools, AHEAD of every pure AI tool in the top 10 |
| F-5.7 | Provider-specific pre-submit cost estimate | UNIQUE | Only possible because you own the keys. No SaaS shows you token cost |

---

### F6 — Code Generation

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-6.1 | API tests, Supertest | PARITY | Keploy generates API suites and also records real traffic, which is stronger |
| F-6.2 | API tests, Postman collection | PARITY | Postman and Keploy native |
| F-6.3 | Unit tests, Jest/Vitest/pytest | BEHIND | Qodo covers more languages with IDE and PR integration. Diffblue beats both inside Java with symbolic, deterministic generation |
| F-6.4 | UI tests, Playwright | PARITY | QA Wolf, KaneAI and Momentic all hand back Playwright |
| F-6.5 | Performance scripts, k6 | AHEAD | Among AI QA tools, almost nobody generates load scripts. BEHIND dedicated perf tooling (Grafana k6, NeoLoad, Gatling) |
| F-6.6 | Security tests, AI picks targets, curated payload library | AHEAD | The split is the right design. Depth is BEHIND ZAP, Akto, StackHawk |
| F-6.7 | Framework auto-detection | PARITY | Standard |
| F-6.8 | Static validation: parses, imports resolve, no placeholder assertions | AHEAD | The single most underrated feature in the catalog. Most AI tools ship code that does not compile and call it output |
| F-6.9 | File tree with syntax highlighting | PARITY | Standard |
| F-6.10 | Export per file or as zip | PARITY | QA Wolf and KaneAI hand over code too. Note mabl, testRigor, Testsigma do not, which is a real advantage over them |
| F-6.11 | Each file records the case IDs it covers | UNIQUE | Nobody links generated code back to the case back to the requirement |
| F-6.12 | Selector policy enforced in code, positional CSS rejected | AHEAD | Better than prompt-only guidance. But see §9.1: rivals answer the same problem at runtime with self-healing, which is strictly more powerful |

---

### F7 — Execution

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-7.1 | gVisor or Firecracker per run | UNIQUE | No QA vendor exposes the sandbox runtime. Infra vendors (E2B, Modal, Daytona) do, and are the reuse option |
| F-7.2 | One-shot containers | STAKES | SaaS does this internally |
| F-7.3 | CPU, memory, process, wall-clock, disk limits | STAKES | Internal to every SaaS |
| F-7.4 | Default-deny egress | AHEAD | Genuinely stronger than any SaaS runner you cannot inspect |
| F-7.5 | Cloud metadata endpoint blocked | AHEAD | Correct and rarely stated |
| F-7.6 | Non-root, read-only rootfs | STAKES | Standard container hygiene |
| F-7.7 | Per-project host allowlist enforced before enqueue | UNIQUE | No rival has a server-side concept of "this project may only touch these hosts" |
| F-7.8 | SSRF guard | AHEAD | Rivals assume you own the target |
| F-7.9 | Live log tail | PARITY | Universal |
| F-7.10 | Logs, screenshots, videos, traces | PARITY | Playwright trace is standard. mabl and Katalon have richer artifact viewers |
| F-7.11 | Flake detection by re-run | PARITY | mabl, Katalon. BEHIND Trunk Flaky Tests and Datadog CI Visibility, which do this as a product |
| F-7.12 | Flake quarantine | BEHIND | Same as above. Buy the signal, do not build the analytics |
| F-7.13 | Runner images pinned by digest | AHEAD | Supply-chain discipline rivals do not expose |
| F-7.14 | Coverage tool inside the runner | PARITY | Standard |
| F-7.15 | Bundled Playwright container | BEHIND | One container versus BrowserStack, LambdaTest and Sauce grids. See §9.3 |
| F-7.16 | Playwright MCP as an alternative driver | UNIQUE | Swappable browser driver behind one interface. Nobody else offers a choice |

---

### F8 — UI Flow Discovery

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-8.1 | Agent drives the real browser, discovers routes and flows | PARITY | Momentic, Applitools Autonomous and open-source Skyvern and browser-use already do this. Meticulous goes further by recording real user traffic |
| F-8.2 | Auth flow handled during discovery | PARITY | Table stakes for anything that crawls |
| F-8.3 | Flow graph as reviewable structured output | AHEAD | The review gate is the differentiator, not the crawl |
| F-8.4 | Screenshot understanding for layout checks | BEHIND | Applitools has a decade of visual AI, baselines, and diff root-causing. A vision call on a screenshot is not the same product. See §9.2 |

---

### F9 — Failure Analysis and Defects

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-9.1 | Root cause, probable cause, suggested fix | PARITY | mabl auto-triage, KaneAI RCA, Testsigma all ship this |
| F-9.2 | Mandatory evidence or the analysis is rejected and retried | AHEAD | The discipline no vendor advertises because it makes the demo shorter. Keep it |
| F-9.3 | Source file and git blame when a repo is connected | AHEAD | Rare, because most rivals never see your repository |
| F-9.4 | Stability score from measurement, never a model self-report | AHEAD | Rivals do show fabricated confidence percentages. This is a correct call |
| F-9.5 | Analysis panel with evidence in context | PARITY | Standard |
| F-9.6 | Thumbs up/down for prompt iteration | PARITY | Standard |
| F-9.7 | Internal defect tracker | BEHIND | Jira and Linear exist. Principle 5 forces this, so keep it minimal: severity, status, assignee, comments, nothing more |
| F-9.8 | Promote failure to defect in one action | PARITY | mabl and Katalon file to Jira in one click |
| F-9.9 | Defect traces to run, case, requirement, analysis, artifacts | AHEAD | The full chain is rare. This is the reporting story |
| F-9.10 | Duplicate linking on same case, same root cause | AHEAD | Rivals flood the tracker |
| F-9.11 | Push to Jira, key stored back | PARITY | Universal |

---

### F10 — Test Data and Mocking

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-10.1 | Seeded faker from schema | PARITY | Free libraries. Correct choice not to use AI here |
| F-10.2 | AI only for semantically tricky fields | AHEAD | Narrow and cheap. Good design |
| F-10.3 | Boundary and invalid data sets | PARITY | Schemathesis does this by property-based generation, arguably better |
| F-10.4 | Card numbers only from published test ranges | AHEAD | Compliance discipline. Cheap and correct |
| F-10.5 | CSV, JSON, SQL export | PARITY | Universal |
| F-10.6 | Mock server from spec | BEHIND | Prism, WireMock, Microcks and Mockoon are mature, free, and generate from OpenAPI today |
| F-10.7 | Fault injection: delay, 500s, timeouts | PARITY | WireMock and Toxiproxy |
| F-10.8 | Mock container per project with a live URL | PARITY | Microcks does exactly this |

**Module verdict:** the most commodity module in the catalog. Recommend wrapping
Prism or Microcks behind the capability interface rather than building F-10.6 to
F-10.8. See §10.

---

### F11 — Performance and Security Testing

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-11.1 | k6 generation from endpoints plus a load profile | AHEAD | Few AI QA tools generate load scripts at all |
| F-11.2 | Latency and throughput charts | BEHIND | Grafana renders k6 output for free and better |
| F-11.3 | SQLi, XSS, CSRF, JWT, IDOR, rate limit, broken auth | BEHIND | OWASP ZAP, Akto, StackHawk and Burp are dedicated DAST products with maintained payload corpora. A curated library will fall behind within a year |
| F-11.4 | Findings with severity, evidence, repro steps | PARITY | Standard DAST output |
| F-11.5 | Both types off by default, enabled by Lead or Admin | AHEAD | Governance rivals skip because they assume you own the target |
| F-11.6 | Explicit host confirmation on first run | AHEAD | Same |

---

### F12 — Reporting

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-12.1 | Project dashboard | PARITY | Universal |
| F-12.2 | Requirement coverage | AHEAD | Ahead of the AI-native tools. Parity with Xray and TestRail |
| F-12.3 | Line and branch code coverage | PARITY | Standard tooling. Codecov and SonarQube do the reporting better |
| F-12.4 | Recent failures linked to analysis | PARITY | Universal |
| F-12.5 | HTML and PDF export | PARITY | Universal |
| F-12.6 | Run history and trend | BEHIND | mabl and Datadog CI Visibility have far deeper trend analytics |
| F-12.7 | AI spend by provider, project, agent | UNIQUE | Structurally impossible for a SaaS rival to offer, since the tokens are theirs |

---

### F13 — Optional External Integrations

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-13.1 | Jira sync | PARITY | Universal, and rivals sync bidirectionally with more field mapping |
| F-13.2 | GitHub/GitLab, PR diff read, PR creation with a fix | PARITY | Qodo does PR review and fix suggestion as its core product |
| F-13.3 | Slack summaries | PARITY | Universal |
| F-13.4 | GitHub Actions / Jenkins triggers | PARITY | Universal |
| F-13.5 | SMTP email | STAKES | Universal |
| F-13.6 | MinIO / S3 | STAKES | Internal to SaaS |
| F-13.7 | MCP servers with deny-all allowlist, write confirmation, least privilege, injection guardrails, full audit | AHEAD | Most vendors have bolted on MCP with no written guardrail policy. This section is genuinely better thought out than the market |

**Module verdict:** integration breadth is a losing race. Katalon and Tricentis have
hundreds. Compete on the guardrails (F-13.7), not the count.

---

### F14 — Notifications

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-14.1 | In-app notification centre | STAKES | Universal |
| F-14.2 | Notify on complete, fail, needs input | STAKES | Universal |
| F-14.3 | Per-user, per-channel toggles | STAKES | Universal |
| F-14.4 | Deep link in every notification | PARITY | Universal |
| F-14.5 | Unconfigured channel reads "Not configured", never an error | AHEAD | Small, and the clearest expression of Principle 2 |

---

### F15 — Maintenance and Drift

The strongest module in the catalog. Every verdict here is UNIQUE or AHEAD.

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-15.1 | Artifact versioning by SHA-256, re-ingest on change | AHEAD | Rivals version tests, not the source of truth the tests came from |
| F-15.2 | Deterministic structural spec diff, not AI | UNIQUE | No competitor diffs the specification itself |
| F-15.3 | Change classification: added, removed, signature, semantics | UNIQUE | Follows from F-15.2. Nobody else can compute it |
| F-15.4 | Per-case decision: regenerate, stale, delete | UNIQUE | The actual product. Everything upstream exists to make this possible |
| F-15.5 | User approves the diff before it applies | AHEAD | Self-healing rivals apply changes silently, which is exactly how a suite quietly stops asserting anything |
| F-15.6 | `superseded_by` history, restore a wrongly deleted case | UNIQUE | Nobody keeps test-case lineage |
| F-15.7 | Scheduled drift check with notification | AHEAD | Rivals detect broken selectors, not changed requirements |

**How rivals answer the same problem:** runtime self-healing (mabl, testRigor,
Testsigma, Katalon, KaneAI, Momentic). Self-healing fixes a locator that moved. It
cannot notice that an endpoint gained a required field and three test cases are now
asserting the wrong thing. Those are different problems and Qavia owns the second
one. It currently answers neither the first (see §9.1).

---

### F16 — AI Configuration

The second moat. Ten of thirteen have no market equivalent, because a SaaS rival
sells you their model choice and cannot expose this even if it wanted to.

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-16.1 | Seven provider kinds | UNIQUE | Rivals have one, chosen by them |
| F-16.2 | Add a provider from the UI, no deploy | UNIQUE | Not a category any competitor has |
| F-16.3 | Abstract tiers reasoning, code, cheap, vision | UNIQUE | Internal architecture with a real user-facing consequence: model swap is a dropdown |
| F-16.4 | Per-model capability matrix | UNIQUE | Nobody exposes it |
| F-16.5 | Connection probe that overwrites declared capabilities | AHEAD | Correct: trust the probe, not the docs |
| F-16.6 | Three prompt-caching strategies per provider | UNIQUE | The single most technically distinctive feature in the plan |
| F-16.7 | Structured output normalized across providers | AHEAD | Solved elsewhere by libraries, but required here |
| F-16.8 | Cross-provider fallback, records which served each call | AHEAD | Uptime story no single-provider rival can tell |
| F-16.9 | Per-call accounting: tokens, cache reads, cost, latency | UNIQUE | Rivals show run counts, never token economics |
| F-16.10 | Spend ceiling with block or warn | UNIQUE | Rivals cap you by plan tier instead |
| F-16.11 | Editable price table, no deploy | UNIQUE | Operational, and nobody needs it but us |
| F-16.12 | Per-project provider pinning and data residency | UNIQUE | **The commercial differentiator.** A client that cannot send code to a US API still gets a working platform. No top-10 competitor can offer this at any price |
| F-16.13 | `external_ai_approved` flag, admin-set, audited, enforced server-side | UNIQUE | The enforcement, not the flag, is the point |

---

### F17 — Audit and Governance

| ID | Feature | Verdict | Market note |
|---|---|---|---|
| F-17.1 | Audit log for login, settings, runs, allowlists, approvals, secrets | PARITY | Enterprise tiers of Tricentis and Katalon |
| F-17.2 | Runner command log by run ID | AHEAD | You cannot get this from a SaaS runner |
| F-17.3 | MCP call log with redacted arguments | UNIQUE | No vendor logs agent tool calls this way yet |
| F-17.4 | Secrets never in logs, responses, errors, notifications | STAKES | Universal requirement |
| F-17.5 | Configurable artifact retention | PARITY | Universal |

---

### Scorecard

| Verdict | Count | Share |
|---|---|---|
| UNIQUE | 27 | 19% |
| AHEAD | 37 | 26% |
| PARITY | 52 | 36% |
| STAKES | 17 | 12% |
| BEHIND | 12 | 8% |

Where the UNIQUE features cluster:

| Module | UNIQUE | Reading |
|---|---|---|
| **F16 AI configuration** | 10 | The moat. Multi-provider plus data residency |
| **F15 Maintenance and drift** | 4 | The second moat. Spec-diff-driven test lifecycle |
| **F7 Execution** | 3 | Sandbox and allowlisting, real but expensive |
| **F1 Platform** | 3 | Internal mechanisms, no customer-visible value |
| **F3, F5, F6, F12, F17** | 7 | Traceability and cost visibility, scattered |

**Blunt reading:** 48% of the catalog (PARITY plus STAKES) is work that buys no
advantage over a tool that already exists. That is normal for a platform, but it
means the sequencing must protect the 19% that does. F15 and F16 are the answer to
"why build this", and F16 ships in Phase 1 while F15 ships in Phase 11, dead last.

---

## 9. What the market has that the catalog does not

Six absences. The first three are the ones that will be asked about in a demo.

### 9.1 Runtime self-healing (missing entirely)

mabl, testRigor, Testsigma, Katalon, KaneAI and Momentic all repair a test when a
locator moves. Qavia's answer is F-6.12, a selector policy applied at generation
time, plus F-7.11 flake detection after the fact. Neither repairs anything.

Consequence: a UI change breaks the suite and someone regenerates. That is the exact
maintenance burden rivals advertise having solved.

**Recommendation:** add intent-based locator resolution to Phase 7 scope, or state
explicitly in `requirements.md` that self-healing is out of scope and accept the
comparison. Do not leave it unaddressed.

### 9.2 Visual regression with baselines (missing)

F-8.4 is a vision call on a screenshot. Applitools, Percy, mabl and Katalon have
baseline capture, diff engines, ignore regions and approval workflows. Layout
regressions are one of the top reasons teams buy a UI testing tool.

**Recommendation:** either wrap an open-source differ behind a capability interface,
or add it to the deferred list with a reason. It is currently neither.

### 9.3 Cross-browser and device coverage (single container)

F-7.15 is one bundled Playwright container. Testsigma advertises 800+ browser/OS
combinations and 2,000+ real devices. Even Playwright's own three engines are not
stated as a matrix in the catalog.

**Recommendation:** Playwright already gives Chromium, Firefox and WebKit for near
zero extra work. Make the browser matrix an explicit per-project setting in Phase 7.
Real devices stay out of scope, and that is defensible.

### 9.4 Test impact selection (missing, and cheap)

Launchable, Datadog and Tricentis run only the tests a diff can affect. Qavia has
drift detection (F15) but no "this PR touched `LoginService`, run these 14 tests".

**Recommendation:** high value, low cost once F-6.11 (file records case IDs) and
F-4.4 (repo map) exist. Worth adding to Phase 6.

### 9.5 Test management depth (partial)

F5 has cases, editing and approval. It has no test plans, no run cycles, no manual
test execution, no sign-off. TestRail, Xray and TestCollab are entire products here.

**Recommendation:** correct to leave out. Note it so nobody mistakes Qavia for a
test management replacement.

### 9.6 Enterprise access plumbing (deferred)

No SSO, SAML or SCIM in v1. Mobile (Appium), contract testing (Pact) and
accessibility all deferred. Every top-10 rival has at least three of those.

**Recommendation:** fine for an internal tool. All six become blocking the day
question 2 in §7 is answered "yes, external clients".

---

## 10. Build, buy, or wrap

Concrete calls, derived from the BEHIND rows.

| Qavia feature | Recommendation | Instead |
|---|---|---|
| F-10.6 to F-10.8 mock server | **Wrap** | Prism or Microcks behind the capability interface. Saves most of Phase 8 |
| F-11.3 security probes | **Wrap** | OWASP ZAP or Nuclei in the runner. Keep F-6.6's targeting logic, drop the payload library |
| F-11.2 perf charts | **Wrap** | k6 already emits Prometheus and JSON. Render, do not model |
| F-7.11, F-7.12 flake | **Buy the signal** | Re-run detection is fine. Do not build quarantine analytics |
| F-6.3 unit tests | **Defer or wrap** | Qodo and Diffblue are ahead. Only justified as part of one traceable pipeline |
| F-4.4 repo comprehension | **Wrap** | Tree-sitter plus a code-tier model. Do not build a code intelligence engine |
| F-9.7 internal tracker | **Build minimal** | Principle 5 requires it. Six fields, no workflow engine, no board |
| F-12.3 code coverage | **Wrap** | The language's own tool, already the plan. Do not add a coverage UI |
| **F15 drift** | **Build** | Nobody else has it. This is the product |
| **F16 AI config** | **Build** | Nobody else can have it. This is the business case |
| **F-5.3, F-6.11, F-9.9 traceability** | **Build** | Cheap, in Phase 0, and what makes F15 possible |

---

## 11. What this changes in the plan

Four sequencing consequences on top of the five in §6.

**1. F15 is last and should not be.** Phase 11 holds four of the 27 UNIQUE features
and the entire "keeping tests correct" story that `features.md` itself calls the
differentiating module. A thin F15 (F-15.1, F-15.2, F-15.3 only, deterministic and
cheap, no AI) is buildable right after Phase 2 because it needs nothing but
`artifacts.sha256` and a diff. Cheaper still since the backend moved to Go:
`pb33f/libopenapi`, already the spec parser, ships a structural diff, so the two
hardest deterministic pieces arrive as a library rather than as a build
(`tech-stack.md` §9). Pulling that forward turns the demo from "it generates
tests" into "it noticed the spec changed", which is the part nobody else does.

**2. F16 is correctly early and is the business case.** Phase 1 already holds ten
UNIQUE features. F-16.12 and F-16.13 are what let Hyscaler take client work under
NDA. Open Item 6 (local models at launch or later) should be closed as "at launch".

**3. Cut Phase 8 to F-10.1 to F-10.5.** The mocking half is commodity, and wrapping
Microcks preserves the feature with a fraction of the effort.

**4. Move the Phase 7 gap decisions now, not in Phase 7.** Self-healing, visual
baselines and the browser matrix are three scope decisions (§9.1 to §9.3) that change
Phase 7's size materially. Phase 7 is already tagged XL. Deciding them at the start of
the phase is how XL becomes XXL.

---

## Sources

- [10 Best AI Test Case Generation Tools (2026), TestCollab](https://testcollab.com/blog/ai-test-case-generation-tools)
- [10 Best AI Test Case Generation Tools in 2026 (Ranked), Shiplight AI](https://www.shiplight.ai/blog/best-ai-test-case-generation-tools-2026)
- [Best AI Testing Tools in 2026: 11 Platforms Compared, Shiplight AI](https://www.shiplight.ai/blog/best-ai-testing-tools-2026)
- [Top 13 AI Software Testing in 2026, Security Boulevard](https://securityboulevard.com/2026/06/top-13-ai-software-testing-in-2026/)
- [AI Testing Platforms Compared: How to Choose in 2026, Autonoma](https://getautonoma.com/blog/ai-testing-platform-comparison)
- [QA Wolf Review 2026: Managed E2E Testing, Pricing Model, and Alternatives, QASkills](https://qaskills.sh/blog/qa-wolf-ai-testing-guide-2026)
- [QA Wolf pricing in 2026: managed-service economics, testeragents.com](https://testeragents.com/pricing/qa-wolf/)
- [Kane AI reviews, features, pricing, testingtools.ai](https://www.testingtools.ai/tools/kane-ai/)
- [7 AI Testing Tools Compared: Features, Pricing, Medium](https://medium.com/@jacobmilller65/7-ai-testing-tools-compared-features-pricing-and-what-actually-works-01f1901ff96f)
- [Qodo vs Diffblue: AI Test Generation Compared, DEV](https://dev.to/rahulxsingh/qodo-vs-diffblue-ai-test-generation-compared-4b05)
- [Keploy AI API testing](https://keploy.io/api-testing)
- [How AI Generates API Tests from OpenAPI, TotalShiftLeft](https://totalshiftleft.ai/blog/how-ai-generates-api-tests-from-openapi)
- [QA trends for 2026: AI, agents, and the future of testing, Tricentis](https://www.tricentis.com/blog/qa-trends-ai-agentic-testing)
- [What Is Agentic QA? The Complete Guide for 2026, Katalon](https://katalon.com/resources-center/blog/what-is-agentic-qa-the-complete-guide-for-2026)
- [AI Software Testing Startups: The Definitive 2026 Guide](https://codenote.net/en/posts/ai-software-testing-startups-2026/)
