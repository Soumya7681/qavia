"""The Go/Python contract.

These models are the boundary (backend-standards.md 10). FastAPI publishes them
as OpenAPI, Go generates a typed client from that schema, and a breaking change
here fails the Go build rather than production.

Two rules show up all over this file:

- Credentials arrive decrypted, per request, and are never stored. Python holds no
  database handle and no APP_ENCRYPTION_KEY, so a compromised AI service leaks the
  keys of calls in flight, not every key on the platform.
- Usage is returned, never persisted. Go writes ``llm_calls``.

The enums inherit from ``str`` and ``Enum`` rather than ``StrEnum``. The published
schema is the Go contract, and the two spellings do not produce identical OpenAPI
under the pinned Pydantic: changing it would regenerate the Go client for no
behavioural gain.
"""

from __future__ import annotations

from enum import Enum
from typing import Any

from pydantic import BaseModel, Field


class ProviderKind(str, Enum):  # noqa: UP042 - see module note
    """The provider kinds from ai-architecture.md 3.1.

    ``openai_compatible`` is the row that makes "any other provider" real: one
    adapter covers Ollama, vLLM, LiteLLM, OpenRouter, Together, Groq, Fireworks,
    and DeepSeek.
    """

    anthropic = "anthropic"
    bedrock = "bedrock"
    vertex = "vertex"
    openai = "openai"
    azure_openai = "azure-openai"
    gemini = "gemini"
    openai_compatible = "openai-compatible"


class Tier(str, Enum):  # noqa: UP042 - see module note
    """What a call is for, rather than which model runs it.

    An agent asks for a tier. Which model serves it is a settings decision, which
    is what keeps provider switching a dropdown change forever.
    """

    reasoning = "reasoning"
    code = "code"
    cheap = "cheap"
    vision = "vision"


class StructuredOutputMode(str, Enum):  # noqa: UP042 - see module note
    native = "native"
    tool_based = "tool_based"
    prompt_only = "prompt_only"


class PromptCaching(str, Enum):  # noqa: UP042 - see module note
    explicit = "explicit"
    automatic = "automatic"
    none = "none"


class Capabilities(BaseModel):
    """What a model can actually do (ai-architecture.md 3.4).

    Providers are not interchangeable, and pretending otherwise produces runtime
    failures a user cannot act on. Gating features on this is what turns "the model
    cannot do that" into a disabled option with a reason.
    """

    tool_use: bool = True
    vision: bool = False
    structured_output: StructuredOutputMode = StructuredOutputMode.native
    prompt_caching: PromptCaching = PromptCaching.none
    effort_control: bool = False
    max_tool_iterations: int = 30


class Provider(BaseModel):
    """One configured provider, as Go resolved it."""

    kind: ProviderKind

    # Non-secret settings: base_url, region, project_id, api_version, deployment.
    config: dict[str, Any] = Field(default_factory=dict)

    # Decrypted for this call only. Shapes are per kind (ai-architecture.md 3.3)
    # and validated by the adapter, so a wrong shape fails with a message naming
    # the field rather than as a provider 401 twenty seconds later.
    credentials: dict[str, Any] = Field(default_factory=dict)


class Model(BaseModel):
    """The model to run, and what it can do."""

    model_id: str
    capabilities: Capabilities = Field(default_factory=Capabilities)

    max_output_tokens: int | None = None

    # Provider-specific reasoning effort. Null where the provider has no such
    # control, rather than a made-up default.
    effort: str | None = None


class Target(BaseModel):
    """A provider and model pair the gateway can build a client from."""

    provider: Provider
    model: Model


class Role(str, Enum):  # noqa: UP042 - see module note
    system = "system"
    user = "user"
    assistant = "assistant"


class Message(BaseModel):
    role: Role
    content: str

    # Marks the stable prefix a caching strategy may place a breakpoint after.
    # Never put a timestamp, UUID, or per-call ID in a cacheable message: it
    # silently destroys caching, which is why there is a test for it.
    cacheable: bool = False


class ChatRequest(BaseModel):
    """One completion, with everything needed to make it."""

    tier: Tier
    agent: str = Field(description="Which agent asked, recorded on the llm_calls row.")

    target: Target

    # Used only for retryable failures: rate limit, overload, connection failure.
    # Never for a 400 (ai-architecture.md 3.8).
    fallback: Target | None = None

    messages: list[Message]

    # JSON Schema the response must satisfy. Absent means free text.
    response_schema: dict[str, Any] | None = None

    # How many times to feed a validation error back into the prompt before
    # failing. The abstraction reduces malformed output; it does not eliminate it.
    max_validation_retries: int = 2

    temperature: float | None = None

    # Gemini's context cache handle, owned by the job chain rather than the call.
    cache_handle: str | None = None


class Usage(BaseModel):
    """What the call cost, in tokens. Go turns this into money and a row."""

    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0


