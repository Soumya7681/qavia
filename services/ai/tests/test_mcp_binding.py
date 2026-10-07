"""The MCP write-safety acceptance test (BE-10.7).

The done-condition of BE-10.7 is a test: an agent holding a write tool cannot be bound to
an untrusted-content agent. This is that test. It is not a deferred suite — it is the
proof the safety property holds, and the feature is meaningless without it.
"""

from __future__ import annotations

import pytest

from src.mcp import binding


READ_TOOL = {"name": "read_ticket", "write": False}
WRITE_TOOL = {"name": "create_ticket", "write": True}


def test_untrusted_agent_never_holds_a_write_tool():
    # analyse reads run logs and response bodies, which an attacker can craft. It must
    # never be offered a write tool, even one the admin allowlisted.
    bound = binding.bind_tools("analyse", [READ_TOOL, WRITE_TOOL], auto_write=True)
    names = {tool.name for tool in bound}

    assert "read_ticket" in names, "a read tool is safe and should be bound"
    assert "create_ticket" not in names, "a write tool must never reach an untrusted-content agent"


def test_every_untrusted_agent_is_denied_write_tools():
    # The property has to hold for every agent that reads untrusted content, not just
    # one. A regression that trusts a new agent is exactly the silent hole this guards.
    for agent in binding.UNTRUSTED_CONTENT_AGENTS:
        assert not binding.can_bind_write_tool(agent), f"{agent} reads untrusted content and must not hold a write tool"

        bound = binding.bind_tools(agent, [WRITE_TOOL], auto_write=True)
        assert bound == [], f"{agent} must be bound no write tools"


def test_read_tools_are_bound_to_every_agent():
    for agent in list(binding.UNTRUSTED_CONTENT_AGENTS) + ["some_future_trusted_agent"]:
        bound = binding.bind_tools(agent, [READ_TOOL], auto_write=False)
        assert [tool.name for tool in bound] == ["read_ticket"]
        assert all(not tool.write for tool in bound)


def test_trusted_agent_may_hold_write_tools_with_confirmation():
    # A hypothetical agent that reasons only over the platform's own structured data can
    # hold a write tool. Without auto_write it requires confirmation.
    trusted = frozenset({"file_defect_agent"})
    original = binding.TRUSTED_WRITE_AGENTS
    binding.TRUSTED_WRITE_AGENTS = trusted
    try:
        assert binding.can_bind_write_tool("file_defect_agent")

        gated = binding.bind_tools("file_defect_agent", [WRITE_TOOL], auto_write=False)
        assert len(gated) == 1
        assert gated[0].write is True
        assert gated[0].requires_confirmation is True, "a write without auto_write must be gated"

        auto = binding.bind_tools("file_defect_agent", [WRITE_TOOL], auto_write=True)
        assert auto[0].requires_confirmation is False, "auto_write skips the per-call confirmation"
    finally:
        binding.TRUSTED_WRITE_AGENTS = original


def test_no_current_agent_is_trusted_for_writes():
    # Today no agent's job is to write to an external system, so none should be trusted.
    # A change here is a deliberate security decision, and this test makes it visible.
    assert binding.TRUSTED_WRITE_AGENTS == frozenset()


@pytest.mark.parametrize("agent", sorted(binding.UNTRUSTED_CONTENT_AGENTS))
def test_binding_is_deny_by_default_for_writes(agent):
    # Even with auto_write on, an untrusted agent gets nothing writable. auto_write
    # governs confirmation, never the untrusted-content rule.
    assert binding.bind_tools(agent, [WRITE_TOOL], auto_write=True) == []
