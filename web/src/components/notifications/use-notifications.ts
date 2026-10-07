"use client";

import { useQueryClient } from "@tanstack/react-query";

import { api } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";

type Notification = components["schemas"]["Notification"];

/** Read state changes, shared by the bell and the notifications page. */
export function useNotificationActions() {
  const queryClient = useQueryClient();
  const markOne = api.useMutation("post", "/api/v1/notifications/{notificationID}/read");
  const markAll = api.useMutation("post", "/api/v1/notifications/read-all");

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/notifications"] }),
      queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/notifications/unread-count"] }),
    ]);

  return {
    open: (n: Notification) => {
      if (!n.read) {
        void markOne.mutateAsync({ params: { path: { notificationID: n.id } } }).then(refresh);
      }
    },
    markAll: () => markAll.mutateAsync({}).then(refresh),
    markingAll: markAll.isPending,
  };
}

/**
 * The unread count. There is no push channel for notifications, so it is re-read
 * every 30 seconds and whenever the window regains focus.
 */
export function useUnreadCount() {
  return api.useQuery("get", "/api/v1/notifications/unread-count", undefined, {
    refetchInterval: 30_000,
    refetchOnWindowFocus: true,
    staleTime: 10_000,
  });
}
