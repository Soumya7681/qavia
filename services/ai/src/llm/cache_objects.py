"""Gemini's context cache: created once per chain, deleted at chain end.

The third caching strategy (ai-architecture.md 3.6) is a different code path
entirely rather than a parameter. The handle belongs to the job chain, not to a
call, and deleting it is part of chain teardown **including on failure** — an
orphaned cache object bills for its whole TTL and nothing points at it.

Every other provider answers "not supported" here, and that is not an error: the
caller proceeds without a handle and the cost model is linear in calls.
"""

from __future__ import annotations

import logging

from .caching import supports_cache_objects
from .errors import classify
from .schemas import CacheCreateRequest, CacheHandle, Role

logger = logging.getLogger(__name__)

# Below roughly this many characters Gemini refuses to create a cache object, and
# the call is wasted. The exact minimum is model-specific and stated in tokens;
# this is a conservative character-count stand-in that avoids a round trip whose
# only outcome is an error.
_MIN_CACHEABLE_CHARS = 4000


async def create(request: CacheCreateRequest) -> CacheHandle:
    if not supports_cache_objects(request.target):
        return CacheHandle(
            created=False,
            detail="This provider has no cache object API. Calls will not share a cached prefix.",
        )

    content = "\n".join(m.content for m in request.messages if m.cacheable)
    if len(content) < _MIN_CACHEABLE_CHARS:
        return CacheHandle(
            created=False,
            detail="The prefix is too small to cache. The provider would reject it.",
        )

    try:
        from google import genai  # type: ignore[import-untyped]
        from google.genai import types  # type: ignore[import-untyped]

        client = genai.Client(api_key=request.target.provider.credentials["api_key"])
        system = "\n".join(m.content for m in request.messages if m.role is Role.system)

        cache = await client.aio.caches.create(
            model=request.target.model.model_id,
            config=types.CreateCachedContentConfig(
                system_instruction=system or None,
                contents=[content],
                ttl=f"{request.ttl_seconds}s",
            ),
        )
    except Exception as err:
        failure = classify(err)
        logger.warning("cache object creation failed", extra={"code": failure.code})
        # Not fatal. Without a handle the fan-out still runs, it just costs more,
        # and failing the chain over a cost optimisation would be the wrong trade.
        return CacheHandle(created=False, detail=failure.message)

    return CacheHandle(handle=cache.name, created=True, detail="Cache object created.")


async def delete(target_api_key: str, handle: str) -> bool:
    """Release a cache object. Called at chain teardown, success or failure."""

    try:
        from google import genai  # type: ignore[import-untyped]

        client = genai.Client(api_key=target_api_key)
        await client.aio.caches.delete(name=handle)
    except Exception as err:
        logger.warning(
            "cache object deletion failed",
            extra={"cause": type(err).__name__, "handle": handle},
        )
        return False
    return True
