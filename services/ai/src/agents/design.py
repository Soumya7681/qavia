"""Design agent: test cases for one requirement (F-5.1, F-5.2).

Single-shot, schema-locked, and called **once per requirement** rather than once
per specification. That is deliberate: one call per requirement keeps each output
small enough to stay accurate, and the specification is a cached prefix shared
across the whole fan-out, so the extra calls cost input tokens at cache-read
prices rather than full price (ai-architecture.md 3.6).

Bounding that fan-out is Go's job: ``errgroup.SetLimit`` with the configured
concurrency, so a 400-endpoint specification does not launch 400 concurrent
provider calls (BE-2.7).
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "reasoning"

AGENT = "design"

SCHEMA: dict[str, Any] = {
    "title": "DesignedTestCases",
    "type": "object",
    "properties": {
        "test_cases": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "title": {"type": "string"},
                    "preconditions": {"type": "string"},
                    "steps": {
                        "type": "array",
                        "items": {
                            "type": "object",
                            "properties": {
                                "action": {"type": "string"},
                                "data": {"type": "string"},
                                "expected": {"type": "string"},
                            },
                            "required": ["action", "data", "expected"],
                            "additionalProperties": False,
                        },
                    },
                    "expected": {"type": "string"},
                    "priority": {
                        "type": "string",
                        "enum": ["critical", "high", "medium", "low"],
                    },
                    "category": {
                        "type": "string",
                        "enum": [
                            "functional",
                            "negative",
                            "boundary",
                            "security",
                            "auth",
                            "performance",
                            "data",
                        ],
                    },
                    # The endpoint and the assertion together are what Go hashes
                    # into the fingerprint, so two wordings of the same check
                    # collide instead of both being stored (F-5.3).
                    "endpoint": {"type": "string"},
                    "assertion": {"type": "string"},
                },
                "required": [
                    "title",
                    "preconditions",
                    "steps",
                    "expected",
                    "priority",
                    "category",
                    "endpoint",
                    "assertion",
                ],
                "additionalProperties": False,
            },
        }
    },
    "required": ["test_cases"],
    "additionalProperties": False,
}

_INSTRUCTION = (
    "Design test cases for this one requirement:\n\n"
    "kind: {kind}\n"
    "title: {title}\n"
    "detail: {body}\n"
    "endpoint: {endpoint}\n\n"
    "Cover, where the requirement calls for it:\n"
    "- the happy path\n"
    "- each validation rule it states, one case per rule\n"
    "- each authentication and authorisation state: missing, invalid, expired, "
    "and wrong-owner where the endpoint takes an identifier\n"
    "- boundary values for every bound the schema gives: minimum, maximum, one "
    "below, one above, empty, and maximum length\n"
    "- negatives: wrong types, missing required fields, malformed payloads\n\n"
    "Rules:\n"
    "- Every case asserts a specific status code and, where there is a body, a "
    "specific field.\n"
    "- assertion is a short stable phrase naming what is checked, such as "
    "\"rejects missing title with 400\". Two cases that check the same thing must "
    "produce the same assertion text.\n"
    "- endpoint is \"METHOD /path\" from the specification above.\n"
    "- Do not produce cases for behaviour the specification does not describe.\n"
    "- Between four and twelve cases. Fewer if the requirement is narrow."
)


def messages(document: dict[str, Any], requirement: dict[str, Any]) -> list[Message]:
    """Build the call for one requirement.

    The requirement goes in the instruction, below the cache boundary. Putting it
    in the prefix would give every requirement a different prefix and turn a
    cached fan-out into a full-price one.
    """

    return [
        *cacheable(document),
        instruction(
            _INSTRUCTION.format(
                kind=requirement.get("kind", ""),
                title=requirement.get("title", ""),
                body=requirement.get("body", ""),
                endpoint=requirement.get("endpoint", "") or "(not endpoint-specific)",
            )
        ),
    ]
