import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";

import { isApiError } from "@/lib/api/errors";

/** Codes that mean "you are not signed in", as opposed to a wrong password. */
const UNAUTHENTICATED = new Set(["unauthenticated"]);

/**
 * Codes that mean the caller may not see this at all. Other 403s are about the
 * thing being asked for (a target not on the allowlist, a project without external
 * AI approval) and belong to the screen that asked, not to a global handler.
 */
const NO_ACCESS = new Set(["forbidden", "role_required", "not_project_member"]);

export type AccessHandlers = {
  onUnauthenticated: () => void;
  onNoAccess: (code: string) => void;
};

const MAX_NETWORK_RETRIES = 3;

/**
 * Retry only when the request never got an answer. A 4xx is the same answer next
 * time, and a 5xx already has an incident ID; retrying either hides the failure
 * behind a longer spinner.
 */
export function shouldRetry(failureCount: number, error: unknown): boolean {
  return isApiError(error) && error.isNetworkError && failureCount < MAX_NETWORK_RETRIES;
}

export function routeAccessError(error: unknown, handlers: AccessHandlers): boolean {
  if (!isApiError(error)) {
    return false;
  }
  if (error.status === 401 && UNAUTHENTICATED.has(error.code)) {
    handlers.onUnauthenticated();
    return true;
  }
  if (error.status === 403 && NO_ACCESS.has(error.code)) {
    handlers.onNoAccess(error.code);
    return true;
  }
  return false;
}

export function makeQueryClient(handlers: AccessHandlers): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache({
      onError: (error, query) => {
        // A background lookup (a breadcrumb label, say) marks itself quiet: its
        // failure is not the page's failure and must not navigate away.
        if (query.meta?.quiet) return;
        routeAccessError(error, handlers);
      },
    }),
    // A failed mutation still sends a signed-out user to login. A forbidden one is
    // left to the form that submitted it, which can say what was refused.
    mutationCache: new MutationCache({
      onError: (error) => {
        if (isApiError(error) && error.status === 401) {
          routeAccessError(error, handlers);
        }
      },
    }),
    defaultOptions: {
      queries: {
        // Long enough that moving between tabs does not refetch everything, short
        // enough that a list is never minutes old. Live state (jobs, runs) comes
        // over SSE and invalidates its own queries (FE-S.5).
        staleTime: 30_000,
        retry: shouldRetry,
      },
      mutations: {
        // A mutation is not retried automatically: repeating a write is a decision.
        retry: false,
      },
    },
  });
}