class ServedBy(BaseModel):
    """Which provider actually answered.

    Recorded because a cross-provider fallback moves the cost to a different price
    list, and attribution that assumed the primary would be wrong.
    """

    provider_kind: ProviderKind
    model_id: str
    fallback_used: bool = False


class ChatResponse(BaseModel):
    text: str = ""

    # Present when response_schema was supplied and validation passed.
    structured: dict[str, Any] | None = None

    usage: Usage = Field(default_factory=Usage)
    served_by: ServedBy
    latency_ms: int = 0

    # How many times the response failed its schema and was retried. A non-zero
    # value on a supposedly native-structured-output provider is worth seeing.
    validation_retries: int = 0

    # Returned when a cache object was created for this chain.
    cache_handle: str | None = None


class AgentCall(BaseModel):
    """What every agent endpoint needs to make its call.

    The target comes from Go, which resolved the tier: an agent asks for a tier
    and never for a model, and the resolution rules live where the settings and the
    database are (backend-standards.md 10).
    """

    target: Target
    fallback: Target | None = None

    max_validation_retries: int = 2

    # Recorded on the llm_calls row Go writes, so spend is attributable per agent.
    project_id: str | None = None
    job_id: str | None = None


class AgentResult(BaseModel):
    """One agent's output, plus what it cost.

    The result is the agent's own schema, kept as a dict rather than typed per
    agent: the shape is the JSON Schema the agent declared, Go validates against
    the same one, and a second Pydantic model here would be a third place for the
    three to disagree.
    """

    result: dict[str, Any]

    usage: Usage = Field(default_factory=Usage)
    served_by: ServedBy
    latency_ms: int = 0
    validation_retries: int = 0


class ExtractRequest(AgentCall):
    """Extraction reads the parsed specification, never the raw file."""

    document: dict[str, Any]


class DesignRequest(AgentCall):
    """One call per requirement, sharing the specification as a cached prefix."""

    document: dict[str, Any]
    requirement: dict[str, Any]


class DedupeRequest(AgentCall):
    """The near-miss pass. Exact duplicates never get here."""

    existing: list[dict[str, Any]] = Field(default_factory=list)
    candidates: list[dict[str, Any]] = Field(default_factory=list)


class CodegenRequest(AgentCall):
    """One endpoint's file, from its approved cases."""

    document: dict[str, Any]
    framework: str = "supertest"

    # "METHOD /path", or empty for a suite that covers the whole specification.
    endpoint: str = ""

    # What Go would like the file called. The model may choose otherwise, and Go
    # validates whatever comes back.
    suggested_path: str = "api.test.ts"

    cases: list[dict[str, Any]] = Field(default_factory=list)

    # The validator's findings from a previous attempt at this file, so a retry
    # corrects a stated problem instead of rolling the dice again (BE-3.4.2).
    feedback: str = ""


class AnalyseRequest(AgentCall):
    """One failure, with the material an explanation may cite (BE-5.2).

    ``context`` is the cacheable prefix: the run log, the captured response, and the
    source of the failing test. A run with forty failures analyses forty times against
    the same log, so paying full price for it forty times is the difference between
    this feature being affordable and not.
    """

    context: dict[str, Any] = Field(default_factory=dict)

    test_name: str = ""
    status: str = "failed"
    attempt: int = 1
    failure_message: str = ""

    # This test's recent results, newest first, so the analysis can say "flaky" with
    # something behind it. The platform computes the stability number itself.
    history: list[dict[str, Any]] = Field(default_factory=list)

    # The evidence findings from a rejected previous attempt, fed back so the retry
    # is a correction (BE-5.3.3).
    feedback: str = ""


class RepoStepRequest(AgentCall):
    """One step of a repository exploration (BE-6.4).

    The tools run in Go, which owns the checkout and the path validation, so this
    request carries what has been seen rather than a filesystem handle. ``tree`` is the
    cacheable prefix: thirty steps over one repository are one cached prefix plus
    thirty short suffixes.
    """

    tree: dict[str, Any] = Field(default_factory=dict)
    stack: str = ""

    step: int = 1
    budget: int = 30

    # Each entry is {action, detail, result}: what was asked for and what came back.
    history: list[dict[str, Any]] = Field(default_factory=list)


class UnitTestRequest(AgentCall):
    """One untested target's test file (BE-6.5).

    ``source`` is the cacheable prefix: the target's own code and its neighbours. A
    regeneration after a validation failure reuses it, so a correction costs a cache
    read plus a short instruction rather than the whole file again.
    """

    source: dict[str, Any] = Field(default_factory=dict)

    # The framework the repository already uses, read from its own manifest rather
    # than chosen by the model (BE-6.3).
    framework: str = ""

    file: str = ""
    symbols: list[str] = Field(default_factory=list)

    feedback: str = ""


