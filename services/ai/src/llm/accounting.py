"""Usage is returned, never persisted.

Go writes ``llm_calls`` (backend-standards.md 10). This module only reads what a
provider reported and normalises it into one shape, because every provider names
its token counts differently and a caller comparing them would be comparing
vocabulary rather than cost.
"""

from __future__ import annotations

from typing import Any

from langchain_core.messages import BaseMessage

from .schemas import Usage

# Where each provider hides its cache counters. Anthropic reports cache creation
# and cache reads separately; OpenAI reports a cached-token subtotal of the input;
# Gemini reports cached content tokens. Nothing here invents a number: a provider
# that reports none leaves the field at zero, and a zero means "not reported"
# rather than "no caching happened".
_INPUT_KEYS = ("input_tokens", "prompt_tokens", "promptTokenCount")
_OUTPUT_KEYS = ("output_tokens", "completion_tokens", "candidatesTokenCount")
_CACHE_READ_KEYS = (
    "cache_read_input_tokens",
    "cache_read",
    "cached_tokens",
    "cachedContentTokenCount",
)
_CACHE_WRITE_KEYS = ("cache_creation_input_tokens", "cache_creation", "cache_write")


def usage_from(message: BaseMessage) -> Usage:
    """Read the token counts off a provider response."""

    metadata: dict[str, Any] = dict(getattr(message, "usage_metadata", None) or {})
    if not metadata:
        response_metadata = getattr(message, "response_metadata", None) or {}
        metadata = dict(
            response_metadata.get("usage") or response_metadata.get("token_usage") or {}
        )

    details: dict[str, Any] = {}
    for key in ("input_token_details", "output_token_details", "cache_tokens_details"):
        value = metadata.get(key)
        if isinstance(value, dict):
            details.update(value)

    return Usage(
        input_tokens=_first(metadata, _INPUT_KEYS),
        output_tokens=_first(metadata, _OUTPUT_KEYS),
        cache_read_tokens=_first(metadata, _CACHE_READ_KEYS) or _first(details, _CACHE_READ_KEYS),
        cache_write_tokens=_first(metadata, _CACHE_WRITE_KEYS)
        or _first(details, _CACHE_WRITE_KEYS),
    )


def _first(source: dict[str, Any], keys: tuple[str, ...]) -> int:
    for key in keys:
        value = source.get(key)
        if isinstance(value, bool):
            continue
        if isinstance(value, int):
            return value
        if isinstance(value, float):
            return int(value)
    return 0
