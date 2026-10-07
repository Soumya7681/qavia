import type { Metadata } from "next";
import { Suspense } from "react";

import { AuthCard } from "@/components/auth/auth-card";
import { LoginForm } from "@/components/auth/login-form";

export const metadata: Metadata = { title: "Sign in" };

export default function LoginPage() {
  return (
    <AuthCard title="Sign in" description="Your AI QA engineer is waiting.">
      {/* The form reads ?next= from the URL, which is request-time. */}
      <Suspense>
        <LoginForm />
      </Suspense>
    </AuthCard>
  );
}
