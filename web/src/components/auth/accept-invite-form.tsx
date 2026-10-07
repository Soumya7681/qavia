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
import { ApiError, isApiError } from "@/lib/api/errors";
import { type AuthMessage, authMessage } from "@/lib/auth/messages";

import { FormAlert } from "./form-alert";
import { mismatch, NewPasswordFields, passwordsMatch } from "./password-fields";

const schema = z
  .object({
    name: z.string().trim().min(1, "Enter the name your teammates know you by.").max(200),
    newPassword: z.string().min(1, "Choose a password."),
    confirmPassword: z.string(),
  })
  .refine(passwordsMatch, mismatch);
type Values = z.infer<typeof schema>;

export function AcceptInviteForm() {
  const router = useRouter();
  const token = useSearchParams().get("token") ?? "";
  const queryClient = useQueryClient();
  const [message, setMessage] = useState<AuthMessage | null>(
    // A link with no token is the same dead end as an expired one.
    token ? null : authMessage(new ApiError({ code: "invite_invalid", status: 400, message: "" })),
  );
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", newPassword: "", confirmPassword: "" },
  });
  const accept = api.useMutation("post", "/api/v1/auth/accept-invite");

  const onSubmit = form.handleSubmit(async ({ name, newPassword }) => {
    setMessage(null);
    try {
      await accept.mutateAsync({ body: { token, name, password: newPassword } });
      queryClient.clear();
      router.replace("/");
      router.refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      setMessage(authMessage(error));
    }
  });

  const passwordError = message?.field === "password" ? message.title : undefined;

  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
      <FormAlert message={message && message.field !== "password" ? message : null} />
      <FieldGroup>
        <Field data-invalid={Boolean(form.formState.errors.name)}>
          <FieldLabel htmlFor="name">Your name</FieldLabel>
          <Input id="name" autoComplete="name" autoFocus {...form.register("name")} />
          <FieldError errors={[form.formState.errors.name]} />
        </Field>
        <NewPasswordFields
          register={form.register}
          errors={form.formState.errors}
          serverError={passwordError}
        />
      </FieldGroup>
      <Button type="submit" disabled={!token || form.formState.isSubmitting}>
        {form.formState.isSubmitting ? "Setting up…" : "Join Qavia"}
      </Button>
    </form>
  );
}
