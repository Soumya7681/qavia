import { cookies } from "next/headers";
import { Suspense } from "react";

import { CurrentUserProvider } from "@/components/auth/current-user";
import { PreferencesProvider } from "@/components/preferences";
import { AppShell } from "@/components/shell/app-shell";
import { Skeleton } from "@/components/ui/skeleton";
import { getCurrentUser, getPreferences } from "@/lib/auth/server";

// Every authenticated page is guarded here, once, rather than page by page
// (FE-S.4). The session is read at request time, so it sits behind a Suspense
// boundary; the fallback is a quiet outline of the shell, not a spinner.
export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <Suspense fallback={<ShellFallback />}>
      <SignedIn>{children}</SignedIn>
    </Suspense>
  );
}

async function SignedIn({ children }: { children: React.ReactNode }) {
  const [user, preferences, jar] = await Promise.all([
    getCurrentUser(),
    getPreferences(),
    cookies(),
  ]);
  return (
    <CurrentUserProvider user={user}>
      <PreferencesProvider preferences={preferences}>
        <AppShell sidebarOpen={jar.get("sidebar_state")?.value !== "false"}>{children}</AppShell>
      </PreferencesProvider>
    </CurrentUserProvider>
  );
}

function ShellFallback() {
  return (
    <div className="flex min-h-svh" aria-busy="true" aria-label="Loading">
      <div className="hidden w-64 shrink-0 flex-col gap-3 border-r bg-sidebar p-3 md:flex">
        <Skeleton className="h-8 w-24" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="mt-4 h-4 w-32" />
        <Skeleton className="h-4 w-28" />
      </div>
      <div className="flex-1">
        <div className="h-12 border-b" />
      </div>
    </div>
  );
}
