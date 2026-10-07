"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

const tabs = [
  { segment: "", label: "Overview" },
  { segment: "requirements", label: "Requirements" },
  { segment: "test-cases", label: "Test cases" },
  { segment: "files", label: "Files" },
  { segment: "runs", label: "Runs" },
  { segment: "defects", label: "Defects" },
  { segment: "settings", label: "Settings" },
];

/** The project's sections, as links: each is its own URL, so it can be shared and reloaded. */
export function ProjectTabs({ projectID }: { projectID: string }) {
  const pathname = usePathname();
  const base = `/projects/${projectID}`;
  const active = (segment: string) =>
    segment === ""
      ? pathname === base
      : pathname === `${base}/${segment}` || pathname.startsWith(`${base}/${segment}/`);

  return (
    <nav aria-label="Project sections" className="-mb-px flex gap-1 overflow-x-auto">
      {tabs.map(({ segment, label }) => {
        const href = segment ? `${base}/${segment}` : base;
        const current = active(segment);
        return (
          <Link
            key={segment || "overview"}
            href={href}
            aria-current={current ? "page" : undefined}
            className={cn(
              "border-b-2 px-3 py-2 text-sm whitespace-nowrap transition-colors",
              current
                ? "border-primary font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {label}
          </Link>
        );
      })}
    </nav>
  );
}
