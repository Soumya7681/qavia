import type { Metadata } from "next";

import { ChangePasswordForm } from "@/components/auth/change-password-form";
import { roleLabel } from "@/components/shell/user-menu";
import { getCurrentUser } from "@/lib/auth/server";

export const metadata: Metadata = { title: "Account" };

export default async function AccountPage() {
  const user = await getCurrentUser();
  return (
    <div className="flex max-w-3xl flex-col gap-10">
      <section className="flex flex-col gap-4">
        <h1 className="text-xl font-semibold">Account</h1>
        <dl className="grid grid-cols-[8rem_1fr] gap-y-2 text-sm">
          <dt className="text-muted-foreground">Name</dt>
          <dd>{user.name}</dd>
          <dt className="text-muted-foreground">Email</dt>
          <dd>{user.email}</dd>
          <dt className="text-muted-foreground">Role</dt>
          <dd>
            {roleLabel[user.role]}
            <span className="text-muted-foreground"> · changed by an admin</span>
          </dd>
        </dl>
      </section>
      <section className="flex flex-col gap-4">
        <div>
          <h2 className="text-base font-semibold">Change password</h2>
          <p className="text-sm text-muted-foreground">
            Other browsers signed in as you are signed out; this one stays signed in.
          </p>
        </div>
        <ChangePasswordForm />
      </section>
    </div>
  );
}
