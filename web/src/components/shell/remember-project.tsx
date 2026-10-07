"use client";

import { useEffect } from "react";

import { LAST_PROJECT_COOKIE } from "@/lib/auth/constants";

/** Records the project being viewed, for per-project landing pages. Not a secret. */
export function RememberProject({ projectID }: { projectID: string }) {
  useEffect(() => {
    document.cookie = `${LAST_PROJECT_COOKIE}=${projectID}; path=/; max-age=${60 * 60 * 24 * 90}; samesite=lax`;
  }, [projectID]);
  return null;
}
