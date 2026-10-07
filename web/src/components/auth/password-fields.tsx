import type { FieldErrors, UseFormRegister } from "react-hook-form";

import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

type Values = { newPassword: string; confirmPassword: string };

/**
 * A new password and its confirmation. The strength rule is the server's alone:
 * it lives in one place, and its message is shown here when a submit is refused.
 * The browser only checks that the two entries match.
 */
export function NewPasswordFields<T extends Values>({
  register,
  errors,
  serverError,
}: {
  register: UseFormRegister<T>;
  errors: FieldErrors<T>;
  serverError?: string;
}) {
  const reg = register as unknown as UseFormRegister<Values>;
  const errs = errors as FieldErrors<Values>;
  const newError = errs.newPassword ?? (serverError ? { message: serverError } : undefined);
  return (
    <>
      <Field data-invalid={Boolean(newError)}>
        <FieldLabel htmlFor="newPassword">New password</FieldLabel>
        <Input
          id="newPassword"
          type="password"
          autoComplete="new-password"
          aria-invalid={Boolean(newError)}
          aria-describedby="newPassword-hint"
          {...reg("newPassword")}
        />
        <FieldDescription id="newPassword-hint">
          A passphrase of several ordinary words is long, strong, and easy to remember.
        </FieldDescription>
        <FieldError errors={[newError]} />
      </Field>
      <Field data-invalid={Boolean(errs.confirmPassword)}>
        <FieldLabel htmlFor="confirmPassword">Type it again</FieldLabel>
        <Input
          id="confirmPassword"
          type="password"
          autoComplete="new-password"
          aria-invalid={Boolean(errs.confirmPassword)}
          {...reg("confirmPassword")}
        />
        <FieldError errors={[errs.confirmPassword]} />
      </Field>
    </>
  );
}

export const passwordsMatch = <T extends Values>(v: T) => v.newPassword === v.confirmPassword;
export const mismatch = { message: "The two passwords are different.", path: ["confirmPassword"] };