class UIFlowRequest(AgentCall):
    """One step of a UI flow discovery (BE-7.2).

    The browser lives in a worker's container, whose egress allowlist, credentials, and
    reaping already govern it, so this request carries what the last action showed
    rather than a browser handle. ``context`` is the cacheable prefix: forty steps
    against one application are one cached prefix plus forty short suffixes.
    """

    context: dict[str, Any] = Field(default_factory=dict)

    # The application being explored. Named app_url rather than target because
    # AgentCall.target already means the provider and model this call resolves to, and a
    # field that shadows it silently replaces the routing decision with a URL.
    app_url: str = ""

    # The project's configured mode, so the agent knows whether to expect a login form
    # and does not spend steps discovering that it needs one. The credentials themselves
    # are never sent here: they are substituted inside the browser (BE-7.3.2).
    auth: str = "none"

    step: int = 1
    budget: int = 40

    # Each entry is {action, detail, result}: what was done and what the page looked
    # like afterwards.
    history: list[dict[str, Any]] = Field(default_factory=list)


class UISpecRequest(AgentCall):
    """One discovered flow's Playwright spec (BE-7.4).

    ``graph`` is the cacheable prefix: the pages and what can be done on each do not
    change between the flows of one application, so a suite is one cached prefix plus
    one short instruction per flow. A regeneration after a rejected selector reuses it.
    """

    graph: dict[str, Any] = Field(default_factory=dict)
    flow: dict[str, Any] = Field(default_factory=dict)

    feedback: str = ""


class TestDataRequest(AgentCall):
    """Realistic values for the handful of fields a faker cannot write (BE-8.2).

    One call for the whole batch rather than one per record: bulk generation is
    deliberately not an AI feature, and this is the narrow exception where semantics
    matter.
    """

    fields: list[dict[str, Any]] = Field(default_factory=list)

    count: int = 10
    locale: str = ""

    # What the object is, as the cacheable prefix.
    context: dict[str, Any] = Field(default_factory=dict)


class PerfRequest(AgentCall):
    """A k6 load script from the API's endpoints under an authorised profile (BE-9.1).

    The profile is fixed by whoever authorised the test; ``endpoints`` is the cacheable
    prefix, so generating a heavier variant reuses it.
    """

    endpoints: dict[str, Any] = Field(default_factory=dict)
    profile: str = ""
    p95_ms: int = 500
    error_rate: float = 0.01


class ProbeSelectRequest(AgentCall):
    """Which endpoints and payload IDs to point a security probe at (BE-9.3).

    The catalogue carries payload IDs and purposes, never the payload strings: the
    strings live in a reviewed Go library, and the agent's only job is to choose which
    reviewed entry to aim where. A plan that named a raw payload would be refused.
    """

    endpoints: dict[str, Any] = Field(default_factory=dict)
    catalogue: list[dict[str, Any]] = Field(default_factory=list)
    categories: list[str] = Field(default_factory=list)


class MCPDiscoverRequest(BaseModel):
    """A server to connect to and list the tools of (BE-10.1).

    Go owns the configuration and the credential; this carries them for one connection
    and this service holds neither afterwards.
    """

    transport: str = "stdio"
    command: str = ""
    args: list[str] = Field(default_factory=list)
    url: str = ""
    credential: str = ""


class MCPTool(BaseModel):
    """One tool a server exposes."""

    name: str
    description: str = ""
    write: bool = False


class MCPDiscoverResponse(BaseModel):
    """The tools a connection test found."""

    tools: list[MCPTool] = Field(default_factory=list)


class ProbeRequest(BaseModel):
    """Confirms what a model can really do (BE-1.10)."""

    target: Target


class ProbeResult(BaseModel):
    """Detected capability. It overwrites declared capability, because a
    declaration is a claim and this is a measurement."""

    reachable: bool
    chat: bool = False
    tool_use: bool = False
    structured_output: bool = False
    vision: bool = False

    # Safe to show a user. It names the failure, never a credential.
    detail: str = ""

    latency_ms: int = 0


class CacheCreateRequest(BaseModel):
    """Creates a provider-side context cache for a fan-out."""

    target: Target
    messages: list[Message]
    ttl_seconds: int = 900


class CacheHandle(BaseModel):
    handle: str | None = None

    # False where the provider has no cache-object API. Not an error: the caller
    # proceeds without one and the cost model is linear.
    created: bool = False
    detail: str = ""


class ErrorKind(str, Enum):  # noqa: UP042 - see module note
    """Why a call failed, in terms the gateway can act on.

    ``retryable`` is the only kind that triggers cross-provider fallback. A
    ``bad_request`` retried on another provider is a bad request twice.
    """

    retryable = "retryable"
    bad_request = "bad_request"
    auth = "auth"
    not_supported = "not_supported"
    validation = "validation"
    internal = "internal"


class ErrorResponse(BaseModel):
    """The error envelope. Mirrors Go's {code, message, details}."""

    code: str
    message: str
    kind: ErrorKind = ErrorKind.internal
    details: dict[str, Any] | None = None
