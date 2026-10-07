"""Prompt construction, with the cache boundary as the organising idea.

Fan-out is the cost story: one specification, then a call per requirement sharing
that specification as a prefix. Caching is what makes that affordable, and it only
works if the prefix is **byte-identical** across every call in the run
(ai-architecture.md 3.6).

So the rule this module exists to enforce: nothing above the cache boundary may
vary between calls. No timestamps, no UUIDs, no per-call identifiers, no dictionary
iteration order. Everything variable goes in the message after the cacheable one.
"""

from __future__ import annotations

import json
from typing import Any

from ..llm.schemas import Message, Role

# The system prompt every generation agent shares. Constant text, so it sits at
# the very front of the cached prefix.
SYSTEM = (
    "You are a senior QA engineer. You read API specifications and produce test "
    "artefacts that another engineer could run without rewriting them.\n"
    "Rules you always follow:\n"
    "- Every item you produce cites the source it came from.\n"
    "- You never invent an endpoint, a field, or a status code that is not in the "
    "input.\n"
    "- You prefer specific assertions over vague ones: a status code and a field, "
    "not \"works correctly\".\n"
    "- You answer with the requested JSON object and nothing else."
)


def specification_block(document: dict[str, Any]) -> str:
    """Render the parsed specification as the cacheable prefix.

    ``sort_keys`` and a fixed separator are not tidiness. Python dictionary order
    follows insertion, and an input assembled in a different order on the second
    call produces a different prefix, which silently costs full price for the rest
    of the fan-out with no error anywhere.
    """

    return "API specification (parsed and normalized):\n" + json.dumps(
        document, sort_keys=True, indent=2, separators=(",", ": ")
    )


def cacheable(document: dict[str, Any]) -> list[Message]:
    """The prefix every call in one run shares."""

    return [
        Message(role=Role.system, content=SYSTEM, cacheable=True),
        Message(role=Role.user, content=specification_block(document), cacheable=True),
    ]


def instruction(text: str) -> Message:
    """The part that differs per call. Never marked cacheable."""

    return Message(role=Role.user, content=text, cacheable=False)
