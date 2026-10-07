"""Qavia AI service.

Three rules govern this service. Breaking any one of them undoes the reason for
having two runtimes at all (backend-standards.md 10):

1. Stateless. No database access. No connection string, no ORM, no migrations.
   Everything it needs arrives in the request; everything it produces goes back
   in the response.
2. The boundary is a versioned OpenAPI contract. FastAPI publishes the schema,
   Go generates a typed client in CI. A breaking change fails the build.
3. No business logic. This service runs agents. What to generate, when, and what
   to do with the result is Go's decision.
"""

from __future__ import annotations

import logging
from contextvars import ContextVar

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel

from .agents import analyse as analyse_agent
from .agents import perf as perf_agent
from .agents import probeselect as probeselect_agent
from .agents import repo as repo_agent
from .agents import testdata as testdata_agent
from .agents import uiflow as uiflow_agent
from .agents import uispec as uispec_agent
from .agents import unittests as unittest_agent
from .agents import codegen as codegen_agent
from .agents import dedupe as dedupe_agent
from .agents import design as design_agent
from .agents import extract as extract_agent
from .llm import cache_objects, gateway, probe, providers
from .llm.errors import LLMError
from .llm.schemas import (
    AgentResult,
    AnalyseRequest,
    CacheCreateRequest,
    CacheHandle,
    ChatRequest,
    ChatResponse,
    CodegenRequest,
    DedupeRequest,
    DesignRequest,
    ErrorResponse,
    ExtractRequest,
    MCPDiscoverRequest,
    MCPDiscoverResponse,
    MCPTool,
    ProbeRequest,
    RepoStepRequest,
    ProbeResult,
    PerfRequest,
    ProbeSelectRequest,
    TestDataRequest,
    Tier,
    UIFlowRequest,
    UISpecRequest,
    UnitTestRequest,
)

logger = logging.getLogger(__name__)

CORRELATION_HEADER = "X-Correlation-ID"

_correlation_id: ContextVar[str] = ContextVar("correlation_id", default="")

# Same redaction key patterns as the Go logger. NFR-10 is enforced by the
# logger, not by discipline at call sites.
REDACT_PATTERNS = ("key", "token", "secret", "password", "credentials", "authorization")


class CorrelationFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        record.correlation_id = _correlation_id.get()
        return True


def configure_logging() -> None:
    handler = logging.StreamHandler()
    handler.addFilter(CorrelationFilter())
    handler.setFormatter(
        logging.Formatter('{"level":"%(levelname)s","msg":"%(message)s","correlation_id":"%(correlation_id)s"}')
    )
    root = logging.getLogger()
    root.handlers = [handler]
    root.setLevel(logging.INFO)


app = FastAPI(
    title="Qavia AI Service",
    version="0.1.0",
    description="Stateless agent runner. Holds no persistence and no business logic.",
)

configure_logging()


@app.middleware("http")
async def correlation_middleware(request: Request, call_next):  # type: ignore[no-untyped-def]
    token = _correlation_id.set(request.headers.get(CORRELATION_HEADER, ""))
    try:
        response = await call_next(request)
    finally:
        _correlation_id.reset(token)
    if cid := request.headers.get(CORRELATION_HEADER):
        response.headers[CORRELATION_HEADER] = cid
    return response


class Health(BaseModel):
    status: str
    version: str


@app.get("/healthz", response_model=Health, operation_id="getAIHealth")
async def healthz() -> Health:
    return Health(status="ok", version=app.version)


@app.exception_handler(LLMError)
async def llm_error_handler(_: Request, err: LLMError) -> JSONResponse:
    """One error envelope, mirroring Go's {code, message, details}.

    The kind is what Go branches on: only ``retryable`` is worth another attempt,
    and only ``auth`` means an admin has to change a setting.
    """

    logger.warning("ai call rejected", extra={"code": err.code, "kind": err.kind.value})
    return JSONResponse(
        status_code=err.status,
        content=ErrorResponse(
            code=err.code, message=err.message, kind=err.kind, details=err.details
        ).model_dump(mode="json"),
    )


