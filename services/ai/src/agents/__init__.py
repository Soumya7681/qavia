"""Agents.

Each one is a prompt, a schema, and a tier. None of them names a provider or a
model: they call ``gateway.chat`` with a tier, and which model serves it is a
settings decision (backend-standards.md 10).

The single-shot agents live here as prompt builders rather than as request
runners, because Go decides what to generate and when. This module produces the
messages and the schema; the gateway makes the call.
"""
