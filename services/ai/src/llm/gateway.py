"""The gateway: the only file that builds a client or touches a credential.

Everything an agent does goes through here (F-16.3). The rules it enforces:

- An agent asks for a **tier**, never a provider or a model. Go resolved the tier
  to a target before the request arrived, which is what keeps provider switching a
  settings change forever (plan.md sequencing rule 5).
- A structured response is **validated anyway**. ``.with_structured_output`` reduces
  malformed output; it does not eliminate it, especially where the provider only
  supports ``prompt_only`` (F-16.7).
- Cross-provider fallback fires on **retryable errors only**, never on a 400
  (ai-architecture.md 3.8), and the response records which provider actually
  served the call so cost attribution stays correct.
"""

from __future__ import annotations

import json
import logging
import time
from typing import Any

import jsonschema
from langchain_core.messages import BaseMessage

from . import caching
from .accounting import usage_from
from .errors import LLMError, classify
from .providers import build_client
from .schemas import (
    ChatRequest,
    ChatResponse,
    Message,
    ProviderKind,
    Role,
    ServedBy,
    StructuredOutputMode,
    Target,
    Usage,
)

logger = logging.getLogger(__name__)


async def chat(request: ChatRequest) -> ChatResponse:
    """Run one completion, with fallback and validation retries."""

    started = time.monotonic()

    try:
        response = await _attempt(request, request.target, fallback_used=False)
    except LLMError as err:
        if err.kind.value != "retryable" or request.fallback is None:
            raise

        # Availability handling, not refusal handling. The two are different
        # mechanisms and mixing them up is how a bad request gets billed twice.
        logger.warning(
            "primary provider unavailable, falling back",
            extra={"primary": request.target.provider.kind.value, "code": err.code},
        )
        response = await _attempt(request, request.fallback, fallback_used=True)

    response.latency_ms = int((time.monotonic() - started) * 1000)
    return response


async def _attempt(request: ChatRequest, target: Target, fallback_used: bool) -> ChatResponse:
    """One provider, with the validation retry loop around it.

    A cross-provider retry invalidates whatever cache the first provider had built,
    so the second attempt pays full input price. The cost estimate shown to a user
    must not assume otherwise.
    """

    client = build_client(target)
    messages = list(request.messages)

    total = Usage()
    validation_retries = 0
    last_error = ""

    for attempt in range(request.max_validation_retries + 1):
        rendered = caching.to_messages(messages, target)

        try:
            reply = await _invoke(client, rendered, request, target)
        except Exception as err:
            raise classify(err) from err

        usage = usage_from(reply)
        total = Usage(
            input_tokens=total.input_tokens + usage.input_tokens,
            output_tokens=total.output_tokens + usage.output_tokens,
            cache_read_tokens=total.cache_read_tokens + usage.cache_read_tokens,
            cache_write_tokens=total.cache_write_tokens + usage.cache_write_tokens,
        )

        text = _text_of(reply)
        if request.response_schema is None:
            return ChatResponse(
                text=text,
                usage=total,
                served_by=_served_by(target, fallback_used),
                validation_retries=validation_retries,
            )

        try:
            structured = _parse(text, request.response_schema, target)
        except LLMError as err:
            last_error = err.message
            validation_retries = attempt + 1
            if attempt >= request.max_validation_retries:
                break

            # The validation error goes back into the conversation, which is what
            # makes the retry different from simply asking again.
            messages = [
                *messages,
                Message(role=Role.assistant, content=text),
                Message(
                    role=Role.user,
                    content=(
                        "That response did not match the required schema: "
                        f"{err.message}\nReturn only valid JSON matching the schema."
                    ),
                ),
            ]
            continue

        return ChatResponse(
            text=text,
            structured=structured,
            usage=total,
            served_by=_served_by(target, fallback_used),
            validation_retries=validation_retries,
        )

    # Out of retries. A hard failure with a readable reason, and nothing partial
    # handed back for a caller to persist (F-16.7).
    raise LLMError.validation(
        f"The model did not return a valid response after {request.max_validation_retries + 1} "
        f"attempts: {last_error}",
        details={"attempts": request.max_validation_retries + 1},
    )


