"use client";

import { useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, RotateCcwIcon, TriangleAlertIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

import { useCurrentUser } from "@/components/auth/current-user";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { api, apiClient } from "@/lib/api/client";
import { type ApiError, isApiError } from "@/lib/api/errors";
import { hasRole } from "@/lib/auth/roles";
import { useFormat } from "@/lib/hooks/use-format";

import {
  fieldName,
  formValueOf,
  type SettingEntry,
  type SettingScope,
  type SettingValue,
  sourceLabel,
  validatorFor,
  writableScope,
} from "./registry";
import { SettingControl } from "./setting-control";

type FormValues = Record<string, unknown>;

function slug(category: string) {
  return `settings-${category.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
}

function SecretState({ value }: { value?: SettingValue }) {
  const format = useFormat();
  const secret = value?.secret;
  if (!secret?.isSet) {
    return <span className="text-sm text-muted-foreground">Not set</span>;
  }
  return (
    <span className="flex flex-wrap items-center gap-2 text-sm">
      <KeyRoundIcon aria-hidden className="size-4 text-muted-foreground" />
      <span>Set</span>
      {secret.hint ? (
        <code className="font-mono text-xs text-muted-foreground">{secret.hint}</code>
      ) : null}
      {secret.updatedAt ? (
        <span className="text-xs text-muted-foreground">
          updated {format.relative(secret.updatedAt)}
        </span>
      ) : null}
    </span>
  );
}

function CategorySection({
  category,
  entries,
  values,
  scope,
  projectID,
  queryKey,
  readOnlyReason,
}: {
  readOnlyReason?: string;
  category: string;
  entries: SettingEntry[];
  values: Map<string, SettingValue>;
  scope: SettingScope;
  projectID?: string;
  queryKey: readonly unknown[];
}) {
  const queryClient = useQueryClient();
  const [replacing, setReplacing] = useState<Set<string>>(new Set());
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [busyKey, setBusyKey] = useState<string | null>(null);

  const defaults = useMemo(
    () =>
      Object.fromEntries(
        entries.map((entry) => [fieldName(entry.key), formValueOf(entry, values.get(entry.key))]),
      ),
    [entries, values],
  );
  const form = useForm<FormValues>({ values: defaults, resetOptions: { keepDirtyValues: false } });
  const update = api.useMutation("put", "/api/v1/settings");

  const refresh = () => queryClient.invalidateQueries({ queryKey });

  const changed = entries.filter((entry) =>
    entry.kind === "secret"
      ? replacing.has(entry.key)
      : form.getFieldState(fieldName(entry.key), form.formState).isDirty,
  );

  const save = form.handleSubmit(async (raw) => {
    setFailure(null);
    const changes = [];
    let valid = true;
    for (const entry of changed) {
      const name = fieldName(entry.key);
      const parsed = validatorFor(entry).safeParse(raw[name]);
      if (!parsed.success) {
        valid = false;
        form.setError(name, { message: parsed.error.issues[0]?.message ?? "Not a valid value." });
        continue;
      }
      changes.push({
        key: entry.key,
        scope,
        ...(scope === "project" ? { projectID } : {}),
        value: parsed.data,
      });
    }
    if (!valid || changes.length === 0) return;

    try {
      await update.mutateAsync({ body: { changes } });
      setReplacing(new Set());
      await refresh();
      const restart = changed.some((entry) => entry.restartRequired);
      toast.success(`${changes.length === 1 ? "Setting" : `${changes.length} settings`} saved`, {
        description: restart
          ? "Some take effect only after the API and worker restart."
          : undefined,
      });
    } catch (error) {
      if (!isApiError(error)) throw error;
      const key = typeof error.details.key === "string" ? error.details.key : undefined;
      if (key && entries.some((e) => e.key === key)) {
        form.setError(fieldName(key), { message: error.message });
      } else {
        setFailure(error);
      }
    }
  });

  const clear = async (entry: SettingEntry) => {
    setBusyKey(entry.key);
    setFailure(null);
    try {
      await apiClient.DELETE("/api/v1/settings/{key}", {
        params: {
          path: { key: entry.key },
          query: { scope, ...(scope === "project" ? { projectID } : {}) },
        },
      });
      await refresh();
      toast.success(
        entry.isSecret ? `${entry.label} cleared` : `${entry.label} reset to inherited`,
      );
    } catch (error) {
      if (!isApiError(error)) throw error;
      setFailure(error);
    } finally {
      setBusyKey(null);
    }
  };

  return (
    <section
      id={slug(category)}
      aria-labelledby={`${slug(category)}-title`}
      className="scroll-mt-16"
    >
      <form onSubmit={save} noValidate className="rounded-lg border bg-card">
        <div className="flex items-center justify-between gap-4 border-b px-5 py-3">
          <h2 id={`${slug(category)}-title`} className="text-sm font-semibold">
            {category}
          </h2>
          <Button
            type="submit"
            size="sm"
            disabled={Boolean(readOnlyReason) || changed.length === 0 || update.isPending}
            title={readOnlyReason}
          >
            {update.isPending
              ? "Saving…"
              : changed.length > 1
                ? `Save ${changed.length} changes`
                : "Save"}
          </Button>
        </div>
        {readOnlyReason ? (
          <p className="border-b px-5 py-2 text-xs text-muted-foreground">{readOnlyReason}</p>
        ) : null}
        {failure ? (
          <div className="px-5 pt-4">
            <ErrorState title="Nothing was saved" error={failure} />
          </div>
        ) : null}
        <ul className="divide-y">
          {entries.map((entry) => {
            const value = values.get(entry.key);
            const name = fieldName(entry.key);
            const id = `setting-${name}`;
            const describedBy = `${id}-help`;
            const error = form.formState.errors[name]?.message as string | undefined;
            const overriddenHere = value?.source === scope && !value.fromDefault;
            const isReplacing = replacing.has(entry.key);
            const help = entry.helpText ?? (entry.schema as { description?: string }).description;

            return (
              <li
                key={entry.key}
                className="grid gap-3 px-5 py-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]"
              >
                <div className="flex flex-col gap-1">
                  <label
                    htmlFor={id}
                    className="flex flex-wrap items-center gap-2 text-sm font-medium"
                  >
                    {entry.label}
                    {entry.restartRequired ? (
                      <Badge variant="warning">Restart required</Badge>
                    ) : null}
                  </label>
                  {help ? (
                    <p id={describedBy} className="text-xs text-muted-foreground">
                      {help}
                    </p>
                  ) : null}
                  <p className="font-mono text-[11px] text-muted-foreground">{entry.key}</p>
                </div>
                <div className="flex flex-col gap-2">
                  {entry.kind === "secret" && !isReplacing ? (
                    <div className="flex flex-wrap items-center gap-2">
                      <SecretState value={value} />
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={Boolean(readOnlyReason)}
                        onClick={() => setReplacing((s) => new Set(s).add(entry.key))}
                      >
                        {value?.secret?.isSet ? "Replace" : "Set"}
                      </Button>
                      {overriddenHere && value?.secret?.isSet ? (
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          disabled={busyKey === entry.key}
                          onClick={() => void clear(entry)}
                        >
                          Clear
                        </Button>
                      ) : null}
                    </div>
                  ) : (
                    <div className="flex items-start gap-2">
                      <div className="min-w-0 flex-1">
                        <SettingControl
                          entry={entry}
                          id={id}
                          control={form.control}
                          register={form.register}
                          invalid={Boolean(error)}
                          describedBy={help ? describedBy : undefined}
                          disabled={Boolean(readOnlyReason)}
                        />
                      </div>
                      {isReplacing ? (
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => {
                            form.resetField(name);
                            setReplacing((s) => {
                              const next = new Set(s);
                              next.delete(entry.key);
                              return next;
                            });
                          }}
                        >
                          Cancel
                        </Button>
                      ) : null}
                    </div>
                  )}
                  {error ? (
                    <p role="alert" className="text-sm text-destructive">
                      {error}
                    </p>
                  ) : null}
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span>{sourceLabel(value, scope)}</span>
                    {overriddenHere && entry.kind !== "secret" ? (
                      <Button
                        type="button"
                        variant="link"
                        size="xs"
                        className="h-auto p-0 text-xs"
                        disabled={Boolean(readOnlyReason) || busyKey === entry.key}
                        onClick={() => void clear(entry)}
                      >
                        <RotateCcwIcon aria-hidden /> Reset to inherited
                      </Button>
                    ) : null}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
        {changed.some((e) => e.restartRequired) ? (
          <div className="border-t px-5 py-3">
            <Alert>
              <TriangleAlertIcon aria-hidden />
              <AlertTitle>Restart needed</AlertTitle>
              <AlertDescription>
                A change here is read once at start-up. It is saved now and takes effect when the
                API and worker next restart.
              </AlertDescription>
            </Alert>
          </div>
        ) : null}
      </form>
    </section>
  );
}

/**
 * Every setting a screen may write, rendered from the registry (FE-0.4). No key
 * is named in this file: a setting added to the Go registry appears here, with its
 * control, validation, and help, with no frontend change.
 */
export function SettingsForm({
  scope,
  projectID,
  readOnlyReason,
}: {
  scope: SettingScope;
  projectID?: string;
  /** When set, every control is disabled and this says why (an archived project, say). */
  readOnlyReason?: string;
}) {
  const user = useCurrentUser();
  const registry = api.useQuery("get", "/api/v1/settings/registry");
  const valuesInit = projectID ? { params: { query: { projectID } } } : {};
  const values = api.useQuery("get", "/api/v1/settings", valuesInit);
  const queryKey = ["get", "/api/v1/settings"] as const;

  const grouped = useMemo(() => {
    const groups = new Map<string, SettingEntry[]>();
    for (const entry of registry.data?.entries ?? []) {
      if (!hasRole(user, entry.minRole) || !writableScope(entry, scope)) continue;
      groups.set(entry.category, [...(groups.get(entry.category) ?? []), entry]);
    }
    return [...groups.entries()];
  }, [registry.data, user, scope]);

  const valueMap = useMemo(
    () => new Map((values.data?.values ?? []).map((v) => [v.key, v])),
    [values.data],
  );

  if (registry.isLoading || values.isLoading) {
    return (
      <div className="flex flex-col gap-4" aria-busy="true" aria-label="Loading settings">
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }
  const failure = registry.error ?? values.error;
  if (failure) {
    return (
      <ErrorState
        error={failure}
        onRetry={() => {
          void registry.refetch();
          void values.refetch();
        }}
      />
    );
  }
  if (grouped.length === 0) {
    return (
      <EmptyState
        title="Nothing to configure here"
        description="Your role has no settings at this level. An admin can change platform settings."
      />
    );
  }

  return (
    <div className="grid gap-8 lg:grid-cols-[12rem_minmax(0,1fr)]">
      <nav aria-label="Setting categories" className="hidden lg:block">
        <ul className="sticky top-4 flex flex-col gap-1 text-sm">
          {grouped.map(([category, entries]) => (
            <li key={category}>
              <a
                href={`#${slug(category)}`}
                className="flex justify-between rounded-md px-2 py-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                {category}
                <span className="tabular-nums">{entries.length}</span>
              </a>
            </li>
          ))}
        </ul>
      </nav>
      <div className="flex min-w-0 flex-col gap-6">
        {grouped.map(([category, entries]) => (
          <CategorySection
            key={category}
            category={category}
            entries={entries}
            values={valueMap}
            scope={scope}
            projectID={projectID}
            queryKey={queryKey}
            readOnlyReason={readOnlyReason}
          />
        ))}
      </div>
    </div>
  );
}
