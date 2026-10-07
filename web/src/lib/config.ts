// Build-time configuration. The only value is the API's address, and it is public
// by design: the web app holds no secret, so nothing here may ever be one
// (work-frontend.md FE-S.1). Credentials live in the API's session cookie.

const DEFAULT_API_BASE_URL = "http://localhost:8080";

export function parseApiBaseUrl(raw: string | undefined): string {
  const value = (raw ?? "").trim() || DEFAULT_API_BASE_URL;

  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(
      `NEXT_PUBLIC_API_BASE_URL must be an absolute URL such as ${DEFAULT_API_BASE_URL}, got "${value}"`,
    );
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`NEXT_PUBLIC_API_BASE_URL must be http or https, got "${url.protocol}"`);
  }
  if (url.username || url.password) {
    throw new Error("NEXT_PUBLIC_API_BASE_URL must not carry credentials: it ships to the browser");
  }

  // No trailing slash, so `${apiBaseUrl}/api/v1` never doubles one.
  return url.origin + url.pathname.replace(/\/+$/, "");
}

export const apiBaseUrl = parseApiBaseUrl(process.env.NEXT_PUBLIC_API_BASE_URL);
