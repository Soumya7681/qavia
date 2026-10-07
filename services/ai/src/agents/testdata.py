"""Test data agent: the handful of fields a faker cannot write (F-10.2, BE-8.2).

**This agent is deliberately small, and the reason is a cost argument rather than a
taste.** A hundred records of twenty fields is two thousand values. A seeded faker
produces them instantly, free, and identically on every run — which is what makes a
failing test reproducible. A model asked for the same two thousand values costs money
per record, takes seconds, and is occasionally wrong in a way nobody notices until an
assertion fails against it (ai-architecture.md 2).

What a model is genuinely better at is narrow and worth paying for: an address that
reads like somewhere a person lives, a product description that matches the product, a
tax identifier whose shape a validator in a country the faker knows nothing about will
actually accept. So this agent is asked for **named fields only**, one call for the
whole set rather than one per record, on the cheap tier. Whatever it does not answer
keeps the faker's value, so a provider outage costs realism on a few columns rather
than the whole request.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "cheap"

AGENT = "testdata"

PROMPT_VERSION = "testdata/v1"

SCHEMA: dict[str, Any] = {
    "title": "TestDataValues",
    "type": "object",
    "properties": {
        "fields": {
            "type": "array",
            "description": "One entry per field asked for, each with one value per record.",
            "items": {
                "type": "object",
                "properties": {
                    "name": {"type": "string"},
                    "values": {
                        "type": "array",
                        "items": {"type": "string"},
                    },
                },
                "required": ["name", "values"],
                "additionalProperties": False,
            },
        },
        # What could not be written plausibly, and why. An honest gap keeps the
        # faker's value; an invented one replaces a usable value with a worse one.
        "skipped": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "name": {"type": "string"},
                    "reason": {"type": "string"},
                },
                "required": ["name", "reason"],
                "additionalProperties": False,
            },
        },
    },
    "required": ["fields", "skipped"],
    "additionalProperties": False,
}

_RULES = (
    "Rules:\n"
    "- Exactly {count} values per field, in order. Fewer is fine and the rest keep "
    "their generated values; more will be ignored.\n"
    "- Respect the field's type, format, pattern, and maximum length. A value that "
    "fails the schema tests the validator rather than the endpoint.\n"
    "- Vary them. Ten records with the same city is not test data.\n"
    "- **Never a real person, a real company, a real address, or a real identifier.** "
    "Plausible and invented, not recalled.\n"
    "- **Never a payment card number.** Those come only from published test ranges "
    "and the platform supplies them itself.\n"
    "- If a field is one you cannot write plausibly, put it in skipped rather than "
    "guessing: the generated value it already has is better than an invented one."
)

_INSTRUCTION = (
    "Write realistic values for these fields of a test record.\n\n"
    "What the record describes: {context}\n"
    "Locale: {locale}\n"
    "Records needed: {count}\n\n"
    "Fields:\n{fields}\n\n"
    "{rules}"
)


def messages(
    fields: list[dict[str, Any]],
    count: int,
    locale: str,
    context: dict[str, Any] | None = None,
) -> list[Message]:
    """Build the call.

    ``context`` is the cacheable prefix: what the object is and which fields it has.
    Generating a second batch for the same schema is then a cache read plus a short
    instruction rather than the whole description again (ai-architecture.md 3.6).
    """

    rendered = []
    for field in fields:
        parts = [f"- {field.get('name', '')}"]
        for key in ("type", "format", "pattern", "maxLength"):
            if field.get(key):
                parts.append(f"{key}={field[key]}")
        if field.get("description"):
            parts.append(f"— {field['description']}")
        rendered.append(" ".join(str(part) for part in parts))

    return [
        *cacheable(context or {}),
        instruction(
            _INSTRUCTION.format(
                context=(context or {}).get("description") or "a record from an API schema",
                locale=locale or "not specified",
                count=count,
                fields="\n".join(rendered) or "(none)",
                rules=_RULES.format(count=count),
            )
        ),
    ]
