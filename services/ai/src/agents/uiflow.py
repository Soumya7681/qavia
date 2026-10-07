"""UI flow discovery agent: what can a user actually do in this application
(F-8.1, F-8.2, F-8.3, BE-7.2).

It drives a real browser rather than reading component source, and that is the whole
point of the task. Source tells you which routes a router declares; it does not tell
you that three of them redirect to a login, that one renders only for an admin, or that
the form on the fourth fails silently without a tenant header. Runtime behaviour and
conditional rendering are exactly what a generated UI suite has to know, and exactly
what static reading misses (BE-7.2.2).

**Where the browser lives is the same decision as the repository tools.** The container
holding it is opened by the worker, whose egress allowlist, credentials, and reaping
already govern it, so the loop runs in Go and this endpoint answers one step at a time.
What that buys, beyond not needing a shared browser process: the action vocabulary is
fixed and validated by the platform, and the credentials are substituted inside the
container, so the thing choosing what to type never sees a password (BE-7.3.2).

The agent is asked for a flow graph, not a list of URLs. A route with no way to reach it
is not a flow, and a suite generated from unreachable routes fails on its first
navigation.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "uiflow"

PROMPT_VERSION = "uiflow/v1"

SCHEMA: dict[str, Any] = {
    "title": "UIFlowStep",
    "type": "object",
    "properties": {
        "action": {
            "type": "string",
            "enum": ["goto", "click", "fill", "press", "back", "snapshot", "screenshot", "answer"],
        },
        # Why this step: the audit trail of a discovery. A graph nobody can retrace is a
        # graph nobody can check.
        "reason": {"type": "string"},
        "url": {"type": "string"},
        # The element to act on, named the way the selector policy prefers, so what
        # discovery records can become a spec without inventing a locator (BE-7.4.2).
        "testid": {"type": "string"},
        "role": {"type": "string"},
        "name": {"type": "string"},
        "label": {"type": "string"},
        "text": {"type": "string"},
        # What to type. The literals $QAVIA_USERNAME and $QAVIA_PASSWORD are substituted
        # inside the browser container; their values are never shown here.
        "value": {"type": "string"},
        "key": {"type": "string"},
        "screenshot": {"type": "string"},
        "graph": {
            "type": "object",
            "properties": {
                "summary": {"type": "string"},
                "pages": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "path": {"type": "string"},
                            "title": {"type": "string"},
                            "purpose": {"type": "string"},
                            "requires_auth": {"type": "boolean"},
                            "actions": {
                                "type": "array",
                                "description": "What a user can do here, each named by a stable locator.",
                                "items": {
                                    "type": "object",
                                    "properties": {
                                        "description": {"type": "string"},
                                        "testid": {"type": "string"},
                                        "role": {"type": "string"},
                                        "name": {"type": "string"},
                                        "label": {"type": "string"},
                                        "leads_to": {"type": "string"},
                                    },
                                    "required": ["description"],
                                    "additionalProperties": False,
                                },
                            },
                        },
                        "required": ["path", "title", "purpose", "requires_auth", "actions"],
                        "additionalProperties": False,
                    },
                },
                "flows": {
                    "type": "array",
                    "description": "Journeys worth a test, each a sequence that was actually walked.",
                    "items": {
                        "type": "object",
                        "properties": {
                            "name": {"type": "string"},
                            "purpose": {"type": "string"},
                            "requires_auth": {"type": "boolean"},
                            "steps": {
                                "type": "array",
                                "items": {
                                    "type": "object",
                                    "properties": {
                                        "action": {"type": "string"},
                                        "target": {"type": "string"},
                                        "testid": {"type": "string"},
                                        "role": {"type": "string"},
                                        "name": {"type": "string"},
                                        "label": {"type": "string"},
                                        "value": {"type": "string"},
                                        "expect": {"type": "string"},
                                    },
                                    "required": ["action", "expect"],
                                    "additionalProperties": False,
                                },
                            },
                        },
                        "required": ["name", "purpose", "requires_auth", "steps"],
                        "additionalProperties": False,
                    },
                },
                "unreachable": {
                    "type": "array",
                    "description": "Paths seen in links but never successfully opened, with why.",
                    "items": {"type": "string"},
                },
                "unknowns": {
                    "type": "array",
                    "description": "What could not be determined, so the graph is honest about its edges.",
                    "items": {"type": "string"},
                },
            },
            "required": ["summary", "pages", "flows", "unreachable", "unknowns"],
            "additionalProperties": False,
        },
    },
    "required": ["action", "reason"],
    "additionalProperties": False,
}

_RULES = (
    "How to explore:\n"
    "- Start at the application's root and follow what is on the page. One action per "
    "step.\n"
    "- Prefer a testid, then a role with its accessible name, then a label, then text. "
    "That is the order the platform's selector policy enforces, so an element recorded "
    "another way cannot become a test.\n"
    "- When you meet a sign-in form, fill it with $QAVIA_USERNAME and $QAVIA_PASSWORD "
    "and submit. Those placeholders are substituted inside the browser; the real values "
    "are never shown to you and must never be guessed at.\n"
    "- A destructive-looking action — delete, remove, cancel a subscription, empty a "
    "cart — is recorded as an action on the page and NOT clicked. This is somebody's "
    "environment.\n"
    "- Record a flow only if you walked it. A journey assembled from routes you never "
    "opened is a test that fails on its first navigation.\n"
    "- Put what you could not reach in unreachable, and what you could not work out in "
    "unknowns. An honest gap is useful; an invented page is not.\n"
    "- Answer before the budget runs out. A graph from what you saw beats being cut off."
)

_INSTRUCTION = (
    "You are mapping a web application so that UI tests can be generated for it.\n\n"
    "Target: {target}\n"
    "Authentication: {auth}\n"
    "Step {step} of at most {budget}.\n\n"
    "{history}"
    "{rules}\n\n"
    "Choose one action. When you have enough to describe the pages, what a user can do "
    "on each, and the journeys worth testing, set action to answer and fill in the graph."
)


def messages(
    context: dict[str, Any],
    target: str,
    auth: str,
    step: int,
    budget: int,
    history: list[dict[str, Any]] | None = None,
) -> list[Message]:
    """Build one step of a discovery.

    ``context`` is the cacheable prefix: the target, the authentication mode, and
    whatever the platform already knows about the application. Forty steps against one
    application are one cached prefix plus forty short suffixes rather than forty full
    prompts (ai-architecture.md 3.6).
    """

    rendered = ""
    if history:
        entries = []
        for entry in history:
            action = entry.get("action", "")
            detail = entry.get("detail", "")
            result = str(entry.get("result", "")).rstrip()
            entries.append(f"### {action} {detail}\n{result}")
        rendered = "What you have seen so far:\n\n" + "\n\n".join(entries) + "\n\n"

    return [
        *cacheable(context),
        instruction(
            _INSTRUCTION.format(
                target=target or "the project's configured target",
                auth=auth or "none",
                step=step,
                budget=budget,
                history=rendered,
                rules=_RULES,
            )
        ),
    ]
