import { describe, expect, it } from "vitest";

import { formatBytes, formatDateTime, formatDuration, formatRelative, formatUSD } from "./format";

describe("format", () => {
  const instant = "2026-10-07T08:30:00Z";

  it("shows a timestamp in the user's zone, not the machine's", () => {
    expect(formatDateTime(instant, { timeZone: "Asia/Kolkata" })).toBe("7 Oct 2026, 14:00");
    expect(formatDateTime(instant, { timeZone: "America/New_York" })).toBe("7 Oct 2026, 04:30");
  });

  it("survives an unknown zone name", () => {
    expect(() => formatDateTime(instant, { timeZone: "Mars/Olympus" })).not.toThrow();
  });

  it("formats relative time", () => {
    const now = new Date("2026-10-07T08:33:00Z");
    expect(formatRelative(instant, { now })).toBe("3 minutes ago");
    expect(formatRelative("2026-10-07T10:33:00Z", { now })).toBe("in 2 hours");
  });

  it("formats durations the way a run summary reads", () => {
    expect(formatDuration(850)).toBe("850ms");
    expect(formatDuration(134_000)).toBe("2m 14s");
    expect(formatDuration(3_780_000)).toBe("1h 03m");
    expect(formatDuration(-1)).toBe("–");
  });

  it("keeps sub-cent AI spend visible", () => {
    expect(formatUSD(12.5)).toBe("US$12.50");
    expect(formatUSD(0.0042)).toBe("US$0.0042");
  });

  it("formats sizes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(50 * 1024 * 1024)).toBe("50 MB");
  });
});
