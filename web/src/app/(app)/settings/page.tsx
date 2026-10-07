import type { Metadata } from "next";

import { SettingsTabs } from "@/components/settings/settings-tabs";
import { getCurrentUser } from "@/lib/auth/server";

export const metadata: Metadata = { title: "Settings" };

export default async function SettingsPage() {
  const user = await getCurrentUser();
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-xl font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          {user.role === "admin"
            ? "Your own preferences, and the platform defaults every project inherits."
            : "Your own preferences. Platform settings are managed by an admin."}
        </p>
      </div>
      <SettingsTabs isAdmin={user.role === "admin"} />
    </div>
  );
}
