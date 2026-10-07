"""Extract agent: what the specification actually says (F-4.1, F-4.2).

Single-shot and schema-locked. Not LangGraph: one call in, one validated object
out, because there is nothing to iterate on. The input is the **parsed and
normalized** model rather than the raw file, so the model is never asked to read
JSON structure that deterministic code already read correctly.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

# The tier this agent asks for. Extraction is the step everything downstream
# depends on: a cheap model that misses a business rule costs more than it saves.
TIER = "reasoning"

AGENT = "extract"

SCHEMA: dict[str, Any] = {
    "title": "ExtractedRequirements",
    "type": "object",
    "properties": {
        "requirements": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "kind": {
                        "type": "string",
                        "enum": [
                            "feature",
                            "business_rule",
                            "validation_rule",
                            "auth",
                            "edge_case",
                        ],
                    },
                    "title": {"type": "string"},
                    "body": {"type": "string"},
                    # Which endpoint this is about, as "METHOD /path", or empty for
                    # a rule that spans the API.
                    "endpoint": {"type": "string"},
                    # Where in the input it came from. An item that cannot cite its
                    # source is a guess, and the reviewer needs to know which.
                    "source_ref": {"type": "string"},
                },
                "required": ["kind", "title", "body", "endpoint", "source_ref"],
                "additionalProperties": False,
            },
        }
    },
    "required": ["requirements"],
    "additionalProperties": False,
}

INSTRUCTION = (
    "Extract what this specification requires, as a flat list.\n"
    "Cover:\n"
    "- features: what each endpoint is for, one per operation\n"
    "- business_rule: rules the API enforces beyond its schema\n"
    "- validation_rule: per-field constraints, one per constrained field\n"
    "- auth: which operations need which scheme, and what happens without it\n"
    "- edge_case: boundaries, conflicts, and states worth testing that the "
    "specification implies but does not state\n\n"
    "For each item set endpoint to \"METHOD /path\" where it belongs to one "
    "operation, and to an empty string where it does not. Set source_ref to the "
    "source_ref of the endpoint it came from, or an empty string.\n"
    "Do not invent endpoints or fields. Do not restate the same rule twice."
)


def messages(document: dict[str, Any]) -> list[Message]:
    """Build the call.

    The specification is the cacheable prefix and the instruction follows it, so a
    later agent working on the same document reuses the same cached prefix.
    """

    return [*cacheable(document), instruction(INSTRUCTION)]
