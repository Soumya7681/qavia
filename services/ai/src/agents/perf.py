"""Performance test agent: a k6 load script from the API's endpoints (F-11.1, BE-9.1).

The load *profile* — how many virtual users, how long the ramp, how long the hold — is
a person's decision made in the UI, not the model's: it is the one input that says how
hard to hit somebody's server, and that belongs to whoever authorised the test, not to
a model choosing a number. What the model writes is the script that exercises the
endpoints under that profile: which requests to make, in what order, with what checks.

The script is generated on the `code` tier and then **validated in the k6 image before
it runs a single request** — `k6 archive` builds the module graph and resolves every
import without making a call, which is exactly the static check the platform already
applies to generated tests (BE-3.4). A load script that does not compile is worse than
none, because it fails partway through a run somebody is watching a real server for.

What the agent is told, and told firmly: the profile's numbers are fixed and it must
use them exactly. A model that quietly raises the virtual users because the endpoints
"look like they can take it" is a model deciding how hard to attack a client's
infrastructure, and that decision was already made.
"""

from __future__ import annotations

from typing import Any

from ..llm.schemas import Message
from .prompts import cacheable, instruction

TIER = "code"

AGENT = "perf"

PROMPT_VERSION = "perf/v1"

SCHEMA: dict[str, Any] = {
    "title": "GeneratedLoadScript",
    "type": "object",
    "properties": {
        # The script itself. k6 runs one entry file, so there is one of these.
        "content": {"type": "string"},
        # Which endpoints it exercises, so the plan is reviewable against the spec: a
        # load test that skipped the checkout endpoint is a load test of the wrong
        # thing.
        "exercises": {"type": "array", "items": {"type": "string"}},
        "notes": {"type": "string"},
    },
    "required": ["content", "exercises", "notes"],
    "additionalProperties": False,
}

_RULES = (
    "How to write it:\n"
    "- Import http from 'k6/http' and { check, sleep } from 'k6'.\n"
    "- Export an `options` object whose `stages` are EXACTLY the ramp given below. Do "
    "not change the virtual users or the durations: they are the authorised load, and "
    "raising them is deciding how hard to hit somebody's server.\n"
    "- Set `options.thresholds` from the stated targets: a p95 latency ceiling and a "
    "maximum error rate, so the run has a stated pass condition rather than only "
    "numbers.\n"
    "- Set `options.summaryTrendStats` to "
    "['avg','min','med','max','p(90)','p(95)','p(99)'] so the platform can read the "
    "tail.\n"
    "- The base URL is process.env.QAVIA_TARGET_URL; build every request from it. The "
    "bearer token, when present, is process.env.QAVIA_AUTH_TOKEN.\n"
    "- Exercise the read endpoints under load. Include a write only when the profile "
    "says to, and never a destructive one — a load test that deletes real data at 50 "
    "users is an outage, not a test.\n"
    "- check() each response for its expected status, so a run that returns 500s under "
    "load is visible as failures rather than as fast responses.\n"
    "- One default function that a virtual user runs in a loop, with a small sleep() "
    "between requests so the load is requests-per-second rather than a tight spin."
)

_INSTRUCTION = (
    "Write a k6 load script for this API.\n\n"
    "Authorised load profile — use these numbers exactly:\n{profile}\n\n"
    "Targets (become thresholds): p95 latency under {p95}ms, error rate under "
    "{error_rate}.\n\n"
    "{rules}\n\n"
    "Return the script's content, the endpoints it exercises, and any notes."
)


def messages(
    endpoints: dict[str, Any],
    profile: str,
    p95_ms: int,
    error_rate: float,
) -> list[Message]:
    """Build the call.

    ``endpoints`` is the cacheable prefix: the API's shape does not change between one
    profile and another, so generating a second script for a heavier load reuses it
    (ai-architecture.md 3.6).
    """

    return [
        *cacheable(endpoints),
        instruction(
            _INSTRUCTION.format(
                profile=profile,
                p95=p95_ms,
                error_rate=error_rate,
                rules=_RULES,
            )
        ),
    ]
