# Qavia — AI Architecture

Covers: where AI is used and where it is deliberately not, the multi-provider
abstraction, the LangChain / LangGraph role, MCP usage, and cost handling.

---

## 1. Where AI Is Used

Nine AI touchpoints. Everything else in the platform is deterministic code.

| # | Area | Phase | Tier | Tools needed | Schema-locked |
|---|---|---|---|---|---|
| 1 | **Requirement extraction** — parsed spec/doc → features, business rules, validation rules, auth model | 1 | reasoning | none | yes |
| 2 | **Test case design** — one requirement → happy path, negatives, boundaries, auth states | 1 | reasoning | none | yes |
| 3 | **Semantic dedupe** — are these two differently-worded cases the same case | 1 | cheap | none | yes |
| 4 | **API test code generation** — approved cases → Supertest / Postman | 2 | code | none | no (code out) |
| 5 | **Failure analysis** — root cause, suggested fix, evidence | 4 | reasoning | file read, git blame | yes |
| 6 | **Repo comprehension** — find uncovered paths, understand service layer | 5 | code | file read, grep, glob | yes |
| 7 | **Unit test generation** — Jest / Vitest / pytest for chosen functions | 5 | code | file read | no |
| 8 | **UI flow discovery + Playwright generation** | 6 | code (vision optional) | browser control via MCP | flow: yes |
| 9 | **Maintenance decision** — per spec change, regenerate / stale / delete | 10 | reasoning | none | yes |

Plus two minor: k6 script generation (code tier), Jira bug summary (cheap tier).

**Tier, not model.** Prompts reference an abstract tier — `reasoning`, `code`,
`cheap`, `vision` — and settings map each tier to a concrete provider and model.
No prompt or agent file names a model. Swapping providers is a settings change.

---

## 2. Where AI Must Not Be Used

This list is as important as the one above. Each of these is where a team burns
money and loses determinism by reaching for a model.

| Task | Correct implementation |
|---|---|
| Parse OpenAPI / Swagger | `pb33f/libopenapi`, with `$ref` resolution and source positions. Never ask a model to read JSON structure. |
| Diff two spec versions | Structural diff on the parsed tree, from the same library. Exact, free, instant. AI only classifies the *meaning* of a diff it is handed (§1 item 9). |
| Detect project framework | Read `package.json`, `pyproject.toml`, `pom.xml`, lockfiles. |
| Test case fingerprint / dedupe key | Hash of normalized `(method, path, assertion-kind)`. AI is the near-miss fallback only. |
| Coverage numbers | The language's own coverage tool. Never a model estimate. |
| Confidence score | Re-run stability count, or git blame overlap. Never model self-report. |
| Flake detection | Re-run N times, count result changes. Arithmetic. |
| Test execution | Docker. |
| Mock server responses | Schema-driven faker from the OpenAPI response schema. |
| Bulk test data | `brianvoe/gofakeit`, seeded from the schema so a run is reproducible. AI only for semantically tricky fields (realistic Indian address, valid GSTIN shape). |
| Security payloads | Curated payload library. AI chooses *which* endpoint and param to target; it does not invent payloads. |
| Selector validation | Static rule: `data-testid` → role/label → text. AI proposes, the rule rejects CSS `nth-child`. |
| Cost accounting | Sum `llm_calls` rows. |

---

## 3. Multi-Provider Support

### 3.1 Requirement

Users configure which AI provider to use, from the UI, per the settings rules in
`requirements.md` §5. Supported provider kinds:

| Provider kind | Reaches |
|---|---|
| `anthropic` | Claude API directly |
| `bedrock` | Claude and other models on AWS Bedrock |
| `vertex` | Claude and Gemini on Google Cloud |
| `openai` | OpenAI API |
| `azure-openai` | OpenAI models on Azure |
| `gemini` | Google AI Studio (Gemini API) |
| `openai-compatible` | **Any** endpoint speaking the OpenAI API shape |

That last row is the one that makes "any other provider" real. One adapter covers
Ollama, vLLM, LiteLLM, OpenRouter, Together, Groq, Fireworks, DeepSeek, and most
self-hosted gateways. A user adds a provider by supplying a base URL, an API key,
and a model name — no code change, no deploy.

### 3.2 Data model

