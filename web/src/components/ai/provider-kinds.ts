import type { components } from "@/lib/api/schema";

export type ProviderKind = components["schemas"]["AIProviderKind"];
export type DataResidency = components["schemas"]["AIDataResidency"];

export type CredentialField = {
  key: string;
  label: string;
  /** Write-only: never pre-filled, never read back, shown as a hint once stored. */
  secret?: boolean;
  optional?: boolean;
  multiline?: boolean;
  placeholder?: string;
  help?: string;
  /** A boolean switch rather than text, sent as "true" or omitted. */
  toggle?: boolean;
};

export type KindInfo = {
  label: string;
  /** Suggested display name for a new provider of this kind. */
  defaultName: string;
  residency: DataResidency;
  fields: CredentialField[];
  note?: string;
};

/**
 * What to ask for per provider kind. The contract describes credentials only as
 * "shape varies by kind", so this table is presentation: it decides which inputs
 * to draw. The API is the authority and names any field it still needs
 * (llm/credentials.go), and that message is shown as-is.
 */
export const providerKinds: Record<ProviderKind, KindInfo> = {
  anthropic: {
    label: "Anthropic",
    defaultName: "Anthropic",
    residency: "external",
    fields: [{ key: "api_key", label: "API key", secret: true, placeholder: "sk-ant-…" }],
  },
  openai: {
    label: "OpenAI",
    defaultName: "OpenAI",
    residency: "external",
    fields: [{ key: "api_key", label: "API key", secret: true, placeholder: "sk-…" }],
  },
  "azure-openai": {
    label: "Azure OpenAI",
    defaultName: "Azure OpenAI",
    residency: "regional",
    fields: [
      { key: "endpoint", label: "Endpoint", placeholder: "https://your-resource.openai.azure.com" },
      { key: "deployment", label: "Deployment name" },
      { key: "api_version", label: "API version", placeholder: "2024-10-21" },
      { key: "api_key", label: "API key", secret: true },
    ],
  },
  gemini: {
    label: "Google Gemini",
    defaultName: "Gemini",
    residency: "external",
    fields: [{ key: "api_key", label: "API key", secret: true }],
  },
  bedrock: {
    label: "AWS Bedrock",
    defaultName: "Bedrock",
    residency: "regional",
    note: "On EC2 or ECS, prefer the instance role: no keys are stored at all.",
    fields: [
      { key: "region", label: "Region", placeholder: "eu-west-1" },
      { key: "use_instance_role", label: "Use the instance role", toggle: true },
      { key: "access_key_id", label: "Access key ID", optional: true },
      { key: "secret_access_key", label: "Secret access key", secret: true, optional: true },
    ],
  },
  vertex: {
    label: "Google Vertex AI",
    defaultName: "Vertex AI",
    residency: "regional",
    fields: [
      { key: "project_id", label: "Project ID" },
      {
        key: "service_account_json",
        label: "Service account JSON",
        secret: true,
        multiline: true,
        help: "Paste the whole key file.",
      },
    ],
  },
  "openai-compatible": {
    label: "OpenAI-compatible (Ollama, vLLM, LiteLLM, OpenRouter…)",
    defaultName: "Local model",
    residency: "local",
    note: "A local model keeps client data on your own infrastructure.",
    fields: [
      { key: "base_url", label: "Base URL", placeholder: "http://localhost:11434/v1" },
      { key: "api_key", label: "API key", secret: true, optional: true },
    ],
  },
};

export const residencyLabel: Record<DataResidency, string> = {
  local: "Local: data stays on your infrastructure",
  regional: "Regional: a cloud region you choose",
  external: "External: the provider's own service",
};
