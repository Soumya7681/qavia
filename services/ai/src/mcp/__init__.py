"""MCP bridge for the AI service (BE-10.1, ai-architecture.md 5.3).

MCP is bridged into LangChain tools here, at the LangChain layer, rather than per
provider. `langchain-mcp-adapters` connects to a server, lists its tools, and converts
them into LangChain tools; every provider that supports tool calling then gets the same
MCP capability through one path, and the agents stay provider-agnostic.

Two functions the platform needs:

* **discover** - connect to a server and list its tools, so the admin can see what a
  server exposes and opt into specific ones. This is the connection test.
* **load_tools** - connect and return the LangChain tools an agent may use, filtered to
  the allowlist the platform passed. The filtering is here as well as in Go because the
  agent must never even be offered a tool the admin did not enable (BE-10.1.3).

The server configuration and its credential arrive in the request from Go, which owns
them; this service holds neither, and a stateless AI service is the whole point of the
two-runtime split (backend-standards.md 10).
"""

from __future__ import annotations

from typing import Any


def _server_spec(config: dict[str, Any]) -> dict[str, Any]:
    """Turn the platform's server config into a MultiServerMCPClient connection spec."""
    transport = config.get("transport", "stdio")

    if transport == "http":
        spec: dict[str, Any] = {"transport": "streamable_http", "url": config.get("url", "")}
        token = config.get("credential", "")
        if token:
            spec["headers"] = {"Authorization": f"Bearer {token}"}
        return spec

    # stdio: a command, its args, and the credential injected as an environment variable
    # the server reads. The token is never on the command line, which would put it in a
    # process list.
    env = {}
    token = config.get("credential", "")
    if token:
        env["MCP_TOKEN"] = token
    return {
        "transport": "stdio",
        "command": config.get("command", ""),
        "args": config.get("args", []) or [],
        "env": env,
    }


async def discover(config: dict[str, Any]) -> list[dict[str, Any]]:
    """Connect to a server and list its tools.

    The write flag is derived conservatively from the tool's name: a tool called
    ``create_issue`` or ``delete_file`` is treated as a write. Go stores this so the
    write-safety rules can act on it without re-deriving intent (BE-10.7).
    """
    from langchain_mcp_adapters.client import MultiServerMCPClient

    client = MultiServerMCPClient({"server": _server_spec(config)})
    tools = await client.get_tools()

    described = []
    for tool in tools:
        described.append(
            {
                "name": tool.name,
                "description": (tool.description or "")[:500],
                "write": _looks_like_write(tool.name),
            }
        )
    return described


_WRITE_MARKERS = (
    "create", "update", "delete", "remove", "write", "post", "put", "patch",
    "add", "set", "edit", "close", "merge", "comment", "send", "upload",
)


def _looks_like_write(name: str) -> bool:
    lowered = name.lower()
    return any(marker in lowered for marker in _WRITE_MARKERS)


async def load_tools(
    config: dict[str, Any],
    allowed: list[str],
    agent: str,
    auto_write: bool = False,
) -> list[Any]:
    """Return the LangChain tools an agent may use, through two independent filters.

    First the **allowlist**: a tool not in ``allowed`` is dropped, so the agent is never
    offered a tool the admin did not enable. An empty allowlist yields no tools, the safe
    reading of the schema doc's shorthand (BE-10.1.3).

    Then the **write-safety binding**: a write-capable tool is removed for an agent that
    reads untrusted content, which is where prompt injection would otherwise turn into
    action (BE-10.7). Both filters are deny-by-default and independent; a tool survives
    only if both admit it.
    """
    from langchain_mcp_adapters.client import MultiServerMCPClient

    from . import binding

    permitted = set(allowed or [])
    client = MultiServerMCPClient({"server": _server_spec(config)})
    tools = await client.get_tools()

    # Allowlist first.
    allowed_tools = [tool for tool in tools if tool.name in permitted]

    # Then the write-safety binding, using the write flag discovery derived.
    descriptors = [{"name": tool.name, "write": _looks_like_write(tool.name)} for tool in allowed_tools]
    bound = {bound_tool.name for bound_tool in binding.bind_tools(agent, descriptors, auto_write)}

    return [tool for tool in allowed_tools if tool.name in bound]
