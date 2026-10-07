import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, renderHook } from "@testing-library/react";

import { CurrentUserProvider } from "@/components/auth/current-user";
import type { CurrentUser } from "@/lib/auth/roles";

export const testUser: CurrentUser = {
  id: "00000000-0000-0000-0000-000000000001",
  email: "qa@hyscaler.test",
  name: "QA Engineer",
  role: "qa_engineer",
  timezone: "Asia/Kolkata",
  disabled: false,
  createdAt: "2026-01-01T00:00:00Z",
};

/** A fresh QueryClient per test with retries off, so a failure shows at once. */
export function testQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } },
  });
}

function Providers({
  client,
  user,
  children,
}: {
  client: QueryClient;
  user: CurrentUser | null;
  children: React.ReactNode;
}) {
  const inner = user ? <CurrentUserProvider user={user}>{children}</CurrentUserProvider> : children;
  return <QueryClientProvider client={client}>{inner}</QueryClientProvider>;
}

export function renderWithProviders(
  ui: React.ReactElement,
  { client = testQueryClient(), user = testUser as CurrentUser | null } = {},
) {
  return {
    client,
    ...render(ui, {
      wrapper: ({ children }) => (
        <Providers client={client} user={user}>
          {children}
        </Providers>
      ),
    }),
  };
}

export function renderHookWithProviders<T>(
  hook: () => T,
  { client = testQueryClient(), user = testUser as CurrentUser | null } = {},
) {
  return {
    client,
    ...renderHook(hook, {
      wrapper: ({ children }) => (
        <Providers client={client} user={user}>
          {children}
        </Providers>
      ),
    }),
  };
}
