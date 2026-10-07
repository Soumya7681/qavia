import { describe, expect, it } from "vitest";

import { hasRole } from "./roles";

describe("hasRole", () => {
  it("orders roles the way the API does", () => {
    expect(hasRole({ role: "admin" }, "qa_lead")).toBe(true);
    expect(hasRole({ role: "qa_lead" }, "qa_lead")).toBe(true);
    expect(hasRole({ role: "qa_engineer" }, "qa_lead")).toBe(false);
    expect(hasRole({ role: "viewer" }, "qa_engineer")).toBe(false);
  });

  it("shows nothing to nobody", () => {
    expect(hasRole(null, "viewer")).toBe(false);
    expect(hasRole(undefined, "viewer")).toBe(false);
  });
});
