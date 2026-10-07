"use client";

import "./globals.css";

// Last resort, when the root layout itself failed. It replaces the whole document,
// so it carries its own <html> and no providers.
export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <html lang="en">
      <body className="flex min-h-svh items-center justify-center p-6">
        <main className="flex max-w-md flex-col gap-3">
          <h1 className="text-lg font-semibold">Qavia could not load</h1>
          <p className="text-sm text-muted-foreground">
            Reload to try again. If it keeps happening, send this to whoever runs Qavia.
          </p>
          {error.digest ? (
            <p className="text-xs text-muted-foreground">
              Incident ID <code className="font-mono select-all">{error.digest}</code>
            </p>
          ) : null}
          <button
            type="button"
            onClick={reset}
            className="w-fit rounded-md border px-3 py-1.5 text-sm"
          >
            Try again
          </button>
        </main>
      </body>
    </html>
  );
}
