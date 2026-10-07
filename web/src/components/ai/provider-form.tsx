"use client";

import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { FormAlert } from "@/components/auth/form-alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api/client";
import { type ApiError, isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";

import {
  type DataResidency,
  type ProviderKind,
  providerKinds,
  residencyLabel,
} from "./provider-kinds";

type Provider = components["schemas"]["AIProvider"];

/**
 * Add an AI provider. Used by the setup wizard and the AI providers screen.
 *
 * Secrets go in once and are never shown again; the API returns only whether one
 * is set. A missing field is reported by the API by name, against that field.
 */
export function ProviderForm({
  kinds,
  makeDefault,
  onCreated,
  submitLabel = "Add provider",
}: {
  kinds: ProviderKind[];
  makeDefault: boolean;
  onCreated: (provider: Provider) => void;
  submitLabel?: string;
}) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<ProviderKind | undefined>(kinds[0]);
  const [name, setName] = useState(kinds[0] ? providerKinds[kinds[0]].defaultName : "");
  const [values, setValues] = useState<Record<string, string>>({});
  const [residency, setResidency] = useState<DataResidency | undefined>();
  const [failure, setFailure] = useState<ApiError | null>(null);
  const create = api.useMutation("post", "/api/v1/ai/providers");

  const info = kind ? providerKinds[kind] : undefined;
  const missing = new Set(
    Array.isArray(failure?.details.missing) ? (failure.details.missing as string[]) : [],
  );

  const chooseKind = (next: string) => {
    const k = next as ProviderKind;
    setKind(k);
    setName(providerKinds[k].defaultName);
    setValues({});
    setResidency(undefined);
    setFailure(null);
  };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!kind || !info) return;
    setFailure(null);
    const credentials = Object.fromEntries(
      Object.entries(values).filter(([, v]) => v.trim() !== ""),
    );
    try {
      const provider = await create.mutateAsync({
        body: {
          name: name.trim() || info.defaultName,
          kind,
          credentials,
          dataResidency: residency ?? info.residency,
          makeDefault,
          enabled: true,
        },
      });
      await queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/ai/providers"] });
      onCreated(provider);
    } catch (error) {
      if (!isApiError(error)) throw error;
      setFailure(error);
    }
  };

  if (!kind || !info) {
    return <p className="text-sm text-muted-foreground">This build offers no provider kinds.</p>;
  }

  const instanceRole = values.use_instance_role === "true";

  return (
    <form onSubmit={submit} noValidate className="flex flex-col gap-5">
      <FormAlert
        message={
          failure
            ? {
                title: failure.message,
                description: failure.incidentId ? `Incident ID ${failure.incidentId}` : undefined,
              }
            : null
        }
      />
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor="provider-kind">Provider</FieldLabel>
          <Select value={kind} onValueChange={chooseKind}>
            <SelectTrigger id="provider-kind" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {kinds.map((k) => (
                <SelectItem key={k} value={k}>
                  {providerKinds[k].label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {info.note ? <FieldDescription>{info.note}</FieldDescription> : null}
        </Field>

        <Field>
          <FieldLabel htmlFor="provider-name">Name</FieldLabel>
          <Input id="provider-name" value={name} onChange={(e) => setName(e.target.value)} />
          <FieldDescription>How this provider appears in tier and spend screens.</FieldDescription>
        </Field>

        {info.fields.map((field) => {
          const id = `cred-${field.key}`;
          const invalid = missing.has(field.key);
          if (field.toggle) {
            return (
              <Field key={field.key} orientation="horizontal">
                <Checkbox
                  id={id}
                  checked={values[field.key] === "true"}
                  onCheckedChange={(checked) =>
                    setValues((v) => ({ ...v, [field.key]: checked === true ? "true" : "" }))
                  }
                />
                <FieldLabel htmlFor={id}>{field.label}</FieldLabel>
              </Field>
            );
          }
          if (
            instanceRole &&
            (field.key === "access_key_id" || field.key === "secret_access_key")
          ) {
            return null;
          }
          const common = {
            id,
            value: values[field.key] ?? "",
            placeholder: field.placeholder,
            "aria-invalid": invalid,
            autoComplete: "off",
            spellCheck: false,
          };
          return (
            <Field key={field.key} data-invalid={invalid}>
              <FieldLabel htmlFor={id}>
                {field.label}
                {field.optional ? (
                  <span className="font-normal text-muted-foreground">optional</span>
                ) : null}
              </FieldLabel>
              {field.multiline ? (
                <Textarea
                  {...common}
                  rows={5}
                  className="font-mono text-xs"
                  onChange={(e) => setValues((v) => ({ ...v, [field.key]: e.target.value }))}
                />
              ) : (
                <Input
                  {...common}
                  type={field.secret ? "password" : "text"}
                  onChange={(e) => setValues((v) => ({ ...v, [field.key]: e.target.value }))}
                />
              )}
              {field.help ? <FieldDescription>{field.help}</FieldDescription> : null}
              {field.secret ? (
                <FieldDescription>Encrypted when saved. It is never shown again.</FieldDescription>
              ) : null}
              {invalid ? <FieldError>Required for this provider.</FieldError> : null}
            </Field>
          );
        })}

        <Field>
          <FieldLabel htmlFor="provider-residency">Where data goes</FieldLabel>
          <Select
            value={residency ?? info.residency}
            onValueChange={(v) => setResidency(v as DataResidency)}
          >
            <SelectTrigger id="provider-residency" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(residencyLabel) as DataResidency[]).map((r) => (
                <SelectItem key={r} value={r}>
                  {residencyLabel[r]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <FieldDescription>
            A project not approved for external AI can only use local providers.
          </FieldDescription>
        </Field>
      </FieldGroup>
      <Button type="submit" className="w-fit" disabled={create.isPending}>
        {create.isPending ? "Saving…" : submitLabel}
      </Button>
    </form>
  );
}
