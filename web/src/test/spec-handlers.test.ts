import { describe, expect, it } from "vitest";

import type { components } from "@/lib/api/schema";

import { specExample, specHandlers } from "./spec-handlers";

describe("spec-seeded stubs", () => {
  it("builds a body with the contract's shape", () => {
    const user = specExample<components["schemas"]["User"]>("getCurrentUser");
    expect(user).toMatchObject({
      id: expect.any(String),
      email: expect.any(String),
      role: expect.stringMatching(/^(admin|qa_lead|qa_engineer|viewer)$/),
    });
    expect(user).not.toHaveProperty("passwordHash");
  });

  it("covers every operation", () => {
    expect(specHandlers().length).toBeGreaterThan(100);
  });

  it("names an unknown operation instead of returning nothing", () => {
    expect(() => specExample("noSuchOperation")).toThrow(/not in qavia.yaml/);
  });
});
