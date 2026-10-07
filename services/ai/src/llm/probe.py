"""Capability probe: detected beats declared (F-16.4, F-16.5).

A capability matrix seeded from a defaults table is a claim about a model. This
runs four small calls and reports what actually worked, and the result overwrites
the declaration, because an admin who mis-typed a model name should learn from a
Test connection button rather than from a job that fails twenty minutes in.

Every probe is deliberately tiny. A "test connection" that costs real money is one
nobody presses.
"""

from __future__ import annotations

import logging
import time

from langchain_core.messages import HumanMessage
from langchain_core.tools import tool

from .errors import LLMError, classify
from .providers import build_client
from .schemas import ProbeRequest, ProbeResult

logger = logging.getLogger(__name__)

# A 1x1 transparent PNG. Small enough to be free, real enough that a model without
# vision refuses it.
_PIXEL = (
    "data:image/png;base64,"
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)

# The title is required, not decoration: an adapter turns a schema into a function
# or a response format, and both need a name.
_SCHEMA = {
    "title": "ProbeAnswer",
    "type": "object",
    "properties": {"ok": {"type": "boolean"}},
    "required": ["ok"],
    "additionalProperties": False,
}


@tool
def probe_tool(value: str) -> str:
    """Echo the value back. Used only to confirm the model can call a tool."""

    return value


async def probe(request: ProbeRequest) -> ProbeResult:
    started = time.monotonic()

    try:
        client = build_client(request.target)
    except LLMError as err:
        return ProbeResult(reachable=False, detail=err.message)

    result = ProbeResult(reachable=False)

    # Chat first. Everything else is meaningless if this fails, and its failure is
    # the one worth reporting.
    try:
        await client.ainvoke([HumanMessage(content="Reply with the single word: ready")])
        result.reachable = True
        result.chat = True
    except Exception as err:
        failure = classify(err)
        result.detail = failure.message
        result.latency_ms = int((time.monotonic() - started) * 1000)
        return result

    result.tool_use = await _succeeds(
        lambda: client.bind_tools([probe_tool]).ainvoke(
            [HumanMessage(content="Call probe_tool with the value 'ping'.")]
        )
    )

    result.structured_output = await _succeeds(
        lambda: client.with_structured_output(_SCHEMA).ainvoke(
            [HumanMessage(content='Return {"ok": true} and nothing else.')]
        )
    )

    result.vision = await _succeeds(
        lambda: client.ainvoke(
            [
                HumanMessage(
                    content=[
                        {"type": "text", "text": "Reply with the single word: seen"},
                        {"type": "image_url", "image_url": {"url": _PIXEL}},
                    ]
                )
            ]
        )
    )

    result.latency_ms = int((time.monotonic() - started) * 1000)
    if not result.detail:
        result.detail = "Reachable."
    return result


async def _succeeds(build) -> bool:  # type: ignore[no-untyped-def]
    """Run one capability check.

    A failure here is a "no", not an error: the whole point is to find out, and a
    model that cannot do tool use is a normal thing to discover.

    It takes a thunk rather than a bound call, because building the runnable can
    fail too: an adapter that refuses to bind a tool is exactly the "no" this is
    looking for, and an exception raised while assembling the argument would
    otherwise escape the try entirely.
    """

    try:
        await build()
    except Exception as err:
        logger.info("probe check failed", extra={"cause": type(err).__name__})
        return False
    return True