@app.post(
    "/v1/chat",
    response_model=ChatResponse,
    # Not simply "chat": oapi-codegen names the response wrapper after the
    # operation, and a "chat" operation beside a ChatResponse model collides in
    # the generated Go.
    operation_id="chatCompletion",
    summary="Run one completion",
    responses={
        400: {"model": ErrorResponse},
        401: {"model": ErrorResponse},
        422: {"model": ErrorResponse},
        503: {"model": ErrorResponse},
    },
)
async def chat(request: ChatRequest) -> ChatResponse:
    """Everything an agent asks for goes through here.

    Credentials arrive decrypted in the request and are used for this call only.
    Usage comes back in the response and is never written here: Go owns
    ``llm_calls`` (backend-standards.md 10).
    """

    return await gateway.chat(request)


@app.post(
    "/v1/probe",
    response_model=ProbeResult,
    operation_id="probeModel",
    summary="Detect what a model can actually do",
)
async def probe_model(request: ProbeRequest) -> ProbeResult:
    """Backs the Test connection button. Detected capability overwrites declared."""

    return await probe.probe(request)


@app.post(
    "/v1/cache",
    response_model=CacheHandle,
    operation_id="createCacheObject",
    summary="Create a provider-side context cache for a fan-out",
)
async def create_cache(request: CacheCreateRequest) -> CacheHandle:
    """Only Gemini has one. Everyone else answers not-created, which is not an
    error: the caller proceeds and the cost model is linear."""

    return await cache_objects.create(request)


class CacheDeleteRequest(BaseModel):
    """Teardown for a cache object, run at chain end including on failure."""

    api_key: str
    handle: str


class CacheDeleteResult(BaseModel):
    deleted: bool


@app.post(
    "/v1/cache/delete",
    response_model=CacheDeleteResult,
    operation_id="deleteCacheObject",
    summary="Release a context cache",
)
async def delete_cache(request: CacheDeleteRequest) -> CacheDeleteResult:
    """A DELETE with a body would be the honest verb, but a cache handle and an API
    key do not belong in a URL: both end up in proxy logs."""

    return CacheDeleteResult(deleted=await cache_objects.delete(request.api_key, request.handle))


class ProviderKinds(BaseModel):
    kinds: list[str]


@app.get(
    "/v1/providers",
    response_model=ProviderKinds,
    operation_id="listProviderKinds",
    summary="Provider kinds this build can serve",
)
async def provider_kinds() -> ProviderKinds:
    """Read by the settings screen, so a build without a provider package installed
    cannot offer a kind it would fail on."""

    return ProviderKinds(kinds=list(providers.supported_kinds()))


async def _run_agent(call, agent: str, tier: str, schema, messages) -> AgentResult:
    """Run one agent through the gateway.

    Every agent goes through this: it is where the tier, the schema, and the
    retry limit meet the request, and having one path means an agent cannot
    quietly skip validation or pick its own model.
    """

    response = await gateway.chat(
        ChatRequest(
            tier=Tier(tier),
            agent=agent,
            target=call.target,
            fallback=call.fallback,
            messages=messages,
            response_schema=schema,
            max_validation_retries=call.max_validation_retries,
        )
    )

    return AgentResult(
        result=response.structured or {},
        usage=response.usage,
        served_by=response.served_by,
        latency_ms=response.latency_ms,
        validation_retries=response.validation_retries,
    )


