import { CircleAlertIcon } from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import type { AuthMessage } from "@/lib/auth/messages";

/** A form-level error, announced to assistive technology as it appears. */
export function FormAlert({ message }: { message: AuthMessage | null }) {
  if (!message) return null;
  return (
    <Alert variant="destructive" aria-live="assertive">
      <CircleAlertIcon aria-hidden />
      <AlertTitle>{message.title}</AlertTitle>
      {message.description ? <AlertDescription>{message.description}</AlertDescription> : null}
    </Alert>
  );
}
