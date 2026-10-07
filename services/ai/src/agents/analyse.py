"""Analyse agent: why did this test fail (F-9.1, BE-5.2).

The hard part of this agent is not producing an explanation. A model will produce a
fluent explanation of any failure, instantly, and a fluent wrong explanation is worse
than silence because somebody will act on it. So the schema forces the model to point
at what it read, and Go rejects the analysis if a citation does not check out
(BE-5.3).

Three properties, and each one is a decision rather than a default:

* **Evidence is structured.** A reference names a log line range, a response field
  path, a source file and line, or the run history the platform computed. A UI turns
  those into links; it cannot turn "see the logs" into anything.
* **The stability score is not asked for.** It is arithmetic over stored results, and
  Go computes it (BE-5.4). Asking a model to rate its own confidence produces a
  number that correlates with nothing.
* **Nothing here is a tool that writes.** The inputs are logs and response bodies,
  which is untrusted content by definition: it came from a target under test, shaped
  by code a model wrote. An agent reading that must not also be able to change a
  file (BE-5.2.3).

The prompt version is part of the contract. It is stored with every analysis and with
every thumbs-up, so a prompt change is measured rather than argued about (BE-5.8).
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "reasoning"

AGENT = "analyse"

# Bumped whenever the instruction or the schema changes in a way that could change
# the output. Feedback is grouped by this, so changing the prompt without changing
# this makes the previous version's numbers a lie.
PROMPT_VERSION = "analyse/v1"

SCHEMA: dict[str, Any] = {
    "title": "FailureAnalysis",
    "type": "object",
    "properties": {
        # One line, for a list. The place where "assertion failed" is not an answer.
        "reason": {"type": "string"},
        # The explanation. What actually happened, in terms of the system under test.
        "root_cause": {"type": "string"},
        # Advice, never applied. Nothing in this platform writes to a repository.
        "suggested_fix": {"type": "string"},
        # Whether this looks like a defect in the target or a problem with the test
        # itself. Both are useful answers and a platform that can only say "the code
        # is broken" is wrong half the time on a generated suite.
        "verdict": {
            "type": "string",
            "enum": ["target_defect", "test_defect", "environment", "unknown"],
        },
        "evidence": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "kind": {
                        "type": "string",
                        "enum": ["log", "response", "source", "history"],
                    },
                    # What this reference shows, in one short phrase. It is the label
                    # on a link, not the explanation.
                    "detail": {"type": "string"},
                    # log and source references.
                    "file": {"type": "string"},
                    "fromLine": {"type": "integer"},
                    "toLine": {"type": "integer"},
                    # response references: a dotted path into the body.
                    "path": {"type": "string"},
                    # The text the reference claims is there. Checked.
                    "quote": {"type": "string"},
                },
                "required": ["kind", "detail"],
                "additionalProperties": False,
            },
        },
    },
    "required": ["reason", "root_cause", "suggested_fix", "verdict", "evidence"],
    "additionalProperties": False,
}

_RULES = (
    "Rules for your analysis:\n"
    "- Cite what you read. Every claim about the log, the response, the test source, "
    "or the test's history needs an evidence entry pointing at it.\n"
    "- Line numbers are 1-based and must exist in the material you were given. A "
    "citation that does not check out fails the whole analysis and it is retried.\n"
    "- Quote exactly when you quote. The quote is compared against the cited lines.\n"
    "- Say which side is at fault. A generated test asserting the wrong thing is a "
    "test defect, and calling it a bug in the target wastes a developer's afternoon.\n"
    "- Do not guess at a stability or confidence number. The platform computes that "
    "from the test's own history and will ignore anything you say about it.\n"
    "- If the material does not explain the failure, say so in root_cause and set "
    "verdict to unknown. An honest 'not enough information' is useful; an invented "
    "cause is not."
)

_INSTRUCTION = (
    "A test failed. Explain why.\n\n"
    "Test: {test_name}\n"
    "Status: {status} (attempt {attempt})\n"
    "Framework message:\n{failure_message}\n\n"
    "{history}\n"
    "{rules}"
)


def messages(
    context: dict[str, Any],
    test_name: str,
    status: str,
    attempt: int,
    failure_message: str,
    history: list[dict[str, Any]] | None = None,
    feedback: str = "",
) -> list[Message]:
    """Build the call for one failure.

    ``context`` carries the material the analysis may cite: the run log, the captured
    response, and the source of the test. It goes in the cacheable prefix because a
    run with forty failures analyses forty times against the same log, and paying full
    price for that log forty times is the difference between this feature being
    affordable and not (ai-architecture.md 3.6).
    """

    rendered_history = ""
    if history:
        lines = "\n".join(
            f"- {point.get('at', '')}: {point.get('status', '')}"
            f" (attempt {point.get('attempt', 1)})"
            for point in history
        )
        rendered_history = (
            "This test's recent results, newest first:\n"
            f"{lines}\n\n"
            "A test that alternates between passing and failing is telling you "
            "something about itself. Say so if that is what the history shows.\n\n"
        )

    text = _INSTRUCTION.format(
        test_name=test_name,
        status=status,
        attempt=attempt,
        failure_message=failure_message.strip() or "(the framework reported no message)",
        history=rendered_history,
        rules=_RULES,
    )

    if feedback.strip():
        # A retry after evidence validation failed. The findings are specific and
        # checkable, so they are quoted rather than summarised: "evidence[1] cites log
        # line 812 and the log has 44 lines" tells the model exactly what to fix.
        text += (
            "\n\nYour previous analysis was rejected because its evidence did not "
            "check out:\n"
            f"{feedback.strip()}\n\n"
            "Cite only what is actually in the material above. Fewer, correct "
            "citations beat more, invented ones."
        )

    return [*cacheable(context), instruction(text)]