@app.post(
    "/v1/agents/extract",
    response_model=AgentResult,
    operation_id="runExtractAgent",
    summary="Extract requirements from a parsed specification",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_extract(request: ExtractRequest) -> AgentResult:
    """Reads the normalized model rather than the uploaded file: deterministic
    code already parsed it, and a model asked to re-read JSON structure would be
    slower, cost money, and be wrong occasionally rather than never."""

    return await _run_agent(
        request,
        extract_agent.AGENT,
        extract_agent.TIER,
        extract_agent.SCHEMA,
        extract_agent.messages(request.document),
    )


@app.post(
    "/v1/agents/design",
    response_model=AgentResult,
    operation_id="runDesignAgent",
    summary="Design test cases for one requirement",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_design(request: DesignRequest) -> AgentResult:
    """One requirement per call, with the specification as a cached prefix. Go
    bounds how many of these run at once."""

    return await _run_agent(
        request,
        design_agent.AGENT,
        design_agent.TIER,
        design_agent.SCHEMA,
        design_agent.messages(request.document, request.requirement),
    )


@app.post(
    "/v1/agents/dedupe",
    response_model=AgentResult,
    operation_id="runDedupeAgent",
    summary="Decide which candidates duplicate a stored case",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_dedupe(request: DedupeRequest) -> AgentResult:
    """The near-miss fallback only, on the cheap tier. Exact duplicates were
    caught by a hash before this was called."""

    return await _run_agent(
        request,
        dedupe_agent.AGENT,
        dedupe_agent.TIER,
        dedupe_agent.SCHEMA,
        dedupe_agent.messages(request.existing, request.candidates),
    )


@app.post(
    "/v1/agents/analyse",
    response_model=AgentResult,
    operation_id="runAnalyseAgent",
    summary="Explain why a test failed, with citations",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_analyse(request: AnalyseRequest) -> AgentResult:
    """Reasoning tier, because this is the hardest thing the platform asks a model to
    do: read a log, a response body, and a test, and say which of them is wrong.

    Every citation the analysis makes is checked by Go against the material before the
    row is stored, and a failed check comes back here as feedback rather than being
    silently accepted (BE-5.3)."""

    return await _run_agent(
        request,
        analyse_agent.AGENT,
        analyse_agent.TIER,
        analyse_agent.SCHEMA,
        analyse_agent.messages(
            request.context,
            request.test_name,
            request.status,
            request.attempt,
            request.failure_message,
            request.history,
            request.feedback,
        ),
    )


@app.post(
    "/v1/agents/repo-step",
    response_model=AgentResult,
    operation_id="runRepoStepAgent",
    summary="Choose the next step of a repository exploration",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_repo_step(request: RepoStepRequest) -> AgentResult:
    """One step, not a loop. Go executes the chosen tool against the checkout it owns
    and calls back with the result, because the process that owns the filesystem is
    the one that can safely validate a path (BE-6.4)."""

    return await _run_agent(
        request,
        repo_agent.AGENT,
        repo_agent.TIER,
        repo_agent.SCHEMA,
        repo_agent.messages(
            request.tree,
            request.stack,
            request.step,
            request.budget,
            request.history,
        ),
    )


@app.post(
    "/v1/agents/perf",
    response_model=AgentResult,
    operation_id="runPerfAgent",
    summary="Generate a k6 load script under an authorised profile",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_perf(request: PerfRequest) -> AgentResult:
    """The load profile is a person's decision, fixed in the request; the model writes
    the script that exercises the endpoints under it. Validated in the k6 image before
    it makes a single request (BE-9.1)."""

    return await _run_agent(
        request,
        perf_agent.AGENT,
        perf_agent.TIER,
        perf_agent.SCHEMA,
        perf_agent.messages(
            request.endpoints,
            request.profile,
            request.p95_ms,
            request.error_rate,
        ),
    )


@app.post(
    "/v1/agents/probe-select",
    response_model=AgentResult,
    operation_id="runProbeSelectAgent",
    summary="Choose which endpoints and library payloads to probe",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_probe_select(request: ProbeSelectRequest) -> AgentResult:
    """The agent picks endpoints and payload IDs from the catalogue; it never writes a
    payload string. The strings live in a reviewed Go library, and a plan naming a raw
    payload is refused (BE-9.3)."""

    return await _run_agent(
        request,
        probeselect_agent.AGENT,
        probeselect_agent.TIER,
        probeselect_agent.SCHEMA,
        probeselect_agent.messages(
            request.endpoints,
            request.catalogue,
            request.categories,
        ),
    )


@app.post(
    "/v1/mcp/discover",
    operation_id="discoverMCPTools",
    summary="Connect to an MCP server and list its tools",
    responses={422: {"model": ErrorResponse}, 502: {"model": ErrorResponse}},
)
async def discover_mcp(request: MCPDiscoverRequest) -> MCPDiscoverResponse:
    """The connection test. Go owns the server configuration and its credential and
    passes them here; this service connects via langchain-mcp-adapters, lists the tools,
    and returns them so an admin can opt into specific ones (BE-10.1)."""

    from . import mcp as mcp_bridge

    try:
        tools = await mcp_bridge.discover(request.model_dump())
    except Exception as exc:  # noqa: BLE001 - a connection failure is the expected error
        return JSONResponse(
            status_code=502,
            content={"error": {"code": "mcp_unreachable", "message": str(exc)[:500]}},
        )
    return MCPDiscoverResponse(tools=[MCPTool(**tool) for tool in tools])


@app.post(
    "/v1/agents/testdata",
    response_model=AgentResult,
    operation_id="runTestDataAgent",
    summary="Write realistic values for named fields",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_testdata(request: TestDataRequest) -> AgentResult:
    """Named fields only, on the cheap tier, one call for the whole batch. The bulk of
    a generated set comes from a seeded faker in Go: it is free, instant, and identical
    every run, and a model asked for two thousand values would cost money to be
    occasionally wrong (BE-8.2)."""

    return await _run_agent(
        request,
        testdata_agent.AGENT,
        testdata_agent.TIER,
        testdata_agent.SCHEMA,
        testdata_agent.messages(
            request.fields,
            request.count,
            request.locale,
            request.context,
        ),
    )


@app.post(
    "/v1/agents/uiflow-step",
    response_model=AgentResult,
    operation_id="runUIFlowStepAgent",
    summary="Choose the next step of a UI flow discovery",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_uiflow_step(request: UIFlowRequest) -> AgentResult:
    """One step, not a loop. The browser runs in a worker's container, so Go performs
    the action and calls back with what the page looked like afterwards: the process
    that owns the container is the one that can validate an action against the
    vocabulary, hold the budget, and substitute a credential without showing it to a
    model (BE-7.2, BE-7.3.2)."""

    return await _run_agent(
        request,
        uiflow_agent.AGENT,
        uiflow_agent.TIER,
        uiflow_agent.SCHEMA,
        uiflow_agent.messages(
            request.context,
            request.app_url,
            request.auth,
            request.step,
            request.budget,
            request.history,
        ),
    )


@app.post(
    "/v1/agents/uispec",
    response_model=AgentResult,
    operation_id="runUISpecAgent",
    summary="Turn one discovered flow into a Playwright spec",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_uispec(request: UISpecRequest) -> AgentResult:
    """One call per flow, on the code tier. The selector policy is stated in the prompt
    and enforced in Go afterwards: a spec that reaches for a positional selector is
    rejected and regenerated rather than merged with a warning (BE-7.4.2)."""

    return await _run_agent(
        request,
        uispec_agent.AGENT,
        uispec_agent.TIER,
        uispec_agent.SCHEMA,
        uispec_agent.messages(request.graph, request.flow, request.feedback),
    )


@app.post(
    "/v1/agents/unittest",
    response_model=AgentResult,
    operation_id="runUnitTestAgent",
    summary="Write a unit test for one untested target",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_unittest(request: UnitTestRequest) -> AgentResult:
    """One call per target, on the code tier. The framework comes from the repository's
    own manifest, and the file goes through the same static validation as generated API
    tests: compiled and scanned in the image that owns the toolchain (BE-3.4)."""

    return await _run_agent(
        request,
        unittest_agent.AGENT,
        unittest_agent.TIER,
        unittest_agent.SCHEMA,
        unittest_agent.messages(
            request.source,
            request.framework,
            request.file,
            request.symbols,
            request.feedback,
        ),
    )


@app.post(
    "/v1/agents/codegen",
    response_model=AgentResult,
    operation_id="runCodegenAgent",
    summary="Turn approved test cases into a runnable file",
    responses={422: {"model": ErrorResponse}, 503: {"model": ErrorResponse}},
)
async def run_codegen(request: CodegenRequest) -> AgentResult:
    """The envelope is schema-locked and the content is code.

    Go needs the path and the covered case IDs to store the file and to trace it,
    and neither can be recovered reliably from prose wrapped around a code block.
    """

    return await _run_agent(
        request,
        codegen_agent.AGENT,
        codegen_agent.TIER,
        codegen_agent.SCHEMA,
        codegen_agent.messages(
            request.document,
            request.framework,
            request.endpoint,
            request.suggested_path,
            request.cases,
            request.feedback,
        ),
    )
