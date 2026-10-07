// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";

import { renderHookWithProviders, testUser } from "@/test/render";

import { useFormat } from "./use-format";

describe("useFormat", () => {
  it("uses the signed-in user's timezone", () => {
    const { result } = renderHookWithProviders(() => useFormat(), {
      user: { ...testUser, timezone: "Asia/Kolkata" },
    });
    expect(result.current.dateTime("2026-10-07T08:30:00Z")).toBe("7 Oct 2026, 14:00");
  });

  it("changes every timestamp when the timezone changes", () => {
    const { result } = renderHookWithProviders(() => useFormat(), {
      user: { ...testUser, timezone: "America/New_York" },
    });
    expect(result.current.dateTime("2026-10-07T08:30:00Z")).toBe("7 Oct 2026, 04:30");
  });
});
