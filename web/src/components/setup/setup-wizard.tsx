"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2Icon, CircleAlertIcon, LoaderIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { ProviderForm } from "@/components/ai/provider-form";
import { FormAlert } from "@/components/auth/form-alert";
import { mismatch, NewPasswordFields, passwordsMatch } from "@/components/auth/password-fields";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { api, apiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";
import { type AuthMessage, authMessage } from "@/lib/auth/messages";
import { cn } from "@/lib/utils";

type SetupStatus = components["schemas"]["SetupStatus"];
type Step = "admin" | "storage" | "ai";

const steps: { id: Step; label: string; requirement: string }[] = [
  { id: "admin", label: "Admin account", requirement: "Required" },
  { id: "storage", label: "Storage", requirement: "Required" },
  { id: "ai", label: "AI provider", requirement: "Needed before AI jobs run" },
];

function Steps({ current }: { current: Step }) {
  const index = steps.findIndex((s) => s.id === current);
  return (
    <ol className="flex flex-col gap-2 sm:flex-row sm:gap-6" aria-label="Setup steps">
      {steps.map((step, i) => (
        <li
          key={step.id}
          aria-current={i === index ? "step" : undefined}
          className={cn("flex items-center gap-2 text-sm", i > index && "text-muted-foreground")}
        >
          <span
            className={cn(
              "flex size-6 items-center justify-center rounded-full border text-xs tabular-nums",
              i < index && "border-primary bg-primary text-primary-foreground",
              i === index && "border-primary text-primary",
            )}
          >
            {i < index ? <CheckCircle2Icon aria-hidden className="size-4" /> : i + 1}
          </span>
          <span>
            {step.label}
            <span className="sr-only">{i < index ? ", done" : ""}</span>
          </span>
        </li>
      ))}
    </ol>
  );
}

const adminSchema = z
  .object({
    name: z.string().trim().min(1, "Enter your name.").max(200),
    email: z.email("Enter an email address."),
    newPassword: z.string().min(1, "Choose a password."),
    confirmPassword: z.string(),
  })
  .refine(passwordsMatch, mismatch);
type AdminValues = z.infer<typeof adminSchema>;

function AdminStep({ onDone }: { onDone: () => void }) {
  const router = useRouter();
  const [message, setMessage] = useState<AuthMessage | null>(null);
  const form = useForm<AdminValues>({
    resolver: zodResolver(adminSchema),
    defaultValues: { name: "", email: "", newPassword: "", confirmPassword: "" },
  });
  const create = api.useMutation("post", "/api/v1/setup/admin");

  const onSubmit = form.handleSubmit(async ({ name, email, newPassword }) => {
    setMessage(null);
    try {
      await create.mutateAsync({
        body: {
          name,
          email,
          password: newPassword,
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
        },
      });
      onDone();
    } catch (error) {
      if (!isApiError(error)) throw error;
      if (error.code === "setup_already_complete") {
        // Someone else finished first. There is nothing left to set up here.
        router.replace("/login");
        return;
      }
      setMessage(authMessage(error));
    }
  });

  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
      <div>
        <h2 className="text-base font-semibold">Create the first admin</h2>
        <p className="text-sm text-muted-foreground">
          This account can invite everyone else. It is the only account that can be created without
          an invitation, and only once.
        </p>
      </div>
      <FormAlert message={message && message.field !== "password" ? message : null} />
      <FieldGroup>
        <Field data-invalid={Boolean(form.formState.errors.name)}>
          <FieldLabel htmlFor="name">Your name</FieldLabel>
          <Input id="name" autoComplete="name" autoFocus {...form.register("name")} />
          <FieldError errors={[form.formState.errors.name]} />
        </Field>
        <Field data-invalid={Boolean(form.formState.errors.email)}>
          <FieldLabel htmlFor="email">Work email</FieldLabel>
          <Input id="email" type="email" autoComplete="username" {...form.register("email")} />
          <FieldError errors={[form.formState.errors.email]} />
        </Field>
        <NewPasswordFields
          register={form.register}
          errors={form.formState.errors}
          serverError={message?.field === "password" ? message.title : undefined}
        />
      </FieldGroup>
      <Button type="submit" className="w-fit" disabled={form.formState.isSubmitting}>
        {form.formState.isSubmitting ? "Creating…" : "Create admin and continue"}
      </Button>
    </form>
  );
}

