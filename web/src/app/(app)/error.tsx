"use client";

import { useEffect } from "react";

import { ErrorState } from "@/components/ui/error-state";
import { isApiError } from "@/lib/api/errors";

/**
 * The global error boundary for the app. An ApiError thrown in the browser
 * carries its incident ID; an error from a server component reaches here with
 * only a digest, which the server logged beside the full cause.
 */
export default function AppError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    console.error(error);
  }, [error]);

  const incidentId = isApiError(error) ? error.incidentId : error.digest;
  const message = isApiError(error)
    ? error.message
    : "Something went wrong showing this page. It has been logged.";

  return (
    <div className="mx-auto max-w-xl pt-12">
      <ErrorState
        title="This page could not be shown"
        error={{ message, incidentId }}
        onRetry={reset}
      />
    </div>
  );
}
