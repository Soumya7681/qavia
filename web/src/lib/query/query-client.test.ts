import { describe, expect, it, vi } from "vitest";

import { ApiError, ClientErrorCode } from "@/lib/api/errors";

import { routeAccessError, shouldRetry } from "./query-client";

const error = (status: number, code: string) => new ApiError({ status, code, message: code });

describe("shouldRetry", () => {
  it("retries a request that got no answer, up to three times", () => {
    const offline = error(0, ClientErrorCode.Network);
    expect(shouldRetry(0, offline)).toBe(true);
    expect(shouldRetry(2, offline)).toBe(true);
    expect(shouldRetry(3, offline)).toBe(false);
  });

  it("never retries an answer", () => {
    expect(shouldRetry(0, error(404, "project_not_found"))).toBe(false);
    expect(shouldRetry(0, error(500, "internal_error"))).toBe(false);
    expect(shouldRetry(0, new Error("not an api error"))).toBe(false);
  });
});

describe("routeAccessError", () => {
  const handlers = () => ({ onUnauthenticated: vi.fn(), onNoAccess: vi.fn() });

  it("sends a signed-out caller to login", () => {
    const h = handlers();
    expect(routeAccessError(error(401, "unauthenticated"), h)).toBe(true);
    expect(h.onUnauthenticated).toHaveBeenCalledOnce();
  });

  it("leaves a wrong password to the login form", () => {
    const h = handlers();
    expect(routeAccessError(error(401, "invalid_credentials"), h)).toBe(false);
    expect(h.onUnauthenticated).not.toHaveBeenCalled();
  });

  it("routes a no-access 403 to the permission screen with its code", () => {
    for (const code of ["forbidden", "role_required", "not_project_member"]) {
      const h = handlers();
      expect(routeAccessError(error(403, code), h)).toBe(true);
      expect(h.onNoAccess).toHaveBeenCalledWith(code);
    }
  });

  it("leaves a 403 about the request itself to the screen that made it", () => {
    const h = handlers();
    expect(routeAccessError(error(403, "target_host_not_allowed"), h)).toBe(false);
    expect(routeAccessError(error(403, "external_ai_not_approved"), h)).toBe(false);
    expect(h.onNoAccess).not.toHaveBeenCalled();
  });
});
