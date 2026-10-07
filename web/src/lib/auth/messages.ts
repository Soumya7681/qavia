import { type ApiError, ClientErrorCode } from "@/lib/api/errors";

export type AuthMessage = {
  title: string;
  description?: string;
  /** Which form field the message belongs beside, when it is about one field. */
  field?: "password" | "currentPassword" | "newPassword" | "token";
  /** For a lockout or rate limit: when trying again can succeed. */
  retryAt?: Date;
};

function retryAt(error: ApiError, now: Date): Date | undefined {
  const seconds = Number(error.details.retryAfterSeconds);
  return Number.isFinite(seconds) && seconds > 0
    ? new Date(now.getTime() + seconds * 1000)
    : undefined;
}

/** "about 15 minutes", "under a minute": a lockout is read once, not timed to the second. */
export function waitPhrase(until: Date, now: Date = new Date()): string {
  const minutes = Math.ceil((until.getTime() - now.getTime()) / 60_000);
  if (minutes <= 1) return "under a minute";
  if (minutes < 60) return `about ${minutes} minutes`;
  const hours = Math.round(minutes / 60);
  return hours === 1 ? "about an hour" : `about ${hours} hours`;
}

/**
 * One distinct, useful message per auth error code (FE-0.2), chosen by code and
 * never by message text.
 *
 * Wrong password and unknown account are one code from the API and one message
 * here, so the page cannot be used to learn whether an address has an account.
 */
export function authMessage(
  error: ApiError,
  { receivedAt = new Date(), now = receivedAt }: { receivedAt?: Date; now?: Date } = {},
): AuthMessage {
  switch (error.code) {
    case "invalid_credentials":
      return {
        title: "That email and password do not match",
        description: "Check both and try again. Passwords are case sensitive.",
      };
    case "account_locked": {
      const at = retryAt(error, receivedAt);
      return {
        title: "This account is locked for now",
        description: at
          ? `Too many attempts failed. Try again in ${waitPhrase(at, now)}, or ask an admin to unlock it.`
          : "Too many attempts failed. Try again later, or ask an admin to unlock it.",
        retryAt: at,
      };
    }
    case "rate_limited": {
      const at = retryAt(error, receivedAt);
      return {
        title: "Too many attempts from here",
        description: at
          ? `Wait ${waitPhrase(at, now)} before trying again.`
          : "Wait a moment before trying again.",
        retryAt: at,
      };
    }
    case "account_disabled":
      return {
        title: "This account has been disabled",
        description: "Ask an admin to re-enable it if you still need access.",
      };
    case "invite_invalid":
      return {
        title: "This invitation cannot be used",
        description:
          "It may have expired or already been accepted. Ask an admin to send a new one.",
        field: "token",
      };
    case "password_too_weak":
      // The API's own sentence names the rule, which lives in one place: the server.
      return { title: error.message, field: "password" };
    case "unauthenticated":
      return { title: "Your session has ended", description: "Sign in again to continue." };
    case ClientErrorCode.Network:
      return {
        title: "Qavia could not be reached",
        description: "Check your connection and try again.",
      };
    default:
      return {
        title: "Something went wrong",
        description: error.incidentId
          ? `${error.message} Incident ID ${error.incidentId}.`
          : error.message,
      };
  }
}
