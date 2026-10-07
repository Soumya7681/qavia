import { type NextRequest, NextResponse } from "next/server";

import { loginPath } from "@/lib/auth/next-path";
import { PATH_HEADER, SESSION_COOKIE } from "@/lib/auth/constants";

/**
 * An optimistic check only: no session cookie at all means there is nothing to
 * ask the API about, so go straight to login and remember where the user was
 * going. A cookie that is present is not trusted here; the (app) layout asks the
 * API who it belongs to on every request.
 */
export function proxy(request: NextRequest) {
  const path = request.nextUrl.pathname + request.nextUrl.search;

  if (!request.cookies.has(SESSION_COOKIE)) {
    return NextResponse.redirect(new URL(loginPath(path), request.url));
  }

  const forwarded = new Headers(request.headers);
  forwarded.set(PATH_HEADER, path);
  return NextResponse.next({ request: { headers: forwarded } });
}

export const config = {
  // Everything except the API rewrite, Next's own assets, and the pages that exist
  // before a session does: login, invite acceptance, first-run setup, and the
  // development-only design page.
  matcher: ["/((?!api/|_next/|favicon\\.ico|login|accept-invite|setup|design).*)"],
};
