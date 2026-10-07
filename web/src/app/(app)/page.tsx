import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { LAST_PROJECT_COOKIE } from "@/lib/auth/constants";
import { getPreferences } from "@/lib/auth/server";

const UUID = /^[0-9a-f-]{36}$/i;

// "/" goes wherever the user's `preferences.landing_page` says. Runs and defects
// are per project in the API, so those land in the project last opened in this
// browser, or the project list when there is none.
export default async function Landing() {
  const [{ landingPage }, jar] = await Promise.all([getPreferences(), cookies()]);
  const lastProject = jar.get(LAST_PROJECT_COOKIE)?.value;
  const project = lastProject && UUID.test(lastProject) ? lastProject : undefined;

  switch (landingPage) {
    case "notifications":
      redirect("/notifications");
    case "runs":
      redirect(project ? `/projects/${project}/runs` : "/projects");
    case "defects":
      redirect(project ? `/projects/${project}/defects` : "/projects");
    default:
      redirect("/projects");
  }
}
