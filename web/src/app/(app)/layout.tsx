import { Suspense } from "react";

import { CurrentUserProvider } from "@/components/auth/current-user";
import { Skeleton } from "@/components/ui/skeleton";
import { getCurrentUser } from "@/lib/auth/server";

// Every authenticated page is guarded here, once, rather than page by page
// (FE-S.4). The session is read at request time, so it sits behind a Suspense
// boundary and the rest of the shell can still be served immediately.
export default function AppLayout({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex-1 p-6">
      <Suspense fallback={<GuardFallback />}>
        <SignedIn>{children}</SignedIn>
      </Suspense>
    </main>
  );
}

async function SignedIn({ children }: { children: React.ReactNode }) {
  const user = await getCurrentUser();
  return <CurrentUserProvider user={user}>{children}</CurrentUserProvider>;
}

function GuardFallback() {
  return (
    <div className="flex flex-col gap-3" aria-busy="true" aria-label="Loading">
      <Skeleton className="h-6 w-48" />
      <Skeleton className="h-4 w-80" />
    </div>
  );
}
