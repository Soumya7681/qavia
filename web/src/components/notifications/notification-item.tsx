"use client";

import { AlertTriangleIcon, BellIcon, CheckCircle2Icon, CircleXIcon, PlugZapIcon, ShieldAlertIcon } from "lucide-react";
import Link from "next/link";

import type { components } from "@/lib/api/schema";
import { useFormat } from "@/lib/hooks/use-format";
import { cn } from "@/lib/utils";

type Notification = components["schemas"]["Notification"];

const icon: Record<Notification["kind"], { Icon: typeof BellIcon; className: string }> = {
  job_completed: { Icon: CheckCircle2Icon, className: "text-success" },
  run_completed: { Icon: CheckCircle2Icon, className: "text-success" },
  job_failed: { Icon: CircleXIcon, className: "text-destructive" },
  job_needs_input: { Icon: AlertTriangleIcon, className: "text-warning" },
  drift_detected: { Icon: AlertTriangleIcon, className: "text-warning" },
  integration_failed: { Icon: PlugZapIcon, className: "text-destructive" },
  admin_alert: { Icon: ShieldAlertIcon, className: "text-warning" },
};

/**
 * One notification. The whole row is its deep link (FE-0.8); following it marks
 * it read.
 */
export function NotificationItem({
  notification,
  onOpen,
  compact = false,
}: {
  notification: Notification;
  onOpen: (n: Notification) => void;
  compact?: boolean;
}) {
  const format = useFormat();
  const { Icon, className } = icon[notification.kind] ?? { Icon: BellIcon, className: "" };
  return (
    <Link
      href={notification.link || "/notifications"}
      onClick={() => onOpen(notification)}
      className={cn(
        "flex gap-3 px-4 py-3 text-sm hover:bg-muted focus-visible:bg-muted focus-visible:outline-none",
        !notification.read && "bg-accent/40",
      )}
    >
      <Icon aria-hidden className={cn("mt-0.5 size-4 shrink-0", className)} />
      <span className="min-w-0 flex-1">
        <span className={cn("block", !notification.read && "font-medium")}>
          {notification.title}
          {!notification.read ? <span className="sr-only"> (unread)</span> : null}
        </span>
        <span className={cn("block text-muted-foreground", compact ? "line-clamp-2 text-xs" : "text-sm")}>
          {notification.body}
        </span>
        <time dateTime={notification.createdAt} className="text-xs text-muted-foreground">
          {format.relative(notification.createdAt)}
        </time>
      </span>
      {!notification.read ? <span aria-hidden className="mt-1.5 size-2 shrink-0 rounded-full bg-primary" /> : null}
    </Link>
  );
}
