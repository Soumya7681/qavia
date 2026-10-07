import { describe, expect, it, vi } from "vitest";

import { CORRELATION_HEADER, createApiClient } from "./client";
import { ApiError, ClientErrorCode } from "./errors";

function respond(status: number, body: unknown, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function clientWith(fetchImpl: (request: Request) => Promise<Response>) {
  // openapi-fetch always calls fetch with a single Request.
  const fetch = vi.fn((input: RequestInfo | URL) => fetchImpl(input as Request));
  return { client: createApiClient({ baseUrl: "http://api.test", fetch }), fetch };
}

describe("createApiClient", () => {
  it("returns typed data on success", async () => {
    const { client } = clientWith(async () =>
      respond(200, {
        complete: false,
        adminExists: false,
        storageReachable: true,
        storageProvider: "local-disk",
        aiProviderConfigured: false,
      }),
    );

    const { data } = await client.GET("/api/v1/setup/status");
    expect(data?.storageProvider).toBe("local-disk");
  });

  it("sends a correlation ID on every request", async () => {
    const { client, fetch } = clientWith(async () => respond(200, { status: "ok", version: "t" }));

    await client.GET("/healthz");
    await client.GET("/healthz");

    const ids = fetch.mock.calls.map(([request]) =>
      (request as Request).headers.get(CORRELATION_HEADER),
    );
    expect(ids[0]).toMatch(/^[0-9a-f-]{36}$/);
    expect(ids[1]).not.toBe(ids[0]);
  });

  it("keeps a correlation ID the caller already set", async () => {
    const { client, fetch } = clientWith(async () => respond(200, { status: "ok", version: "t" }));

    await client.GET("/healthz", { headers: { [CORRELATION_HEADER]: "from-server-component" } });
    expect((fetch.mock.calls[0][0] as Request).headers.get(CORRELATION_HEADER)).toBe(
      "from-server-component",
    );
  });

  it("throws the API's error envelope as an ApiError", async () => {
    const { client } = clientWith(async () =>
      respond(
        403,
        {
          code: "target_host_not_allowed",
          message: "Host is not on the allowlist.",
          details: { host: "x.test" },
        },
        { [CORRELATION_HEADER]: "corr-1" },
      ),
    );

    const failure = await client.GET("/api/v1/setup/status").catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(ApiError);
    expect(failure).toMatchObject({
      code: "target_host_not_allowed",
      status: 403,
      message: "Host is not on the allowlist.",
      details: { host: "x.test" },
      correlationId: "corr-1",
    });
  });

  it("carries the incident ID of a server error", async () => {
    const { client } = clientWith(async () =>
      respond(500, { code: "internal_error", message: "Something failed.", incidentId: "inc_1" }),
    );

    await expect(client.GET("/api/v1/setup/status")).rejects.toMatchObject({
      code: "internal_error",
      incidentId: "inc_1",
    });
  });

  it("reports a response that is not the envelope without inventing an API code", async () => {
    const { client } = clientWith(
      async () =>
        new Response("<html>Bad gateway</html>", { status: 502, statusText: "Bad Gateway" }),
    );

    await expect(client.GET("/api/v1/setup/status")).rejects.toMatchObject({
      code: ClientErrorCode.UnexpectedResponse,
      status: 502,
    });
  });

  it("turns a fetch failure into a network error", async () => {
    const { client } = clientWith(async () => {
      throw new TypeError("fetch failed");
    });

    const failure = await client.GET("/api/v1/setup/status").catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(ApiError);
    expect((failure as ApiError).isNetworkError).toBe(true);
    expect((failure as ApiError).status).toBe(0);
  });

  it("lets an abort through as an abort", async () => {
    const { client } = clientWith(async () => {
      throw new DOMException("aborted", "AbortError");
    });

    await expect(client.GET("/api/v1/setup/status")).rejects.toMatchObject({ name: "AbortError" });
  });
});
