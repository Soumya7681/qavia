"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api/client";
import { type ApiError, isApiError } from "@/lib/api/errors";
import { authMessage } from "@/lib/auth/messages";
import { safeNextPath } from "@/lib/auth/next-path";
import { useNow } from "@/lib/hooks/use-now";

import { FormAlert } from "./form-alert";

// Only what the browser can check without telling an attacker anything: an email
// that looks like one and a password that is not empty. Length is not checked
// here, deliberately (see LoginRequest in qavia.yaml).
const schema = z.object({
  email: z.email("Enter the email address you were invited with."),
  password: z.string().min(1, "Enter your password."),
});
type Values = z.infer<typeof schema>;

export function LoginForm() {
  const router = useRouter();
  const search = useSearchParams();
  const queryClient = useQueryClient();
  const [failure, setFailure] = useState<{ error: ApiError; receivedAt: Date } | null>(null);
  const now = useNow(Boolean(failure?.error.details.retryAfterSeconds));
  const message = failure
    ? authMessage(failure.error, { receivedAt: failure.receivedAt, now })
    : null;
  const locked = message?.retryAt && message.retryAt > now;

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { email: "", password: "" },
  });
  const login = api.useMutation("post", "/api/v1/auth/login");

  const onSubmit = form.handleSubmit(async (values) => {
    setFailure(null);
    try {
      await login.mutateAsync({ body: values });
      queryClient.clear();
      router.replace(safeNextPath(search.get("next")) ?? "/");
      router.refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      setFailure({ error, receivedAt: new Date() });
      // Cleared for the next attempt, without flagging the now-empty field: the
      // message above is the only thing that went wrong.
      form.setValue("password", "", { shouldValidate: false, shouldDirty: false });
      form.clearErrors("password");
      form.setFocus("password");
    }
  });

  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
      <FormAlert message={message} />
      <FieldGroup>
        <Field data-invalid={Boolean(form.formState.errors.email)}>
          <FieldLabel htmlFor="email">Email</FieldLabel>
          <Input
            id="email"
            type="email"
            autoComplete="username"
            autoFocus
            aria-invalid={Boolean(form.formState.errors.email)}
            {...form.register("email")}
          />
          <FieldError errors={[form.formState.errors.email]} />
        </Field>
        <Field data-invalid={Boolean(form.formState.errors.password)}>
          <FieldLabel htmlFor="password">Password</FieldLabel>
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            aria-invalid={Boolean(form.formState.errors.password)}
            {...form.register("password")}
          />
          <FieldError errors={[form.formState.errors.password]} />
        </Field>
      </FieldGroup>
      <Button type="submit" disabled={form.formState.isSubmitting || Boolean(locked)}>
        {form.formState.isSubmitting ? "Signing in…" : "Sign in"}
      </Button>
      <p className="text-xs text-muted-foreground">
        No account? Qavia is invite only. Ask an admin to invite you.
      </p>
    </form>
  );
}