async def _invoke(
    client: Any,
    rendered: list[BaseMessage],
    request: ChatRequest,
    target: Target,
) -> BaseMessage:
    """Send the request, using native structured output where the model has it."""

    bound = client
    options: dict[str, Any] = {}

    if request.temperature is not None:
        options["temperature"] = request.temperature
    if target.model.effort and target.model.capabilities.effort_control:
        options["reasoning_effort"] = target.model.effort
    if request.cache_handle and target.provider.kind is ProviderKind.gemini:
        # Gemini references a cache object created once for the whole chain.
        options["cached_content"] = request.cache_handle

    if options:
        bound = bound.bind(**options)

    schema = _named_schema(request.response_schema)
    if schema is not None and target.model.capabilities.structured_output in (
        StructuredOutputMode.native,
        StructuredOutputMode.tool_based,
    ):
        # include_raw keeps the usage metadata, which a plain structured call
        # discards, and usage is the whole point of the accounting path.
        structured = bound.with_structured_output(schema, include_raw=True)
        result = await structured.ainvoke(rendered)
        if isinstance(result, dict) and "raw" in result:
            return result["raw"]
        return result  # type: ignore[return-value]

    return await bound.ainvoke(rendered)


def _named_schema(schema: dict[str, Any] | None) -> dict[str, Any] | None:
    """Give a bare JSON Schema the title the adapters insist on.

    A schema handed to ``with_structured_output`` is turned into a function or a
    response format, and both need a name. Agents write schemas that describe a
    shape rather than a function, so the name is supplied here instead of making
    every caller remember a field that has nothing to do with what it is asking
    for.
    """

    if schema is None or "title" in schema:
        return schema

    named = dict(schema)
    named["title"] = "Response"
    return named


def _text_of(reply: BaseMessage) -> str:
    content = reply.content
    if isinstance(content, str):
        return content

    # Anthropic-style block lists.
    parts: list[str] = []
    for block in content:
        if isinstance(block, str):
            parts.append(block)
        elif isinstance(block, dict) and block.get("type") == "text":
            parts.append(str(block.get("text", "")))
    return "".join(parts)


def _parse(text: str, schema: dict[str, Any], target: Target) -> dict[str, Any]:
    """Decode and validate, with a repair pass where the provider enforces nothing.

    ``prompt_only`` providers return prose around JSON often enough that a stricter
    extraction is worth having; a native provider that produced something invalid
    is a real failure and is not papered over.
    """

    candidate = text
    if target.model.capabilities.structured_output is StructuredOutputMode.prompt_only:
        candidate = _extract_json(text)

    try:
        parsed = json.loads(candidate)
    except json.JSONDecodeError as err:
        raise LLMError.validation(f"the response was not JSON ({err.msg})") from err

    if not isinstance(parsed, dict):
        raise LLMError.validation("the response was not a JSON object")

    try:
        jsonschema.validate(parsed, schema)
    except jsonschema.ValidationError as err:
        path = "/".join(str(part) for part in err.absolute_path) or "(root)"
        raise LLMError.validation(f"{path}: {err.message}") from err

    return parsed


def _extract_json(text: str) -> str:
    """Pull the JSON object out of a response that wrapped it in prose or fences."""

    stripped = text.strip()
    if stripped.startswith("```"):
        stripped = stripped.split("```")[1] if "```" in stripped[3:] else stripped[3:]
        if stripped.startswith("json"):
            stripped = stripped[4:]
        stripped = stripped.strip()

    start = stripped.find("{")
    end = stripped.rfind("}")
    if start != -1 and end > start:
        return stripped[start : end + 1]
    return stripped


def _served_by(target: Target, fallback_used: bool) -> ServedBy:
    return ServedBy(
        provider_kind=target.provider.kind,
        model_id=target.model.model_id,
        fallback_used=fallback_used,
    )
