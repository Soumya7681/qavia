import { describe, expect, it } from "vitest";

import { ApiError } from "@/lib/api/errors";

import { authMessage, waitPhrase } from "./messages";

const now = new Date("2026-10-07T10:00:00Z");
const err = (code: string, status: number, details: Record<string, unknown> = {}, message = code) =>
  new ApiError({ code, status, message, details });

describe("authMessage", () => {
  it("gives every catalogued auth code its own message", () => {
    const codes = [
      ["invalid_credentials", 401],
      ["account_locked", 423],
      ["rate_limited", 429],
      ["account_disabled", 403],
      ["invite_invalid", 400],
      ["password_too_weak", 400],
      ["unauthenticated", 401],
    ] as const;
    const titles = codes.map(([code, status]) => authMessage(err(code, status), { now }).title);
    expect(new Set(titles).size).toBe(codes.length);
  });

  it("says when a locked account can try again", () => {
    const message = authMessage(err("account_locked", 423, { retryAfterSeconds: 900 }), {
      receivedAt: now,
    });
    expect(message.description).toContain("about 15 minutes");
    expect(message.retryAt?.toISOString()).toBe("2026-10-07T10:15:00.000Z");
  });

  it("counts the wait down from when the refusal arrived", () => {
    const later = new Date(now.getTime() + 10 * 60_000);
    const message = authMessage(err("account_locked", 423, { retryAfterSeconds: 900 }), {
      receivedAt: now,
      now: later,
    });
    expect(message.description).toContain("about 5 minutes");
  });

  it("uses the server's sentence for a weak password, beside the password field", () => {
    const message = authMessage(
      err(
        "password_too_weak",
        400,
        {},
        "Use at least 12 characters. A short passphrase of several words is fine.",
      ),
    );
    expect(message).toEqual({
      title: "Use at least 12 characters. A short passphrase of several words is fine.",
      field: "password",
    });
  });

  it("never mentions whether an account exists", () => {
    const { title, description } = authMessage(err("invalid_credentials", 401));
    expect(`${title} ${description}`).not.toMatch(/no account|not found|unknown|exist/i);
  });
});

describe("waitPhrase", () => {
  it("rounds to what a person reads", () => {
    expect(waitPhrase(new Date(now.getTime() + 20_000), now)).toBe("under a minute");
    expect(waitPhrase(new Date(now.getTime() + 14 * 60_000 + 1), now)).toBe("about 15 minutes");
    expect(waitPhrase(new Date(now.getTime() + 2 * 3_600_000), now)).toBe("about 2 hours");
  });
});
