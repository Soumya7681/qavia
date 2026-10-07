import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";

type EmptyStateProps = {
  icon?: LucideIcon;
  title: string;
  /** What to do next. An empty state that only says "no data" is unfinished. */
  description: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
};

// The empty quarter of the four states every screen has (work-frontend.md rule 5).
export function EmptyState({ icon: Icon, title, description, action, className }: EmptyStateProps) {
  return (
    <section
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg border border-dashed p-6 sm:p-8",
        className,
      )}
    >
      {Icon ? <Icon aria-hidden className="size-5 text-muted-foreground" /> : null}
      <div className="flex max-w-prose flex-col gap-1">
        <h2 className="text-sm font-semibold">{title}</h2>
        <div className="text-sm text-muted-foreground">{description}</div>
      </div>
      {action ? <div className="pt-1">{action}</div> : null}
    </section>
  );
}
