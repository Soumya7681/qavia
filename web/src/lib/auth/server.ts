import "server-only";

import { cookies, headers } from "next/headers";
import { connection } from "next/server";
import { notFound, redirect } from "next/navigation";
import { cache } from "react";

import { CORRELATION_HEADER, createApiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";
import { apiBaseUrl } from "@/lib/config";

import { PATH_HEADER, SESSION_COOKIE } from "./constants";
import { loginPath } from "./next-path";
import type { CurrentUser } from "./roles";

/**
 * A client for server components, acting as the signed-in user.
 *
 * Only the session cookie is forwarded, not every cookie the browser sent: the API
 * needs exactly one, and forwarding the rest hands it values it has no business
 * reading. The request's own correlation ID travels with it, so one page render is
 * one thread through the API's logs.
 */
export const serverApi = cache(async () => {
  // Every API call is request-time work (it acts as this user, and the client
  // draws a random request ID), so say so before anything else happens.
  await connection();
  const [jar, incoming] = await Promise.all([cookies(), headers()]);
  const session = jar.get(SESSION_COOKIE)?.value;

  const forwarded: Record<string, string> = {};
  if (session) {
    forwarded.cookie = `${SESSION_COOKIE}=${session}`;
  }
  const correlation = incoming.get(CORRELATION_HEADER);
  if (correlation) {
    forwarded[CORRELATION_HEADER] = correlation;
  }

  return createApiClient({ baseUrl: apiBaseUrl, headers: forwarded });
});

async function currentPath(): Promise<string | null> {
  return (await headers()).get(PATH_HEADER);
}

/**
 * The signed-in user, or a redirect to login.
 *
 * Asked of the API on every request and cached only for that request, so a role
 * change or a revoked session takes effect on the very next navigation.
 */
export const getCurrentUser = cache(async (): Promise<CurrentUser> => {
  const client = await serverApi();
  try {
    const { data } = await client.GET("/api/v1/me");
    return data!;
  } catch (error) {
    if (isApiError(error) && error.status === 401) {
      redirect(loginPath(await currentPath()));
    }
    throw error;
  }
});

export type ProjectAccess =
  { project: Project; denied?: never } | { project?: never; denied: string };

type Project = components["schemas"]["Project"];

/**
 * A project the current user may see, or the reason they may not.
 *
 * Not found and not a member are kept apart because the API keeps them apart: a
 * missing project is the 404 page, a project you are not in is a no-access state
 * that says who can add you.
 */
export const getProject = cache(async (projectID: string): Promise<ProjectAccess> => {
  const client = await serverApi();
  try {
    const { data } = await client.GET("/api/v1/projects/{projectID}", {
      params: { path: { projectID } },
    });
    return { project: data! };
  } catch (error) {
    if (isApiError(error)) {
      if (error.status === 404) {
        notFound();
      }
      if (error.status === 403) {
        return { denied: error.code };
      }
      if (error.status === 401) {
        redirect(loginPath(await currentPath()));
      }
    }
    throw error;
  }
});

export type Preferences = {
  timezone?: string;
  theme: "system" | "light" | "dark";
  landingPage: "projects" | "runs" | "defects" | "notifications";
};

/**
 * The signed-in user's resolved preferences, read from the settings registry
 * (user, then global, then default), once per request.
 */
export const getPreferences = cache(async (): Promise<Preferences> => {
  const client = await serverApi();
  const { data } = await client.GET("/api/v1/settings");
  const value = (key: string) => data?.values.find((v) => v.key === key)?.value;

  const theme = value("preferences.theme");
  const landing = value("preferences.landing_page");
  const timezone = value("preferences.timezone");
  return {
    timezone: typeof timezone === "string" && timezone ? timezone : undefined,
    theme: theme === "light" || theme === "dark" ? theme : "system",
    landingPage:
      landing === "runs" || landing === "defects" || landing === "notifications"
        ? landing
        : "projects",
  };
});

/**
 * A client for public endpoints, with no session. Setup status is read before
 * any account exists, so it is fetched this way.
 */
export async function publicServerApi() {
  await connection();
  return createApiClient({ baseUrl: apiBaseUrl });
}

/** First-run state; nothing about it is secret. */
export const getSetupStatus = cache(async () => {
  const client = await publicServerApi();
  const { data } = await client.GET("/api/v1/setup/status");
  return data!;
});
