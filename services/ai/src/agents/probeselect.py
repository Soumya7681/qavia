"""Security probe selection agent: which endpoint, which parameter, which reviewed
payload (F-11.3, BE-9.3).

The whole design of this agent is a subtraction. It does **not** write payload strings.
The payloads live in a reviewed Go library — SQLi, XSS, JWT tampering, IDOR, and the
rest — versioned like any other code, and this agent is shown a catalogue of their IDs
and purposes with the strings withheld. Its output is a plan: for each probe, an
endpoint, a parameter, and a payload *ID* from the catalogue. A plan that named a raw
payload would be refused by the Go side, because the point of splitting it this way is
that no attack string this platform sends was ever chosen by a model (BE-9.3.2).

Why a model at all, then, if it cannot write the interesting part? Because choosing
*where* to aim is judgement a list cannot encode: an `id` path parameter is worth an
IDOR probe and a `search` query parameter is worth an injection probe, and which
endpoint is worth authentication testing depends on what it does. The model reads the
spec and matches reviewed weapons to plausible targets. The plan is schema-locked and
reviewable before anything is sent — which matters more here than anywhere else in the
platform, because what it authorises is traffic that, pointed at the wrong host, is an
attack.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "probeselect"

PROMPT_VERSION = "probeselect/v1"

SCHEMA: dict[str, Any] = {
    "title": "SecurityProbePlan",
    "type": "object",
    "properties": {
        "summary": {"type": "string"},
        "probes": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "endpoint": {
                        "type": "string",
                        "description": "METHOD /path from the specification.",
                    },
                    # Where the payload goes: a parameter name, or empty for the
                    # deliveries that target the whole request (omit auth, repeat).
                    "parameter": {"type": "string"},
                    # A payload ID from the catalogue. NOT a payload string — the
                    # platform refuses a plan that supplies one.
                    "payload_id": {"type": "string"},
                    "rationale": {
                        "type": "string",
                        "description": "Why this endpoint and parameter are worth this probe.",
                    },
                },
                "required": ["endpoint", "payload_id", "rationale"],
                "additionalProperties": False,
            },
        },
        # Endpoints the agent judged not worth probing, and why, so the plan is honest
        # about its coverage rather than silently thin.
        "skipped": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "endpoint": {"type": "string"},
                    "reason": {"type": "string"},
                },
                "required": ["endpoint", "reason"],
                "additionalProperties": False,
            },
        },
    },
    "required": ["summary", "probes", "skipped"],
    "additionalProperties": False,
}

_RULES = (
    "How to choose:\n"
    "- Every probe names a payload_id FROM THE CATALOGUE. You never write a payload "
    "string: the strings are a reviewed library and are deliberately not shown to you. "
    "A plan that invents one is rejected.\n"
    "- Match the weakness to the target. A path with an {id} is worth an IDOR probe. A "
    "query or body parameter that is searched or stored is worth an injection or XSS "
    "probe. An endpoint that changes state is worth a CSRF probe. A protected endpoint "
    "is worth a broken-auth probe. A token-authenticated endpoint is worth JWT "
    "tampering.\n"
    "- Name the exact parameter for a parameter-delivered payload, so the probe hits a "
    "real field rather than a guessed one.\n"
    "- Do not probe a destructive write — deleting or paying — even for a security "
    "test: an IDOR probe that deletes somebody else's record is the damage the "
    "vulnerability would cause, done by us.\n"
    "- Prefer a focused plan. Ten well-aimed probes find more than every payload "
    "against every parameter, which is a scanner's noise.\n"
    "- Put endpoints not worth probing in skipped, so the plan's coverage is honest."
)

_INSTRUCTION = (
    "Choose security probes for this API.\n\n"
    "Categories to consider: {categories}\n\n"
    "Payload catalogue (IDs and purposes; the strings are withheld):\n{catalogue}\n\n"
    "{rules}\n\n"
    "Return the plan: for each probe an endpoint, a parameter where one applies, a "
    "payload_id from the catalogue, and a rationale."
)


def messages(
    endpoints: dict[str, Any],
    catalogue: list[dict[str, Any]],
    categories: list[str],
) -> list[Message]:
    """Build the call.

    ``endpoints`` is the cacheable prefix. The catalogue goes in the instruction rather
    than the prefix because a run may narrow the categories, and the withheld strings
    make it small enough that this costs nothing.
    """

    rendered = []
    for entry in catalogue:
        parts = [f"- {entry.get('id', '')}"]
        if entry.get("category"):
            parts.append(f"[{entry['category']}]")
        if entry.get("delivery"):
            parts.append(f"delivery={entry['delivery']}")
        if entry.get("purpose"):
            parts.append(f"— {entry['purpose']}")
        rendered.append(" ".join(str(part) for part in parts))

    return [
        *cacheable(endpoints),
        instruction(
            _INSTRUCTION.format(
                categories=", ".join(categories) or "all",
                catalogue="\n".join(rendered) or "(empty)",
                rules=_RULES,
            )
        ),
    ]
