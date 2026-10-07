"""Codegen agent: approved test cases become a runnable file (F-6.1).

Single-shot, on the ``code`` tier. The envelope is schema-locked — path,
framework, covered case IDs — while the content is code and cannot be. That split
matters: Go has to know which file this is and which cases it covers to store it
and to trace it, and none of that can be recovered reliably from prose around a
code block.

One call per endpoint, with the specification as the cached prefix. Batching every
endpoint into one call would produce a single enormous file, lose the per-endpoint
retry, and save nothing: the prefix is cached either way, and the output tokens are
the same tokens (work.md 8, open item 2).
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "codegen"

SCHEMA: dict[str, Any] = {
    "title": "GeneratedTestFile",
    "type": "object",
    "properties": {
        # Repository-relative and forward-slashed. Go validates it before storing:
        # a model wrote it, so a path that climbs out of the suite root is a real
        # possibility rather than a theoretical one.
        "path": {"type": "string"},
        "content": {"type": "string"},
        # Which of the offered cases the file actually implements. The model is
        # told to omit a case it could not implement rather than claim it, because
        # a false claim here is a coverage number that lies.
        "covered_case_ids": {"type": "array", "items": {"type": "string"}},
        "notes": {"type": "string"},
    },
    "required": ["path", "content", "covered_case_ids", "notes"],
    "additionalProperties": False,
}

# What every generated suite must obey. These are not style preferences: a suite
# with a hardcoded URL cannot run anywhere but the machine it was written for, and
# one with a hardcoded token is a credential in a repository.
_RULES = (
    "Rules for the file you write:\n"
    "- Read the target from process.env.QAVIA_TARGET_URL and credentials from "
    "process.env.QAVIA_AUTH_TOKEN. Never hardcode a URL, a token, or a password.\n"
    "- One describe block per endpoint, one it block per test case, and the it "
    "title must be the test case title so a failure names the case.\n"
    "- Assert the status code and the specific fields the case names. Never "
    "expect(true) and never an empty body.\n"
    "- No TODO comments and no placeholder assertions. A case you cannot "
    "implement is left out of covered_case_ids instead.\n"
    "- Use only the framework's own API and the standard library. No helper file "
    "that does not exist.\n"
    "- The file must run under `npm test` with no arguments."
)

_INSTRUCTION = (
    "Write one {framework} test file for {endpoint}.\n\n"
    "Approved test cases to implement:\n{cases}\n\n"
    "{rules}\n\n"
    "Set path to something like tests/{suggested_path}. Set covered_case_ids to "
    "the ids of the cases you implemented."
)

# Appended on a retry. The findings come from the real toolchain inside the runner
# image, not from a guess: the file was compiled and scanned, and these are what it
# said. Repeating the rules would be noise; naming the actual failures is what makes
# a second attempt worth paying for (BE-3.4.2).
_FEEDBACK = (
    "\n\nYour previous attempt at this file was rejected by the validator that "
    "compiles and scans it. Fix exactly these findings and return the whole file "
    "again:\n{feedback}\n\n"
    "A test you cannot implement correctly is left out of covered_case_ids. Do not "
    "silence a finding with a weaker assertion."
)


def messages(
    document: dict[str, Any],
    framework: str,
    endpoint: str,
    suggested_path: str,
    cases: list[dict[str, Any]],
    feedback: str = "",
) -> list[Message]:
    """Build the call for one endpoint's file.

    feedback carries the validator's findings from a previous attempt, so a retry is
    a correction rather than the same roll of the dice.
    """

    rendered = "\n".join(
        f"- id: {case.get('id', '')}\n"
        f"  title: {case.get('title', '')}\n"
        f"  preconditions: {case.get('preconditions', '')}\n"
        f"  steps: {case.get('steps', '')}\n"
        f"  expected: {case.get('expected', '')}"
        for case in cases
    )

    text = _INSTRUCTION.format(
        framework=framework,
        endpoint=endpoint or "the endpoints in this specification",
        cases=rendered,
        rules=_RULES,
        suggested_path=suggested_path,
    )
    if feedback.strip():
        text += _FEEDBACK.format(feedback=feedback.strip())

    # The specification stays the cached prefix either way: the feedback is appended
    # to the instruction, which is the part that was never cacheable (BE-1.9).
    return [*cacheable(document), instruction(text)]
