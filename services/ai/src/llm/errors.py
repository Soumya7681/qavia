"""One error type, classified by what the caller can do about it.

The kind is the load-bearing part. Cross-provider fallback fires on ``retryable``
and on nothing else: retrying a 400 on a second provider produces the same 400 and
bills for it twice (ai-architecture.md 3.8).
"""

from __future__ import annotations

from typing import Any

from .schemas import ErrorKind


class LLMError(Exception):
    """A failure with a stable code, a safe message, and a kind."""

    def __init__(
        self,
        code: str,
        message: str,
        kind: ErrorKind = ErrorKind.internal,
        details: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.kind = kind
        self.details = details

    @property
    def status(self) -> int:
        """HTTP status for the response.

        503 for retryable, so Go's client can treat it as an availability problem
        without parsing the body first.
        """

        return {
            ErrorKind.retryable: 503,
            ErrorKind.bad_request: 400,
            ErrorKind.auth: 401,
            ErrorKind.not_supported: 422,
            ErrorKind.validation: 422,
            ErrorKind.internal: 500,
        }[self.kind]

    @classmethod
    def retryable(cls, message: str, details: dict[str, Any] | None = None) -> LLMError:
        return cls("ai_provider_unavailable", message, ErrorKind.retryable, details)

    @classmethod
    def bad_request(cls, message: str, details: dict[str, Any] | None = None) -> LLMError:
        return cls("ai_bad_request", message, ErrorKind.bad_request, details)

    @classmethod
    def auth(cls, message: str, details: dict[str, Any] | None = None) -> LLMError:
        return cls("ai_credentials_invalid", message, ErrorKind.auth, details)

    @classmethod
    def not_supported(cls, message: str, details: dict[str, Any] | None = None) -> LLMError:
        return cls("ai_not_supported", message, ErrorKind.not_supported, details)

    @classmethod
    def validation(cls, message: str, details: dict[str, Any] | None = None) -> LLMError:
        return cls("ai_response_invalid", message, ErrorKind.validation, details)


# Substrings that mark a provider failure as worth retrying elsewhere.
#
# Matching on text is unpleasant and deliberate: every provider package raises its
# own exception types, and the set changes with each release. The status code is
# checked first where the exception exposes one; this is the fallback, and it is
# tuned to be conservative, because misclassifying a bad request as retryable
# doubles the bill rather than fixing anything.
_RETRYABLE_MARKERS = (
    "rate limit",
    "rate_limit",
    "ratelimit",
    "429",
    "overloaded",
    "overload_error",
    "503",
    "502",
    "504",
    "timeout",
    "timed out",
    "connection",
    "temporarily unavailable",
    "service unavailable",
    "capacity",
    "throttl",
)

_AUTH_MARKERS = (
    "401",
    "403",
    "unauthorized",
    "authentication",
    "invalid api key",
    "invalid_api_key",
    "permission denied",
    "accessdenied",
    "credential",
)


def classify(err: Exception) -> LLMError:
    """Turn a provider exception into something the gateway can act on.

    The provider's own message is not passed through: it can contain the request
    body, which for this platform is client source code, and it ends up in a log, a
    notification, and a screenshot (backend-standards.md 5).
    """

    if isinstance(err, LLMError):
        return err

    text = f"{type(err).__name__}: {err}".lower()
    status = getattr(err, "status_code", None) or getattr(err, "http_status", None)

    if status in (429, 500, 502, 503, 504) or any(m in text for m in _RETRYABLE_MARKERS):
        return LLMError.retryable(
            "The AI provider is unavailable or rate limiting. "
            "The platform will retry, or fall back if a fallback model is configured.",
            details={"cause": type(err).__name__},
        )

    if status in (401, 403) or any(m in text for m in _AUTH_MARKERS):
        return LLMError.auth(
            "The AI provider rejected the credentials. "
            "Check them in Settings, AI, Providers.",
            details={"cause": type(err).__name__},
        )

    if status == 400:
        return LLMError.bad_request(
            "The AI provider rejected the request. This is a platform bug, not a "
            "configuration problem.",
            details={"cause": type(err).__name__},
        )

    return LLMError(
        "ai_call_failed",
        "The AI call failed.",
        ErrorKind.internal,
        {"cause": type(err).__name__},
    )
