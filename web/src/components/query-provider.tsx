"use client";

import { QueryClientProvider } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { makeQueryClient } from "@/lib/query/query-client";

// One QueryClient per browser session, created on first render rather than at
// module scope, so a server render never shares a cache between two users.
export function QueryProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [client] = useState(() => {
    const queryClient = makeQueryClient({
      onUnauthenticated: () => {
        // Every cached query belonged to the session that just ended, so none of it
        // may survive into the next one.
        queryClient.clear();
        const next = window.location.pathname + window.location.search;
        router.replace(`/login?next=${encodeURIComponent(next)}`);
      },
      onNoAccess: (code) => {
        router.replace(`/forbidden?reason=${encodeURIComponent(code)}`);
      },
    });
    return queryClient;
  });

  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