```
llm_providers
  id              uuid pk
  name            text            -- "Hyscaler Claude", "Local Qwen", "Client-X Azure"
  kind            enum            -- anthropic | bedrock | vertex | openai
                                  -- | azure-openai | gemini | openai-compatible
  config          jsonb           -- non-secret: base_url, region, project_id,
                                  --   api_version, deployment name
  credentials     jsonb           -- encrypted; shape varies by kind
  is_enabled      bool
  is_default      bool
  health_status   enum            -- unknown | ok | failing
  health_checked_at timestamptz
  created_by      uuid fk users
  created_at      timestamptz

llm_models
  id              uuid pk
  provider_id     uuid fk llm_providers
  model_id        text            -- "claude-opus-5", "gpt-5", "gemini-2.5-pro",
                                  --   "anthropic.claude-opus-5", "qwen2.5-coder:32b"
  display_name    text
  tiers           text[]          -- which tiers this model may serve
  capabilities    jsonb           -- see §3.4
  price_input     numeric         -- USD per 1M input tokens
  price_output    numeric
  price_cache_read numeric        -- null if provider has no cache pricing
  max_input_tokens int
  max_output_tokens int
  is_enabled      bool

tier_assignments
  scope           enum global|project
  scope_id        uuid null
  tier            enum reasoning|code|cheap|vision
  model_id        uuid fk llm_models
  fallback_model_id uuid fk llm_models null
  effort          text null       -- provider-specific; null where unsupported
  unique(scope, scope_id, tier)
```

Resolution when the gateway needs a model for a tier:
**project assignment → global assignment → error with a clear message.**

Never a silent code default. If an admin has not configured the `reasoning` tier,
the job fails immediately at enqueue time with "No model assigned to the reasoning
tier. Configure it in Settings → AI." It does not fail inside a worker twenty
minutes later.

### 3.3 Credential shapes per kind

| Kind | Credential fields (all encrypted) |
|---|---|
| `anthropic` | `api_key` |
| `openai` | `api_key`, optional `organization` |
| `azure-openai` | `api_key`, `endpoint`, `deployment`, `api_version` |
| `gemini` | `api_key` |
| `bedrock` | `access_key_id`, `secret_access_key`, optional `session_token`, `region` — or `use_instance_role: true` with no keys |
| `vertex` | `service_account_json`, `project_id`, `location` |
| `openai-compatible` | `base_url`, optional `api_key`, optional custom headers |

`bedrock` with `use_instance_role` is the preferred production path — no long-lived
keys stored at all, IAM handles it. Offer it as the default option in the UI when
the platform detects it is running on EC2 or ECS.

### 3.4 Capability matrix — required, not optional

Providers are not interchangeable. Store what each model can actually do and gate
features on it.

```jsonc
// llm_models.capabilities
{
  "tool_use":            true,
  "vision":              true,
  "structured_output":   "native",   // "native" | "tool_based" | "prompt_only"
  "prompt_caching":      "explicit", // "explicit" | "automatic" | "none"
  "effort_control":      true,
  "max_tool_iterations": 30
}
```

Behaviour driven by this:

- `vision: false` → the model cannot serve the `vision` tier. UI hides it from that
  dropdown.
- `tool_use: false` → cannot serve agents 5, 6, 7, 8 (they need file and browser
  tools). UI shows why it is unavailable rather than letting a user pick it and hit
  a runtime failure.
- `structured_output: "prompt_only"` → the gateway adds a JSON-repair retry loop
  and a stricter validation pass, because the provider will not enforce the schema.
- `prompt_caching` → drives the cost estimate shown in the UI. See §3.6.

New providers seed their capabilities from a shipped defaults table, and an admin
can override per model. A "Test connection" button in the UI runs a probe that
confirms chat, tool use, structured output, and vision, then writes back what
actually worked. Detected capability beats declared capability.

### 3.5 Per-project provider override — this solves the NDA problem

`requirements.md` §8.1 flags that sending client code to an external API may breach
NDAs, and that a refusing client would need local inference.

Multi-provider makes that a configuration choice rather than a blocker:

- Project setting: **AI provider** — inherit global, or pin to a specific provider.
- Project flag: **external AI processing approved** — boolean, admin-set, audited.
- A project without that flag may only be assigned providers marked
  `data_residency: local`. The server enforces this before enqueueing any AI job.
- Provider records carry a `data_residency` field: `local` (Ollama or vLLM on
  Hyscaler hardware), `regional` (Bedrock or Vertex in a named region under an
  enterprise agreement), or `external`.

So: Client A signs off, their project uses Claude on Bedrock in `ap-south-1`.
Client B refuses, their project is pinned to a local Qwen instance and the platform
physically cannot send their code anywhere else. That enforcement is a server-side
check, not a UI hint.

