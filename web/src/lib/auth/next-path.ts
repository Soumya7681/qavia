/** Where a sign-in should return to, if the value is safe to follow. */
export function safeNextPath(raw: string | null | undefined): string | undefined {
  if (!raw) {
    return undefined;
  }
  // Only a path on this site. "//evil.test" and "/\evil.test" are protocol-relative
  // in browsers, and anything with a scheme is somewhere else entirely: following
  // either turns the login page into an open redirect.
  if (!raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) {
    return undefined;
  }
  if (raw.startsWith("/login")) {
    return undefined;
  }
  return raw;
}

export function loginPath(next?: string | null): string {
  const safe = safeNextPath(next);
  return safe ? `/login?next=${encodeURIComponent(safe)}` : "/login";
}
