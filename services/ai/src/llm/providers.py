"""One adapter per provider kind (F-16.1, F-16.2).

This file and ``gateway.py`` are the only places a provider package is imported.
An agent that imports one is a review failure (backend-standards.md 10): the point
of the tier abstraction is that switching provider stays a settings change forever,
and an agent that knows a vendor name breaks that permanently.

Credential shapes are per kind (ai-architecture.md 3.3) and validated here, so a
missing field fails with a message naming the field rather than as a provider 401
twenty seconds into a job.
"""

from __future__ import annotations

import json
from collections.abc import Sequence
from typing import Any

from langchain_core.language_models import BaseChatModel

from .errors import LLMError
from .schemas import ProviderKind, Target

# Credential fields each kind requires, from ai-architecture.md 3.3. Optional
# fields are deliberately absent: an adapter tolerates their absence.
REQUIRED_CREDENTIALS: dict[ProviderKind, tuple[str, ...]] = {
    ProviderKind.anthropic: ("api_key",),
    ProviderKind.openai: ("api_key",),
    ProviderKind.azure_openai: ("api_key", "endpoint", "deployment", "api_version"),
    ProviderKind.gemini: ("api_key",),
    # bedrock is checked in its own adapter: instance-role mode needs no keys at
    # all, which is the preferred production path.
    ProviderKind.bedrock: (),
    ProviderKind.vertex: ("service_account_json", "project_id"),
    ProviderKind.openai_compatible: ("base_url",),
}


def build_client(target: Target) -> BaseChatModel:
    """Construct a chat model for one call.

    Built per request rather than cached, because credentials arrive per request
    and a cached client would outlive the decrypted secret it was built from.
    """

    kind = target.provider.kind
    _require(kind, target.provider.credentials)

    builders = {
        ProviderKind.anthropic: _anthropic,
        ProviderKind.openai: _openai,
        ProviderKind.azure_openai: _azure_openai,
        ProviderKind.gemini: _gemini,
        ProviderKind.bedrock: _bedrock,
        ProviderKind.vertex: _vertex,
        ProviderKind.openai_compatible: _openai_compatible,
    }

    builder = builders.get(kind)
    if builder is None:
        raise LLMError.not_supported(f"Provider kind {kind.value!r} is not supported.")
    return builder(target)


def _require(kind: ProviderKind, credentials: dict[str, Any]) -> None:
    missing = [
        field
        for field in REQUIRED_CREDENTIALS.get(kind, ())
        if not str(credentials.get(field, "")).strip()
    ]
    if missing:
        raise LLMError.auth(
            f"The {kind.value} provider is missing {', '.join(missing)}. "
            f"Add it in Settings, AI, Providers.",
            details={"kind": kind.value, "missing": missing},
        )


def _max_tokens(target: Target) -> dict[str, Any]:
    if target.model.max_output_tokens:
        return {"max_tokens": target.model.max_output_tokens}
    return {}


def _anthropic(target: Target) -> BaseChatModel:
    from langchain_anthropic import ChatAnthropic

    return ChatAnthropic(
        model=target.model.model_id,
        api_key=target.provider.credentials["api_key"],
        base_url=target.provider.config.get("base_url") or None,
        timeout=None,
        stop=None,
        **_max_tokens(target),
    )


def _openai(target: Target) -> BaseChatModel:
    from langchain_openai import ChatOpenAI

    return ChatOpenAI(
        model=target.model.model_id,
        api_key=target.provider.credentials["api_key"],
        organization=target.provider.credentials.get("organization") or None,
        base_url=target.provider.config.get("base_url") or None,
    )


def _azure_openai(target: Target) -> BaseChatModel:
    from langchain_openai import AzureChatOpenAI

    credentials = target.provider.credentials
    return AzureChatOpenAI(
        # Azure addresses a deployment rather than a model name, which is why the
        # deployment is a required credential field for this kind alone.
        azure_deployment=credentials["deployment"],
        azure_endpoint=credentials["endpoint"],
        api_version=credentials["api_version"],
        api_key=credentials["api_key"],
        model=target.model.model_id,
    )


def _gemini(target: Target) -> BaseChatModel:
    from langchain_google_genai import ChatGoogleGenerativeAI

    return ChatGoogleGenerativeAI(
        model=target.model.model_id,
        google_api_key=target.provider.credentials["api_key"],
    )


def _bedrock(target: Target) -> BaseChatModel:
    """Bedrock, with instance-role support.

    ``use_instance_role`` is the preferred production path: no long-lived keys are
    stored anywhere, and IAM handles it. It is offered as the default in the UI
    when the platform is running on EC2 or ECS.
    """

    from langchain_aws import ChatBedrockConverse

    credentials = target.provider.credentials
    region = credentials.get("region") or target.provider.config.get("region")
    if not region:
        raise LLMError.auth(
            "The bedrock provider needs a region. Add it in Settings, AI, Providers.",
            details={"kind": ProviderKind.bedrock.value, "missing": ["region"]},
        )

    if credentials.get("use_instance_role"):
        return ChatBedrockConverse(model=target.model.model_id, region_name=region)

    missing = [
        field
        for field in ("access_key_id", "secret_access_key")
        if not str(credentials.get(field, "")).strip()
    ]
    if missing:
        raise LLMError.auth(
            "The bedrock provider needs either an access key pair or "
            "use_instance_role. Set one in Settings, AI, Providers.",
            details={"kind": ProviderKind.bedrock.value, "missing": missing},
        )

    return ChatBedrockConverse(
        model=target.model.model_id,
        region_name=region,
        aws_access_key_id=credentials["access_key_id"],
        aws_secret_access_key=credentials["secret_access_key"],
        aws_session_token=credentials.get("session_token") or None,
    )


def _vertex(target: Target) -> BaseChatModel:
    from google.oauth2 import service_account  # type: ignore[import-untyped]
    from langchain_google_vertexai import ChatVertexAI

    credentials = target.provider.credentials
    raw = credentials["service_account_json"]
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except json.JSONDecodeError as err:
            raise LLMError.auth(
                "The vertex service account is not valid JSON. Paste the whole key file.",
                details={"kind": ProviderKind.vertex.value},
            ) from err

    return ChatVertexAI(
        model_name=target.model.model_id,
        project=credentials["project_id"],
        location=credentials.get("location")
        or target.provider.config.get("location")
        or "us-central1",
        credentials=service_account.Credentials.from_service_account_info(raw),
    )


def _openai_compatible(target: Target) -> BaseChatModel:
    """The adapter that makes "any other provider" real.

    One base URL covers Ollama, vLLM, LiteLLM, OpenRouter, Together, Groq,
    Fireworks and DeepSeek. A user adds a provider with a URL, an optional key, and
    a model name: no code change and no deploy.
    """

    from langchain_openai import ChatOpenAI

    credentials = target.provider.credentials
    headers = credentials.get("headers") or target.provider.config.get("headers") or {}
    if not isinstance(headers, dict):
        raise LLMError.bad_request("Custom headers must be an object of name to value.")

    return ChatOpenAI(
        model=target.model.model_id,
        base_url=credentials["base_url"],
        # A local endpoint often needs no key, and OpenAI's client refuses an empty
        # one, so a placeholder stands in where the user supplied nothing.
        api_key=credentials.get("api_key") or "not-needed",
        default_headers=headers or None,
    )


def supported_kinds() -> Sequence[str]:
    """Every kind this build can serve, for the settings screen."""

    return tuple(kind.value for kind in ProviderKind)