Quality on a local 32B model will be materially worse than Claude for extraction
and analysis. That is a real trade-off to state to the client, not something to
paper over. Record the assigned provider on every generated artifact so output
quality can be traced back to what produced it.

### 3.6 Prompt caching does not normalize — the biggest gotcha

Fan-out is the cost story for this platform: one spec, then 40+ calls sharing that
spec as a prefix. Caching is what makes it affordable. Providers handle it
completely differently, and **no abstraction layer hides this**.

| Provider | Mechanism | What the gateway must do |
|---|---|---|
| Anthropic (direct, Bedrock, Vertex) | Explicit `cache_control` breakpoints on content blocks | Place breakpoints deliberately. Keep the spec prefix byte-identical across the fan-out. Cache reads ~0.1x input price. |
| OpenAI | Automatic prefix caching, no parameter | Just keep the prefix stable and identical. Nothing to set. Discount applied automatically. |
| Gemini | Explicit context caching via a separate cache-create API, with its own TTL and minimum size | Create the cache object once, reference its handle in each call, delete it when the job chain ends. A different code path entirely. |
| `openai-compatible` / local | Usually none | Assume no caching. Cost model is linear in calls. |

Consequences to design for, not discover later:

1. The `LLMGateway` needs a per-provider caching strategy, selected from
   `capabilities.prompt_caching`. Three implementations: explicit-breakpoint,
   passive-prefix, and cache-object-lifecycle.
2. The cost estimate shown before a user submits a job must use the selected
   provider's caching behaviour. The same 40-endpoint spec can differ by roughly an
   order of magnitude in cost between an explicit-caching provider and one with
   none. Showing a single number would be misleading.
3. Prefix stability is a code-level invariant on every path. No timestamps, no
   UUIDs, no per-call IDs above the cache boundary in a prompt. Add a unit test
   that renders the same prompt twice and asserts the prefixes are byte-identical.

### 3.7 Cost normalization

- Prices live in `llm_models`, editable from the UI. Never hardcoded — provider
  pricing changes and a code deploy is the wrong way to track it.
- Every call writes an `llm_calls` row: provider, model, tier, agent, input tokens,
  output tokens, cache-read tokens, cache-write tokens, computed cost, latency,
  job ID, project ID.
- Token counting differs per provider and per model generation. Do not reuse a
  count measured on one model as an estimate for another. Where the provider
  exposes a count-tokens endpoint, use it; otherwise record only actuals from the
  response and label pre-submit estimates as estimates.
- Spend ceiling (`requirements.md` §5.2) is evaluated per provider and in total,
  since a mixed setup may have one cheap local provider and one metered one.

### 3.8 Fallback

Two distinct mechanisms, and mixing them up causes confusion:

- **Same-provider fallback.** The Anthropic API can re-run a policy-declined
  request on another model server-side, inside the same call. Cheap and simple.
  Use it where available for refusal handling.
- **Cross-provider fallback.** Must be implemented in the gateway: catch a rate
  limit, overload, or connection failure, then retry the same logical request on
  the tier's `fallback_model_id`. This is not a refusal handler — it is an
  availability handler. Retry only on retryable errors; never on a 400.

Cross-provider fallback invalidates any cache the first provider had. Record which
provider actually served each call so cost attribution stays correct.

---

## 4. LangChain and LangGraph

### 4.1 What they are used for

The Python service is the only place either appears. The Go API never imports them.

**LangChain — provider abstraction and structured output.** This is the real
reason to take the dependency. Structured output is the hard part of
multi-provider, not the chat call:

- Anthropic uses `output_config.format` with a JSON schema, or tool-based
  constraint.
- OpenAI uses `response_format` with `json_schema` and `strict`.
- Gemini uses `responseSchema` on the generation config.
- Local models often support none of it and need prompt-plus-repair.

Four incompatible mechanisms for one need. `.with_structured_output(PydanticModel)`
normalizes them and picks the best available method per provider. Writing and
maintaining that yourself across seven provider kinds is substantial work and a
permanent source of bugs.

`init_chat_model(model, model_provider=..., **kwargs)` gives one construction path
for every provider kind, driven by the `llm_providers` row.

**LangGraph — agent orchestration.** Used for the agents that need multi-step
tool loops: repo comprehension (§1 item 6), failure analysis (item 5), and UI flow
discovery (item 8). Those genuinely benefit from explicit state machines with
checkpointing, because a run can take minutes and must survive a worker restart.

