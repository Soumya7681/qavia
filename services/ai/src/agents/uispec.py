"""Playwright spec agent: one discovered flow becomes one runnable spec (F-6.4,
BE-7.4).

One call per flow rather than one per application, for the reason every fan-out in
this platform is per-item: a call asked to write the whole suite writes twelve
mediocre specs, and the one that matters is the flow somebody is about to change. The
flow graph is the cached prefix, so twelve flows cost one cached prefix plus twelve
short instructions (ai-architecture.md 3.6).

**The selector policy is not this prompt's job to guarantee.** It is stated here
because a generator that starts inside the rule needs fewer corrections, but it is
enforced in Go, on every locator, after generation: a spec that reaches for
`nth-child` is rejected and regenerated rather than merged with a warning
(internal/uitests/selectors.go, BE-7.4.2). That split is deliberate. "Prefer
data-testid" as an instruction produces a suite that mostly prefers it, and the one
spec in twenty that locates a button by its position breaks the first time somebody
adds a wrapper div — in a way that looks like a real failure.

What the agent is **not** asked to do:

* invent a journey — the flow was walked in a real browser, and a spec for a route
  nobody opened fails on its first navigation (BE-7.2);
* guess whether its spec passes — the runner runs it, and static validation in the
  image that owns the toolchain rejects a file that does not compile (BE-3.4);
* handle credentials — the two placeholders are substituted from the environment at
  run time, and a password in a generated file is a password in version control
  (BE-7.3.2).
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "uispec"

PROMPT_VERSION = "uispec/v1"

SCHEMA: dict[str, Any] = {
    "title": "GeneratedUISpec",
    "type": "object",
    "properties": {
        # Where the file belongs. Go validates it: a path outside the suite directory
        # is refused by the code that owns the filesystem.
        "path": {"type": "string"},
        "content": {"type": "string"},
        # Which steps of the flow the spec actually covers, so a spec claiming a
        # journey it skipped half of does not count as that journey.
        "covered_steps": {"type": "array", "items": {"type": "string"}},
        # What could not be expressed and why. An honest gap beats a test that clicks
        # something and asserts nothing.
        "skipped": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "step": {"type": "string"},
                    "reason": {"type": "string"},
                },
                "required": ["step", "reason"],
                "additionalProperties": False,
            },
        },
        "notes": {"type": "string"},
    },
    "required": ["path", "content", "covered_steps", "skipped", "notes"],
    "additionalProperties": False,
}

_RULES = (
    "How to write it:\n"
    "- One `test(...)` per flow, in TypeScript, using @playwright/test. Import "
    "{ test, expect } from '@playwright/test'.\n"
    "- Locate elements in this order and no other: getByTestId, then getByRole with "
    "its accessible name, then getByLabel, then getByPlaceholder, then getByText. "
    "The platform checks every locator and regenerates a spec that breaks the order, "
    "so a positional selector costs a round trip and reaches nobody.\n"
    "- Never use a CSS selector with nth-child or a relative, a numeric .nth(n), a "
    "structural pseudo-class, a chain of child combinators, a bare tag, XPath, or a "
    "chained generated class name.\n"
    "- Never use waitForTimeout. Wait for the thing itself: await "
    "expect(locator).toBeVisible() or locator.waitFor().\n"
    "- Every step's expectation from the graph becomes a real assertion. A step that "
    "clicks and asserts nothing passes on a blank page.\n"
    "- The base URL comes from the test runner's configuration, so navigate with "
    "paths: await page.goto('/orders').\n"
    "- Credentials are process.env.QAVIA_AUTH_USERNAME and "
    "process.env.QAVIA_AUTH_PASSWORD. Wherever the flow says $QAVIA_USERNAME or "
    "$QAVIA_PASSWORD, read the environment variable. Never write a literal "
    "credential into the file.\n"
    "- Do not write a step the flow does not contain, and do not navigate to a page "
    "the graph does not list. Both fail on the first run and cost somebody an hour."
)

_INSTRUCTION = (
    "Write a Playwright spec for one discovered flow.\n\n"
    "Flow: {name}\n"
    "Purpose: {purpose}\n"
    "Needs authentication: {auth}\n\n"
    "Steps, as they were walked in a real browser:\n{steps}\n\n"
    "{rules}\n\n"
    "{feedback}"
    "Return the file's path and its content."
)

_FEEDBACK = (
    "A previous attempt was rejected. Fix exactly this and change nothing else:\n\n"
    "{feedback}\n\n"
)


def messages(
    graph: dict[str, Any],
    flow: dict[str, Any],
    feedback: str = "",
) -> list[Message]:
    """Build the call for one flow.

    ``graph`` is the cacheable prefix: the pages, what can be done on each, and the
    target do not change between flows of one application, so a suite of twelve specs
    is one cached prefix plus twelve short instructions. A regeneration after a
    rejected selector reuses the same prefix, which is what makes a correction cost a
    cache read rather than the whole graph again.
    """

    rendered = []
    for index, step in enumerate(flow.get("steps") or [], start=1):
        parts = [f"{index}. {step.get('action', '')}"]
        for key in ("target", "testid", "role", "name", "label", "value"):
            if step.get(key):
                parts.append(f"{key}={step[key]}")
        if step.get("expect"):
            parts.append(f"expect: {step['expect']}")
        rendered.append(" ".join(parts))

    return [
        *cacheable(graph),
        instruction(
            _INSTRUCTION.format(
                name=flow.get("name", "an unnamed flow"),
                purpose=flow.get("purpose", ""),
                auth="yes" if flow.get("requires_auth") else "no",
                steps="\n".join(rendered) or "(the flow recorded no steps)",
                rules=_RULES,
                feedback=_FEEDBACK.format(feedback=feedback) if feedback else "",
            )
        ),
    ]
