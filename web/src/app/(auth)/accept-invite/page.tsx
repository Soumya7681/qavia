import type { Metadata } from "next";
import { Suspense } from "react";

import { AcceptInviteForm } from "@/components/auth/accept-invite-form";
import { AuthCard } from "@/components/auth/auth-card";

export const metadata: Metadata = { title: "Accept invitation" };

export default function AcceptInvitePage() {
  return (
    <AuthCard
      title="Join Qavia"
      description="Choose the name your teammates will see and a password. You will be signed in straight away."
    >
      <Suspense>
        <AcceptInviteForm />
      </Suspense>
    </AuthCard>
  );
}
