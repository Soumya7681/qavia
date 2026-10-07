// Shared by the proxy, which cannot import server-only modules, and by server code.

/** Matches SessionCookieName in api/internal/auth. */
export const SESSION_COOKIE = "qavia_session";

/** Set by src/proxy.ts so a server component knows which page it is rendering. */
export const PATH_HEADER = "x-qavia-path";
