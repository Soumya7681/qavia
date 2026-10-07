// Package mcp holds the Model Context Protocol server registry and the audit of MCP
// tool calls (BE-10.1, ai-architecture.md 5.4).
//
// MCP is how a team plugs Qavia into infrastructure they already run — a Jira, a GitHub,
// a read-only Postgres. It is entirely optional: every capability it fronts has a
// built-in that needs no server, and this registry staying empty forever is a supported
// configuration.
//
// An MCP server is a channel through which model output reaches an external system that
// can change state, and this package treats it as one throughout:
//
//   - **Tools are allowlisted, deny-all.** An admin tests the connection, sees the
//     tools, and opts into specific ones. A server's full tool set is never enabled by
//     default, because that is how a write tool gets turned on by accident.
//   - **Credentials are encrypted** with the same cipher as every other secret, and
//     never returned once stored.
//   - **Servers are scoped.** A project-scoped server is reachable only from its
//     project; one client's Jira is unreachable from another client's work.
//   - **Every call is audited** — server, tool, redacted arguments, status, agent, job
//     — because after an unexpected write the question is which job made it.
//
// The protocol itself is spoken in the Python AI service, where langchain-mcp-adapters
// bridges MCP tools into LangChain tools once, for every provider (ai-architecture.md
// 5.3). This package owns the configuration, the allowlist, the scoping, and the audit;
// it delegates the handshake — listing a server's tools — to that service, because the
// bridge that runs the agents is the right place to prove a server actually connects.
package mcp