**Not used for the single-shot agents.** Requirement extraction, test case design,
dedupe, and code generation are one call in, one validated object out. Wrapping
those in a graph adds indirection for nothing. Use the model directly through the
LangChain chat interface and validate with Pydantic.

### 4.2 Rules

- **Pydantic schemas are the contract.** Every schema-locked agent (§1) declares a
  Pydantic model. That model generates the JSON schema, validates the response, and
  types the code. One definition, three uses.
- **Validate after the abstraction anyway.** `.with_structured_output()` reduces
  malformed output; it does not eliminate it, especially on `prompt_only`
  providers. The gateway validates every response against the Pydantic model and
  retries with the validation error fed back on failure, up to a configured limit.
  A hard failure after retries is a job failure with a readable reason, never a
  partially-written result.
- **Do not use LangChain's caching, memory, retriever, or vector-store
  abstractions.** Prompt caching is provider-specific and belongs in the gateway
  (§3.6). Conversation state belongs in Postgres. Neither needs a framework.
- **Pin exact versions** of `langchain-core`, `langgraph`, and each provider
  package. The ecosystem moves fast and minor releases have broken interfaces
  before. Upgrade deliberately, with the agent test suite as the gate.
- **Keep provider construction in one module.** `llm/gateway.py` is the only file
  that calls `init_chat_model` or touches provider credentials. Every agent asks
  the gateway for a tier and gets back a ready client. An agent must never know
  which provider it is talking to.

### 4.3 Gateway shape

```
llm/
  gateway.py        get_client(tier, project_id) -> configured chat model
                    resolves tier -> provider -> credentials -> caching strategy
  providers.py      one adapter per provider kind; credential decryption
  caching.py        three strategies from capabilities.prompt_caching
  accounting.py     writes llm_calls rows, enforces spend ceiling
  schemas/          Pydantic models, one per schema-locked agent
agents/
  extract.py        single-shot
  design.py         single-shot
  dedupe.py         single-shot
  codegen.py        single-shot
  analyze.py        LangGraph
  repo.py           LangGraph
  uiflow.py         LangGraph
```

Everything an agent needs about the provider arrives through `get_client`. That is
what makes the provider swappable from a settings screen.

---

## 5. MCP

**MCP is entirely optional.** Per `requirements.md` §5.4, every capability has a
built-in implementation that requires no MCP server and no external platform. MCP
is configured from the UI when a team wants it, and its absence is never an error.

Where it is used, MCP fits tools that are **remote or shared capabilities with their
own lifecycle**. It is not used where a plain local function is simpler.

### 5.1 Where MCP earns its place — and what runs without it

| Use | Phase | Built-in default | Optional MCP adds |
|---|---|---|---|
| **Browser control for UI flow discovery** | 7 | **Bundled Playwright container.** The agent drives a real browser through the app via a local driver, discovers forms and flows by interacting, then writes specs from what it observed. Fully self-contained. | An external Playwright MCP server, for teams that already run one or want browser control on separate infrastructure. |
| **Bug tracking** | 5, 10 | **Internal defect tracker** (`requirements.md` FR-11). | Jira MCP — handles auth, field discovery, and API versioning without hand-writing a Jira client. |
| **Source hosting** | 6, 11 | Archive upload, or clone-by-URL with an optional token. | GitHub / GitLab MCP — open a PR carrying a suggested fix, read a PR diff to scope regeneration. Handles pagination, rate limits, and API drift. |
| **Database schema inspection** | 8 | SQL dump upload, parsed locally. | Read-only Postgres MCP server, scoped to one database with a read-only role — a cleaner boundary than embedding a driver in the agent. |
| **Chat notification** | 10 | In-app notification centre. | Slack MCP. |

Note the pattern: the built-in path is the product. MCP is how a team plugs the
product into infrastructure they already have.

### 5.2 Where MCP is the wrong tool

- **Reading the cloned repository.** The repo is already on local disk in the
  worker. A plain `read_file` / `grep` / `glob` tool is faster, simpler, and has no
  process to supervise. Do not put MCP between a worker and its own filesystem.
- **Anything inside the runner container.** The runner is a security boundary
  (`requirements.md` §8.2). Do not open an MCP channel across it.
- **Calling Qavia's own internal services.** That is a function call.

### 5.3 MCP across providers

Provider support for MCP is uneven. Claude has a native connector; other providers
require the client to bridge MCP tools into their own tool-calling format.

