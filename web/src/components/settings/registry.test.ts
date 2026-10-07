import { describe, expect, it } from "vitest";

import { fieldName, keyOf, type SettingEntry, sourceLabel, validatorFor } from "./registry";

const entry = (over: Partial<SettingEntry>): SettingEntry => ({
  key: "x.y",
  category: "Test",
  label: "Thing",
  kind: "string",
  scopes: ["global"],
  minRole: "admin",
  isSecret: false,
  restartRequired: false,
  schema: { type: "string" },
  ...over,
});

describe("validatorFor", () => {
  it("enforces bounds from the registry's own schema", () => {
    const v = validatorFor(
      entry({ kind: "int", schema: { type: "integer", minimum: 1, maximum: 5 } }),
    );
    expect(v.safeParse(3).success).toBe(true);
    expect(v.safeParse(9).success).toBe(false);
    expect(v.safeParse(2.5).success).toBe(false);
  });

  it("enforces an enum", () => {
    const v = validatorFor(
      entry({ kind: "enum", schema: { type: "string", enum: ["block", "warn"] } }),
    );
    expect(v.safeParse("warn").success).toBe(true);
    expect(v.safeParse("ignore").success).toBe(false);
  });

  it("enforces a duration pattern", () => {
    const v = validatorFor(
      entry({
        kind: "duration",
        schema: { type: "string", pattern: "^[0-9]+(ns|us|ms|s|m|h)([0-9]+(ns|us|ms|s|m|h))*$" },
      }),
    );
    expect(v.safeParse("5m").success).toBe(true);
    expect(v.safeParse("five minutes").success).toBe(false);
  });

  it("parses an object edited as JSON before validating it", () => {
    const v = validatorFor(entry({ kind: "object", schema: { type: "object" } }));
    expect(v.safeParse('{"a":1}')).toMatchObject({ success: true, data: { a: 1 } });
    expect(v.safeParse("{not json").success).toBe(false);
  });

  it("requires a replacement secret to be non-empty", () => {
    const v = validatorFor(entry({ kind: "secret", isSecret: true, schema: { type: "string" } }));
    expect(v.safeParse("").success).toBe(false);
    expect(v.safeParse("xoxb-1").success).toBe(true);
  });
});

describe("helpers", () => {
  it("round-trips a dotted key through a form field name", () => {
    expect(keyOf(fieldName("notifications.slack_token"))).toBe("notifications.slack_token");
    expect(fieldName("a.b")).not.toContain(".");
  });

  it("says where an effective value came from", () => {
    expect(sourceLabel(undefined, "global")).toBe("Default");
    expect(sourceLabel({ key: "k", source: "global", fromDefault: false }, "global")).toBe(
      "Set here",
    );
    expect(sourceLabel({ key: "k", source: "global", fromDefault: false }, "project")).toBe(
      "Inherited from global",
    );
  });
});
