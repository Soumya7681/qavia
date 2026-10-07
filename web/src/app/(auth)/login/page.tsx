import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Suspense } from "react";

import { AuthCard } from "@/components/auth/auth-card";
import { LoginForm } from "@/components/auth/login-form";
import { getSetupStatus } from "@/lib/auth/server";

export const metadata: Metadata = { title: "Sign in" };

// A fresh install has no one to sign in as: send the first visitor to setup.
async function SetupGate() {
  const status = await getSetupStatus();
  if (!status.adminExists) redirect("/setup");
  return null;
}

export default function LoginPage() {
  return (
    <AuthCard title="Sign in" description="Your AI QA engineer is waiting.">
      {/* The form reads ?next= from the URL, which is request-time. */}
      <Suspense>
        <SetupGate />
        <LoginForm />
      </Suspense>
    </AuthCard>
  );
}
