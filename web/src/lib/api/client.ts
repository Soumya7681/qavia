import createFetchClient, { type Middleware } from "openapi-fetch";
import createQueryHooks from "openapi-react-query";

import { apiErrorFromFetchFailure, apiErrorFromResponse } from "./errors";
import type { paths } from "./schema";

export const CORRELATION_HEADER = "X-Correlation-ID";

/**
 * Every request carries a correlation ID, so a failure a user reports can be found
 * in the API's logs. One is generated per request unless the caller already set
 * one (a server component forwarding its own, for example).
 */
const correlation: Middleware = {
  onRequest({ request }) {
    if (!request.headers.has(CORRELATION_HEADER)) {
      request.headers.set(CORRELATION_HEADER, crypto.randomUUID());
    }
    return request;
  },
};

/**
 * Turns every failure into a thrown ApiError, so a caller never has to check an
 * `error` field and TanStack Query sees a rejection it can retry or report.
 */
const errors: Middleware = {
  async onResponse({ request, response }) {
    if (!response.ok) {
      throw await apiErrorFromResponse(
        response,
        request.headers.get(CORRELATION_HEADER) ?? undefined,
      );
    }
    return undefined;
  },
  onError({ request, error }) {
    const wrapped = apiErrorFromFetchFailure(
      error,
      request.headers.get(CORRELATION_HEADER) ?? undefined,
    );
    return wrapped instanceof Error ? wrapped : undefined;
  },
};

export type ApiClientOptions = {
  /**
   * Empty in the browser: calls go to the relative /api path, which Next rewrites
   * to the API, so the session cookie is same-origin. A server component passes
   * the absolute API address and forwards the caller's cookie in `headers`.
   */
  baseUrl?: string;
  headers?: HeadersInit;
  fetch?: typeof globalThis.fetch;
};

/**
 * The only way the web app talks to the API. Request and response types come from
 * the generated schema; a hand-written fetch anywhere else fails review
 * (work-frontend.md rule 1).
 */
export function createApiClient({ baseUrl = "", headers, fetch }: ApiClientOptions = {}) {
  const client = createFetchClient<paths>({
    baseUrl,
    headers,
    credentials: "same-origin",
    // Looked up per call rather than captured once, so anything that wraps the
    // global fetch after this module loads (instrumentation, test mocks) is honoured.
    fetch: (request: Request) => (fetch ?? globalThis.fetch)(request),
  });
  client.use(correlation, errors);
  return client;
}

export type ApiClient = ReturnType<typeof createApiClient>;

/**
 * The browser client. Its base is the page's own origin, which is what a relative
 * URL would resolve to anyway; spelling it out keeps the client usable where
 * `Request` will not resolve a relative URL (tests, workers).
 */
export const apiClient = createApiClient({
  baseUrl: typeof window === "undefined" ? "" : window.location.origin,
});

/**
 * Typed TanStack Query hooks over the browser client:
 * `api.useQuery("get", "/api/v1/projects/{projectID}", { params: { path: { projectID } } })`.
 *
 * Query keys are `[method, path, init]`, built by the library from the same
 * arguments, so a key cannot drift from the request it caches. Invalidate with
 * `queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/projects"] })`.
 */
export const api = createQueryHooks(apiClient);
