"""Three prompt-caching strategies, because providers do not agree (F-16.6).

Fan-out is the cost story here: one specification, then forty calls sharing it as a
prefix. Caching is what makes that affordable, and no abstraction layer hides the
difference between how providers do it (ai-architecture.md 3.6):

- **explicit-breakpoint** (Anthropic direct, Bedrock, Vertex): mark the end of the
  stable prefix with ``cache_control``. Reads cost roughly a tenth of input.
- **passive-prefix** (OpenAI): nothing to set. Keep the prefix byte-identical and
  the discount applies itself.
- **cache-object-lifecycle** (Gemini): create a cache object once, reference its
  handle per call, delete it when the chain ends. A different code path entirely.

The invariant every strategy depends on: nothing above the cache boundary may vary
between calls. No timestamps, no UUIDs, no per-call IDs. That is a code-level rule
with a test behind it, not a style preference.
"""

from __future__ import annotations

from typing import Any

from langchain_core.messages import AIMessage, BaseMessage, HumanMessage, SystemMessage

from .schemas import Message, PromptCaching, Role, Target

_ROLE_TYPES = {
    Role.system: SystemMessage,
    Role.user: HumanMessage,
    Role.assistant: AIMessage,
}


def to_messages(messages: list[Message], target: Target) -> list[BaseMessage]:
    """Render the request's messages for the provider, with caching applied."""

    strategy = target.model.capabilities.prompt_caching

    if strategy is PromptCaching.explicit:
        return _explicit_breakpoints(messages, target)
    # passive-prefix and none render identically: the difference is what the
    # provider does with the prefix, not what is sent.
    return [_plain(message) for message in messages]


def _plain(message: Message) -> BaseMessage:
    return _ROLE_TYPES[message.role](content=message.content)


def _explicit_breakpoints(messages: list[Message], target: Target) -> list[BaseMessage]:
    """Place a breakpoint after the last cacheable message.

    One breakpoint, at the end of the stable prefix, rather than one per block.
    Providers cap how many breakpoints a request may carry, and the prefix is the
    only part that repeats across a fan-out, so more of them buys nothing and risks
    a rejected request.
    """

    last_cacheable = -1
    for index, message in enumerate(messages):
        if message.cacheable:
            last_cacheable = index

    rendered: list[BaseMessage] = []
    for index, message in enumerate(messages):
        if index != last_cacheable:
            rendered.append(_plain(message))
            continue

        # Anthropic-style content blocks. Bedrock and Vertex accept the same shape
        # through their LangChain adapters, which is why one branch serves all
        # three.
        block: dict[str, Any] = {
            "type": "text",
            "text": message.content,
            "cache_control": {"type": "ephemeral"},
        }
        rendered.append(_ROLE_TYPES[message.role](content=[block]))

    _ = target  # kept in the signature: the strategy is selected per target
    return rendered


def cacheable_prefix(messages: list[Message]) -> str:
    """The exact text a cache would key on.

    Used by the prefix-stability test: render twice, assert byte-identical. If this
    ever includes a timestamp the fan-out silently stops caching and the bill goes
    up by roughly an order of magnitude with no error anywhere.
    """

    return "\n".join(message.content for message in messages if message.cacheable)


def supports_cache_objects(target: Target) -> bool:
    """Whether this provider has a cache-create API to manage a handle for.

    Only Gemini does. For everyone else the caller proceeds without a handle, which
    is why an absent handle is never an error.
    """

    from .schemas import ProviderKind

    return (
        target.provider.kind is ProviderKind.gemini
        and target.model.capabilities.prompt_caching is PromptCaching.explicit
    )
