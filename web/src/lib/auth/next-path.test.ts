import { describe, expect, it } from "vitest";

import { loginPath, safeNextPath } from "./next-path";

describe("safeNextPath", () => {
  it("keeps a path on this site", () => {
    expect(safeNextPath("/projects/abc?tab=runs")).toBe("/projects/abc?tab=runs");
  });

  it("refuses anything that leaves the site", () => {
    for (const value of [
      "https://evil.test",
      "//evil.test",
      "/\\evil.test",
      "javascript:alert(1)",
      "evil",
    ]) {
      expect(safeNextPath(value)).toBeUndefined();
    }
  });

  it("does not send a sign-in back to the login page", () => {
    expect(safeNextPath("/login?next=/x")).toBeUndefined();
  });
});

describe("loginPath", () => {
  it("encodes the return path", () => {
    expect(loginPath("/projects/a?b=c")).toBe("/login?next=%2Fprojects%2Fa%3Fb%3Dc");
    expect(loginPath("//evil.test")).toBe("/login");
    expect(loginPath(undefined)).toBe("/login");
  });
});
