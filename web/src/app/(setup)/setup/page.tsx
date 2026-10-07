import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Suspense } from "react";

import { SetupWizard } from "@/components/setup/setup-wizard";
import { getSetupStatus } from "@/lib/auth/server";

export const metadata: Metadata = { title: "Set up Qavia" };

// Reachable only while no account exists. The API's first-admin endpoint stops
// existing at the same moment, so this page cannot become a way back in.
async function Wizard() {
  const status = await getSetupStatus();
  if (status.adminExists) {
    redirect("/");
  }
  return <SetupWizard initialStatus={status} />;
}

export default function SetupPage() {
  return (
    <Suspense>
      <Wizard />
    </Suspense>
  );
}
