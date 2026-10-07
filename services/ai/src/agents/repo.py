"""Repository comprehension agent: what is in this codebase (F-4.4, BE-6.4).

The agent explores rather than reads one prompt, because a repository does not fit in
a context window and the interesting parts are not knowable in advance: you find the
controllers, then look at what they call.

**Where the tools run is the design decision.** The plan's shape was a LangGraph
agent with local file, grep, and glob tools, on the grounds that the repository is
already on worker disk and putting a supervised process between a worker and its own
filesystem buys nothing (ai-architecture.md 5.2). That argument holds, and it points
at Go rather than at this service: the checkout is in a worker's per-job workspace,
the path validation that makes reading it safe already lives there
(internal/workspace), and this service runs as its own process that may not even
share a host.

So the loop is inverted. This endpoint answers one question per call — "given what
you have seen, what next" — and Go executes the tool and calls back with the result.
The properties the plan asked for are kept and one is gained:

* bounded iterations, enforced by the caller that owns the budget;
* real tools over the real checkout, not a summary guessed at in advance;
* **every path is validated by the process that owns the filesystem**, so a model
  asking for `../../../etc/passwd` is refused by the code whose job that is rather
  than by a sandbox this service would have to build.

The trade is more round trips. Each one is a cached-prefix call — the tree and the
findings so far are the prefix — so the cost is a cache read per step, not a full
prompt per step (ai-architecture.md 3.6).
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "repo"

PROMPT_VERSION = "repo/v1"

# One object, four actions. A union of schemas would be cleaner to read and is not
# reliably supported across providers; a discriminated single object is, and the
# caller ignores the fields that do not belong to the action.
SCHEMA: dict[str, Any] = {
    "title": "RepositoryStep",
    "type": "object",
    "properties": {
        "action": {
            "type": "string",
            "enum": ["read", "grep", "glob", "answer"],
        },
        # Why this step. Kept because it is the audit trail of an exploration: a map
        # nobody can retrace is a map nobody can check.
        "reason": {"type": "string"},
        # read: a repository-relative path. Validated by the caller, which owns the
        # workspace and will refuse anything outside it.
        "path": {"type": "string"},
        # grep: a plain substring or a simple regular expression, and an optional
        # glob to narrow which files are searched.
        "pattern": {"type": "string"},
        "include": {"type": "string"},
        # glob: a path pattern such as `src/**/*.controller.ts`.
        "glob": {"type": "string"},
        # answer: the map. Present only when action is answer.
        "map": {
            "type": "object",
            "properties": {
                "summary": {"type": "string"},
                "controllers": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "file": {"type": "string"},
                            "name": {"type": "string"},
                            "routes": {"type": "array", "items": {"type": "string"}},
                            "calls": {"type": "array", "items": {"type": "string"}},
                        },
                        "required": ["file", "name", "routes", "calls"],
                        "additionalProperties": False,
                    },
                },
                "services": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "file": {"type": "string"},
                            "name": {"type": "string"},
                            "responsibility": {"type": "string"},
                        },
                        "required": ["file", "name", "responsibility"],
                        "additionalProperties": False,
                    },
                },
                "data_access": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "file": {"type": "string"},
                            "name": {"type": "string"},
                            "tables": {"type": "array", "items": {"type": "string"}},
                        },
                        "required": ["file", "name", "tables"],
                        "additionalProperties": False,
                    },
                },
                # Paths with no test covering them, named as functions rather than as
                # a percentage: a number is the coverage tool's job, and this is the
                # list of things worth writing a test for (BE-6.5).
                "uncovered": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "file": {"type": "string"},
                            "symbol": {"type": "string"},
                            "why": {"type": "string"},
                        },
                        "required": ["file", "symbol", "why"],
                        "additionalProperties": False,
                    },
                },
                "unknowns": {
                    "type": "array",
                    "description": "What could not be determined, so the map is honest about its edges.",
                    "items": {"type": "string"},
                },
            },
            "required": ["summary", "controllers", "services", "data_access", "uncovered", "unknowns"],
            "additionalProperties": False,
        },
    },
    "required": ["action", "reason"],
    "additionalProperties": False,
}

_RULES = (
    "How to explore:\n"
    "- Start from the file tree and the detected stack. Read the entry points first, "
    "then follow what they call.\n"
    "- One action per step. Use grep to find where something is used and read to see "
    "how.\n"
    "- Paths are repository-relative. A path outside the repository is refused by the "
    "platform, and spending a step on one wastes the budget.\n"
    "- Name real files and real symbols. A map that lists a plausible file which does "
    "not exist is worse than a shorter map, because somebody will act on it.\n"
    "- Put what you could not work out in unknowns instead of guessing. An honest gap "
    "is useful; an invented service is not.\n"
    "- Answer before the budget runs out. A map from what you have seen beats being "
    "cut off mid-exploration."
)

_INSTRUCTION = (
    "You are mapping a repository so that tests can be written for it.\n\n"
    "Detected stack: {stack}\n"
    "Step {step} of at most {budget}.\n\n"
    "{history}"
    "{rules}\n\n"
    "Choose one action. When you have enough to describe the controllers, the "
    "services, the data access, and the untested paths, set action to answer and fill "
    "in the map."
)


def messages(
    tree: dict[str, Any],
    stack: str,
    step: int,
    budget: int,
    history: list[dict[str, Any]] | None = None,
) -> list[Message]:
    """Build one step of the exploration.

    ``tree`` is the cacheable prefix: the file listing and the detected stack do not
    change between steps of one pass, so thirty steps over one repository are one
    cached prefix plus thirty short suffixes rather than thirty full prompts.
    """

    rendered = ""
    if history:
        lines = []
        for entry in history:
            action = entry.get("action", "")
            detail = entry.get("detail", "")
            result = str(entry.get("result", "")).rstrip()
            lines.append(f"### {action} {detail}\n{result}")
        rendered = "What you have looked at so far:\n\n" + "\n\n".join(lines) + "\n\n"

    return [
        *cacheable(tree),
        instruction(
            _INSTRUCTION.format(
                stack=stack or "not detected",
                step=step,
                budget=budget,
                history=rendered,
                rules=_RULES,
            )
        ),
    ]
