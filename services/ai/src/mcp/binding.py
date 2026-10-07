"""MCP tool-binding safety (BE-10.7, ai-architecture.md 5.5).

This is the file that stands between prompt injection and action. An MCP server is a
channel through which a model's output reaches a system that can change state, and the
agents that read untrusted content — an uploaded spec, a client's source, an HTTP
response body, a test log — are exactly the ones an attacker can feed a crafted
instruction. Give one of those agents a write-capable tool and injection becomes a filed
ticket, an opened PR, a posted message. So the rule is enforced here, in the binding
layer, not asked for in a prompt:

  1. **A write-capable tool is never bound to an agent that reads untrusted content.**
     Not gated, not confirmed — never offered. The intersection does not happen.
  2. **A write-capable tool otherwise requires confirmation**, unless the server is a
     project explicitly configured for automatic writes (`auto_write`). A model
     reasoning over a failed test does not silently file twenty tickets.
  3. **Read tools are bound freely** to any agent, because reading is not the risk.

The allowlist filtering from `load_tools` runs first — a tool the admin did not enable
is never in the set this sees. This layer then removes what the *agent* must not hold.
Both filters are deny-by-default and independent, which is the point: two locks, and an
argument for either one keeps the tool out.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

# The agents that read untrusted content. Untrusted means anything a person or a client
# supplied that a crafted instruction could hide in: an uploaded specification, source
# code, a page's DOM, an HTTP response, a test log. Every generation and analysis agent
# is on this list, because that is what they are for.
#
# Listed explicitly rather than derived, because the safety property depends on getting
# it right and a wrong default here is a silent hole. A new agent is untrusted until
# someone decides otherwise and adds it to the trusted set below — deny by default.
UNTRUSTED_CONTENT_AGENTS = frozenset(
    {
        "extract",      # reads the uploaded specification
        "design",       # reads the specification
        "codegen",      # reads the specification and prior test files
        "analyse",      # reads run logs and response bodies
        "repo",         # reads client source code
        "unittest",     # reads client source code
        "uiflow",       # reads a live application's pages
        "uispec",       # reads a discovered flow graph built from untrusted pages
        "probeselect",  # reads the specification
        "testdata",     # reads the specification's field descriptions
        "perf",         # reads the specification
        "dedupe",       # reads generated test cases
    }
)

# The agents trusted to hold a write tool. Empty today and that is honest: no current
# agent's job is to write to an external system, so none should hold a write tool. When
# a dedicated "file the defect" or "open the PR" agent is built — one that reasons only
# over the platform's own structured data, never untrusted input — it goes here.
TRUSTED_WRITE_AGENTS: frozenset[str] = frozenset()


@dataclass(frozen=True)
class BoundTool:
    """One tool an agent may use, and how."""

    name: str
    write: bool
    # requires_confirmation is true for a write tool on a server that is not configured
    # for automatic writes. The runtime pauses for a human step before such a call.
    requires_confirmation: bool


def reads_untrusted_content(agent: str) -> bool:
    """Whether an agent reads content an attacker could have crafted."""
    return agent in UNTRUSTED_CONTENT_AGENTS


def bind_tools(
    agent: str,
    tools: list[dict[str, Any]],
    auto_write: bool,
) -> list[BoundTool]:
    """Return the tools an agent may hold, enforcing the write-safety rules.

    ``tools`` are the allowlist-filtered tools for a server — each a dict with ``name``
    and ``write``. ``auto_write`` is whether the server permits writes without a
    per-call confirmation.

    The rules, applied in order:

    * A read tool is always bound.
    * A write tool bound to an untrusted-content agent is dropped entirely. This is the
      rule that defeats prompt injection: the tool is not merely gated, it is absent.
    * A write tool bound to a trusted agent is allowed, requiring confirmation unless the
      server is configured for automatic writes.
    """
    untrusted = reads_untrusted_content(agent)
    trusted_for_write = agent in TRUSTED_WRITE_AGENTS

    bound: list[BoundTool] = []
    for tool in tools:
        name = tool.get("name", "")
        is_write = bool(tool.get("write", False))

        if not is_write:
            bound.append(BoundTool(name=name, write=False, requires_confirmation=False))
            continue

        if untrusted or not trusted_for_write:
            # The intersection this whole file exists to prevent, or an agent not
            # cleared for writes. Dropped, not offered.
            continue

        bound.append(
            BoundTool(
                name=name,
                write=True,
                requires_confirmation=not auto_write,
            )
        )

    return bound


def can_bind_write_tool(agent: str) -> bool:
    """Whether an agent may ever hold a write tool at all.

    The single predicate the safety property reduces to: an agent that reads untrusted
    content, or one not explicitly trusted for writes, can never hold a write tool. This
    is what the acceptance test asserts (BE-10.7).
    """
    return (not reads_untrusted_content(agent)) and (agent in TRUSTED_WRITE_AGENTS)