Resolution: **bridge MCP at the LangChain layer, not the provider layer.** Use
`langchain-mcp-adapters` to load MCP servers and convert their tools into
LangChain tools. Every provider that supports tool calling then gets the same MCP
capability through one path, and the agents stay provider-agnostic.

Consequence: MCP-backed agents require `capabilities.tool_use: true`. A model
without tool use cannot serve those tiers, and the UI must say so (§3.4).

### 5.4 MCP server configuration — from the UI

MCP servers are configured like providers, not like environment variables.

```
mcp_servers
  id             uuid pk
  name           text
  transport      enum stdio | http
  command        text null     -- stdio: command + args
  args           text[] null
  url            text null     -- http: endpoint
  credentials    jsonb         -- encrypted; token or OAuth material
  scope          enum global | project
  scope_id       uuid null
  enabled_tools  text[]        -- allowlist; empty means all
  is_enabled     bool
  health_status  enum
```

- Admin adds a server, clicks **Test connection**, and the platform lists the tools
  it exposes.
- The admin picks which of those tools to allow. Default is a deny-all allowlist
  that the admin opts into, not everything enabled.
- Per-project scoping matters: Client A's Jira server must not be reachable from
  Client B's project.

### 5.5 Security for MCP

An MCP server is a channel through which model output reaches an external system
that can change state. Treat it as one.

- **Allowlist tools explicitly.** Never enable a server's full tool set by default.
  A read-only integration should have only its read tools allowed.
- **Write operations require confirmation.** Creating a Jira issue, opening a pull
  request, or posting to Slack must either be gated on a human approval step or
  restricted to a project explicitly configured for automatic writes. An agent
  reasoning over a failed test should not silently file twenty tickets.
- **Least privilege on the credential.** The Postgres MCP server gets a read-only
  role on one database. The GitHub token gets contents-read unless PR creation is
  actually enabled, and then contents-write on specific repositories only — never
  organisation-wide admin.
- **Prompt injection is a live risk here.** Agents read untrusted input: uploaded
  specs, client source code, HTTP response bodies, test logs. Any of those can
  contain text crafted to look like an instruction. An agent holding a write-capable
  MCP tool and reading untrusted content is the exact combination that turns
  injection into action. Mitigate by keeping write tools off the agents that read
  untrusted content, requiring confirmation for writes, and logging every MCP tool
  call with its arguments and the job that made it.
- **Audit every call.** `mcp_calls` table: server, tool, arguments, result status,
  agent, job ID, timestamp. Redact credential-shaped values.

### 5.6 Qavia as an MCP server — deferred, worth noting

Qavia could expose its own MCP server so developers use it from inside Claude
Code, Cursor, or any MCP client: "generate tests for the endpoint I just added",
"why did this test fail". That is a genuinely useful product surface and a natural
extension once the core platform works.

Out of scope for v1. Recorded here so the API is designed with it in mind — keep
the generate and analyse operations callable as clean service functions, not
buried inside HTTP controllers.

---

## 6. Settings Impact

This document replaces the AI section of `requirements.md` §5.2. The settings
screens become:

**Settings → AI → Providers** (admin)
List of configured providers with health status. Add, edit, test connection,
enable, disable. Per provider: kind, non-secret config, credentials, data
residency.

**Settings → AI → Models** (admin)
Per provider, the enabled models. Per model: display name, tiers it may serve,
capabilities (detected, overridable), prices, token limits.

**Settings → AI → Tiers** (admin)
Map each tier — reasoning, code, cheap, vision — to a model, an optional fallback
model, and an effort level where the provider supports one.

**Settings → AI → Budget** (admin)
Monthly ceiling per provider and in total. Behaviour on reaching it: block or warn.
Current spend, broken down by provider, project, and agent.

**Settings → MCP** (admin) — optional
Configured MCP servers, transport, credentials, tool allowlist, scope, health.
Empty by default. Every capability an MCP server can provide has a built-in
implementation (`requirements.md` §5.4), so this screen may stay empty forever
without limiting the product.

**Project → Settings → AI** (QA Lead)
Inherit global tiers, or override per tier. Assigned provider. External-AI-approved
flag (admin only). Visible cost estimate for the next generation run, computed
using the assigned provider's actual caching behaviour.

Only the five bootstrap environment variables from `requirements.md` §5.1 remain
outside the UI. No provider key, model name, or MCP endpoint is ever an environment
variable.
