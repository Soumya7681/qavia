import { CircleAlertIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** The parts of the API error envelope this component shows. */
export type DisplayableError = {
  code?: string;
  message: string;
  /** Present on a 5xx: the ID support uses to find the server log line. */
  incidentId?: string;
};

type ErrorStateProps = {
  title?: string;
  error: DisplayableError;
  onRetry?: () => void;
  className?: string;
};

// The error quarter of the four states. It shows the API's own message, which is
// written for a reader, and the incident ID when there is one, so a report to
// support carries something that can be looked up.
export function ErrorState({
  title = "This could not be loaded",
  error,
  onRetry,
  className,
}: ErrorStateProps) {
  return (
    <section
      role="alert"
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg border border-destructive/30 bg-destructive-wash p-6",
        className,
      )}
    >
      <CircleAlertIcon aria-hidden className="size-5 text-destructive" />
      <div className="flex max-w-prose flex-col gap-1">
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        <p className="text-sm text-foreground/80">{error.message}</p>
        {error.incidentId ? (
          <p className="text-xs text-muted-foreground">
            Incident ID{" "}
            <code className="rounded-sm bg-background/60 px-1 py-0.5 font-mono select-all">
              {error.incidentId}
            </code>
          </p>
        ) : null}
      </div>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Try again
        </Button>
      ) : null}
    </section>
  );
}
