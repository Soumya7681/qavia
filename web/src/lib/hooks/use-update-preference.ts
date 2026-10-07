"use client";

import { useRouter } from "next/navigation";

import { api } from "@/lib/api/client";

/** Writes one of the signed-in user's own preferences, then re-reads the page's server data. */
export function useUpdatePreference() {
  const router = useRouter();
  const mutation = api.useMutation("put", "/api/v1/settings", {
    onSuccess: () => router.refresh(),
  });
  return {
    update: (key: string, value: unknown) =>
      mutation.mutateAsync({ body: { changes: [{ key, scope: "user", value }] } }),
    isPending: mutation.isPending,
  };
}
