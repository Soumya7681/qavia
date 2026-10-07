"""Dedupe agent: the near-miss fallback only (F-5.4).

Exact duplicates never reach here. A deterministic fingerprint over
``(method, path, assertion)`` catches those for free, and the unique index refuses
them at write time (BE-2.8). This runs on the narrow set that survives: two cases
for the same requirement, worded differently, that may or may not check the same
thing.

Hence the ``cheap`` tier. It is a yes-or-no question about two short strings, and
paying reasoning prices for it across a 400-case run would cost more than the
duplicates.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message, Role

TIER = "cheap"

AGENT = "dedupe"

SCHEMA: dict[str, Any] = {
    "title": "DuplicateVerdict",
    "type": "object",
    "properties": {
        "duplicates": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "candidate_index": {"type": "integer"},
                    "duplicate_of_index": {"type": "integer"},
                    "reason": {"type": "string"},
                },
                "required": ["candidate_index", "duplicate_of_index", "reason"],
                "additionalProperties": False,
            },
        }
    },
    "required": ["duplicates"],
    "additionalProperties": False,
}

_SYSTEM = (
    "You decide whether two test cases check the same thing. Two cases are "
    "duplicates when they exercise the same endpoint with the same intent and "
    "assert the same outcome, however differently they are worded. They are not "
    "duplicates when they differ in input, in expected status, or in the state "
    "they set up."
)


def messages(existing: list[dict[str, Any]], candidates: list[dict[str, Any]]) -> list[Message]:
    """Ask which candidates duplicate something already stored.

    Indices rather than identifiers: an identifier in the prompt is a per-call
    value, and this prompt is short enough that the saving is irrelevant next to
    the risk of a model echoing a UUID back wrongly.
    """

    lines = ["Already stored:"]
    lines += [
        f"  [{index}] {case.get('title', '')} — asserts: {case.get('assertion', '')}"
        for index, case in enumerate(existing)
    ]
    lines.append("")
    lines.append("Candidates:")
    lines += [
        f"  [{index}] {case.get('title', '')} — asserts: {case.get('assertion', '')}"
        for index, case in enumerate(candidates)
    ]
    lines.append("")
    lines.append(
        "Return the candidates that duplicate a stored case. "
        "candidate_index indexes the candidate list, duplicate_of_index the stored "
        "list. Return an empty array when none do."
    )

    return [
        Message(role=Role.system, content=_SYSTEM, cacheable=False),
        Message(role=Role.user, content="\n".join(lines), cacheable=False),
    ]
