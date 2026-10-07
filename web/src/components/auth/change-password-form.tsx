"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { type AuthMessage, authMessage } from "@/lib/auth/messages";

import { FormAlert } from "./form-alert";
import { mismatch, NewPasswordFields, passwordsMatch } from "./password-fields";

const schema = z
  .object({
    currentPassword: z.string().min(1, "Enter your current password."),
    newPassword: z.string().min(1, "Choose a new password."),
    confirmPassword: z.string(),
  })
  .refine(passwordsMatch, mismatch);
type Values = z.infer<typeof schema>;

export function ChangePasswordForm() {
  const [message, setMessage] = useState<AuthMessage | null>(null);
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { currentPassword: "", newPassword: "", confirmPassword: "" },
  });
  const change = api.useMutation("post", "/api/v1/auth/change-password");

  const onSubmit = form.handleSubmit(async ({ currentPassword, newPassword }) => {
    setMessage(null);
    try {
      await change.mutateAsync({ body: { currentPassword, newPassword } });
      form.reset();
      toast.success("Password changed", {
        description: "Every other browser signed in as you has been signed out.",
      });
    } catch (error) {
      if (!isApiError(error)) throw error;
      if (error.code === "invalid_credentials") {
        form.setError("currentPassword", { message: "That is not your current password." });
        form.setFocus("currentPassword");
        return;
      }
      setMessage(authMessage(error));
    }
  });

  const passwordError = message?.field === "password" ? message.title : undefined;

  return (
    <form onSubmit={onSubmit} noValidate className="flex max-w-sm flex-col gap-5">
      <FormAlert message={message && message.field !== "password" ? message : null} />
      <FieldGroup>
        <Field data-invalid={Boolean(form.formState.errors.currentPassword)}>
          <FieldLabel htmlFor="currentPassword">Current password</FieldLabel>
          <Input
            id="currentPassword"
            type="password"
            autoComplete="current-password"
            aria-invalid={Boolean(form.formState.errors.currentPassword)}
            {...form.register("currentPassword")}
          />
          <FieldError errors={[form.formState.errors.currentPassword]} />
        </Field>
        <NewPasswordFields
          register={form.register}
          errors={form.formState.errors}
          serverError={passwordError}
        />
      </FieldGroup>
      <Button type="submit" className="w-fit" disabled={form.formState.isSubmitting}>
        {form.formState.isSubmitting ? "Changing…" : "Change password"}
      </Button>
    </form>
  );
}