function StorageStep({ initial, onDone }: { initial: SetupStatus; onDone: () => void }) {
  const status = useQuery({
    queryKey: ["get", "/api/v1/setup/status"],
    queryFn: async () => (await apiClient.GET("/api/v1/setup/status")).data!,
    initialData: initial,
    staleTime: 0,
  });
  const reachable = status.data.storageReachable;

  return (
    <section className="flex flex-col gap-5">
      <div>
        <h2 className="text-base font-semibold">Check storage</h2>
        <p className="text-sm text-muted-foreground">
          Uploads, logs, screenshots, and reports are kept here. Local disk is the built-in default
          and needs nothing configured.
        </p>
      </div>
      <div
        role="status"
        className={cn(
          "flex items-start gap-3 rounded-lg border p-4",
          reachable
            ? "border-success/30 bg-success-wash"
            : "border-destructive/30 bg-destructive-wash",
        )}
      >
        {status.isFetching ? (
          <LoaderIcon aria-hidden className="mt-0.5 size-4 animate-spin" />
        ) : reachable ? (
          <CheckCircle2Icon aria-hidden className="mt-0.5 size-4 text-success" />
        ) : (
          <CircleAlertIcon aria-hidden className="mt-0.5 size-4 text-destructive" />
        )}
        <div className="flex flex-col gap-0.5 text-sm">
          <p className="font-medium">
            {reachable ? "Storage is reachable" : "Storage cannot be reached"}
            <Badge variant="outline" className="ml-2 align-middle font-mono">
              {status.data.storageProvider}
            </Badge>
          </p>
          {status.data.storageDetail ? (
            <p className="text-muted-foreground">{status.data.storageDetail}</p>
          ) : null}
          {!reachable ? (
            <p className="text-muted-foreground">
              Fix the storage location the server was started with, then check again. Setup cannot
              finish until files can be stored.
            </p>
          ) : null}
        </div>
      </div>
      <div className="flex gap-2">
        <Button onClick={onDone} disabled={!reachable}>
          Continue
        </Button>
        <Button
          variant="outline"
          onClick={() => void status.refetch()}
          disabled={status.isFetching}
        >
          Check again
        </Button>
      </div>
    </section>
  );
}

function AiStep({ onDone }: { onDone: () => void }) {
  const providers = api.useQuery("get", "/api/v1/ai/providers");
  const [added, setAdded] = useState<string | null>(null);

  return (
    <section className="flex flex-col gap-5">
      <div className="flex flex-col gap-1">
        <h2 className="flex items-center gap-2 text-base font-semibold">
          Add an AI provider <Badge variant="info">Needed before AI jobs run</Badge>
        </h2>
        <p className="text-sm text-muted-foreground">
          Generation, analysis, and code writing need a model. Everything else works without one, so
          you can add this later in AI providers.
        </p>
      </div>
      {added ? (
        <div
          role="status"
          className="flex items-start gap-3 rounded-lg border border-success/30 bg-success-wash p-4 text-sm"
        >
          <CheckCircle2Icon aria-hidden className="mt-0.5 size-4 text-success" />
          <p>
            <span className="font-medium">{added} added.</span> Test it and assign models to tiers
            in AI providers.
          </p>
        </div>
      ) : providers.data ? (
        <ProviderForm
          kinds={providers.data.kinds}
          makeDefault={providers.data.items.length === 0}
          onCreated={(provider) => setAdded(provider.name)}
        />
      ) : (
        <p className="text-sm text-muted-foreground">Loading provider kinds…</p>
      )}
      <div className="flex items-center gap-3 border-t pt-4">
        <Button onClick={onDone} variant={added ? "default" : "outline"}>
          {added ? "Finish setup" : "Skip for now"}
        </Button>
        {!added ? <p className="text-xs text-muted-foreground">You can add this later.</p> : null}
      </div>
    </section>
  );
}

export function SetupWizard({ initialStatus }: { initialStatus: SetupStatus }) {
  const router = useRouter();
  const [step, setStep] = useState<Step>("admin");

  return (
    <div className="flex w-full max-w-xl flex-col gap-8">
      <div className="flex flex-col gap-1">
        <p className="text-sm font-semibold tracking-tight">Qavia</p>
        <h1 className="text-xl font-semibold">Set up Qavia</h1>
        <p className="text-sm text-muted-foreground">
          Three steps. Only the first two are required to start.
        </p>
      </div>
      <Steps current={step} />
      {step === "admin" ? <AdminStep onDone={() => setStep("storage")} /> : null}
      {step === "storage" ? (
        <StorageStep initial={initialStatus} onDone={() => setStep("ai")} />
      ) : null}
      {step === "ai" ? (
        <AiStep
          onDone={() => {
            router.replace("/");
            router.refresh();
          }}
        />
      ) : null}
    </div>
  );
}
