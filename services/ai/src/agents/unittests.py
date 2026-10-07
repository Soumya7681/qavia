"""Unit test agent: a test for one untested function (F-6.3, BE-6.5).

One call per target, not one per repository. The reason is the same one that made the
design agent per-requirement: a call asked to write forty test files produces forty
mediocre ones, and the file that matters is the one for the function somebody is about
to change. The source of the target is a cached prefix, so the fan-out costs input
tokens at cache-read prices rather than full price (ai-architecture.md 3.6).

What the agent is **not** asked to do:

* choose the framework — the repository already said, in its own manifest, and
  detection read it (BE-6.3);
* guess whether its test passes — the runner runs it, and static validation in the
  image that owns the toolchain rejects a file that does not compile or that asserts
  nothing (BE-3.4);
* touch anything but the file it is writing. Nothing in this platform writes to a
  client's repository, and a generated unit test is a file in this platform's own
  store until somebody exports it.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "unittest"

PROMPT_VERSION = "unittest/v1"

SCHEMA: dict[str, Any] = {
    "title": "GeneratedUnitTest",
    "type": "object",
    "properties": {
        # Where the file belongs, in the convention the repository already uses. Go
        # validates it and refuses anything that escapes the workspace.
        "path": {"type": "string"},
        "content": {"type": "string"},
        # Which symbols the file actually tests. Filtered against what was offered, so
        # a file claiming coverage of a function it never imports does not count.
        "covered_symbols": {"type": "array", "items": {"type": "string"}},
        # What the agent could not test and why: a function needing a live database is
        # an honest gap, and a test that mocks one into meaninglessness is not.
        "skipped": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "symbol": {"type": "string"},
                    "reason": {"type": "string"},
                },
                "required": ["symbol", "reason"],
                "additionalProperties": False,
            },
        },
        "notes": {"type": "string"},
    },
    "required": ["path", "content", "covered_symbols", "skipped", "notes"],
    "additionalProperties": False,
}

# Per-framework conventions. Held here rather than left to the model, because the
# repository has a convention and a file that ignores it is a file a reviewer rejects
# for reasons that have nothing to do with the test.
_CONVENTIONS = {
    "vitest": (
        "Use vitest: import { describe, it, expect } from 'vitest'. Name the file "
        "alongside the source as <name>.test.ts."
    ),
    "jest": (
        "Use jest globals (describe, it, expect) without importing them. Name the file "
        "alongside the source as <name>.test.js, or .test.ts if the repository is "
        "TypeScript."
    ),
    "mocha": (
        "Use mocha with node:assert. Name the file under test/ as <name>.spec.js."
    ),
    "pytest": (
        "Use pytest: plain functions named test_*, plain assert statements. Name the "
        "file test_<module>.py next to the module or under tests/."
    ),
    "gotest": (
        "Use the standard library testing package. Name the file <name>_test.go in the "
        "same package as the source."
    ),
}

_RULES = (
    "Rules for the file you write:\n"
    "- Test behaviour through the public surface. A test that reaches into a private "
    "field breaks on the next refactor and tells you nothing about the contract.\n"
    "- Import the target by its real path, relative to the file you are writing. A "
    "wrong import is caught by the compiler and costs a regeneration.\n"
    "- Cover the cases that matter: the normal one, the boundary, and the error. Three "
    "real assertions beat ten that repeat each other.\n"
    "- Assert on specific values, not on truthiness. Never expect(true), never a bare "
    "assert on a variable, and never an empty test body.\n"
    "- No TODOs. A case you cannot implement goes in skipped with the reason.\n"
    "- Use only what the repository already depends on. A test importing a mocking "
    "library the project does not have does not run.\n"
    "- If testing the target honestly needs a database, a network call, or a clock you "
    "cannot control, say so in skipped rather than mocking it into a test that asserts "
    "your own mock."
)

_INSTRUCTION = (
    "Write one {framework} test file for the untested code below.\n\n"
    "Target file: {file}\n"
    "Untested symbols: {symbols}\n\n"
    "{convention}\n\n"
    "{rules}\n\n"
    "Set path to where the file belongs and covered_symbols to what it really tests."
)


def messages(
    source: dict[str, Any],
    framework: str,
    file: str,
    symbols: list[str],
    feedback: str = "",
) -> list[Message]:
    """Build the call for one target.

    ``source`` is the cacheable prefix: the target's own code, its neighbours, and the
    repository's test conventions. A regeneration after a validation failure reuses it,
    so a correction is a cache read plus a short instruction.
    """

    text = _INSTRUCTION.format(
        framework=framework or "the repository's own test framework",
        file=file,
        symbols=", ".join(symbols) or "the exported functions in this file",
        convention=_CONVENTIONS.get(framework, ""),
        rules=_RULES,
    )

    if feedback.strip():
        # The findings come from the real toolchain in the runner image: the file was
        # compiled and scanned, and these are what it said (BE-3.4.2).
        text += (
            "\n\nYour previous attempt was rejected by the validator that compiles and "
            "scans the file. Fix exactly these findings and return the whole file "
            f"again:\n{feedback.strip()}\n\n"
            "Do not silence a finding with a weaker assertion."
        )

    return [*cacheable(source), instruction(text)]
