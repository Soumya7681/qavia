"use client";

import { useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Fragment } from "react";

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import type { components } from "@/lib/api/schema";

import { segmentLabels } from "./nav";

type Project = components["schemas"]["Project"];

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function titleCase(segment: string) {
  return segment.replace(/-/g, " ").replace(/^\w/, (c) => c.toUpperCase());
}

/**
 * Derived from the URL, not maintained per page: a new route gets breadcrumbs by
 * existing. A project ID shows the project's name when it is already cached; any
 * other ID shows its first eight characters, which is how people quote them.
 */
export function Breadcrumbs() {
  const pathname = usePathname();
  const queryClient = useQueryClient();
  const segments = pathname.split("/").filter(Boolean);

  const projectName = (id: string) => {
    for (const [, data] of queryClient.getQueriesData<{ pages?: { items: Project[] }[] } | Project>(
      {
        queryKey: ["get"],
      },
    )) {
      if (!data) continue;
      if ("id" in data && data.id === id) return data.name;
      const found =
        "pages" in data ? data.pages?.flatMap((p) => p.items).find((p) => p.id === id) : undefined;
      if (found) return found.name;
    }
    return undefined;
  };

  const crumbs = segments.map((segment, index) => {
    const href = `/${segments.slice(0, index + 1).join("/")}`;
    let label = segmentLabels[segment] ?? titleCase(segment);
    if (UUID.test(segment)) {
      label = (segments[index - 1] === "projects" && projectName(segment)) || segment.slice(0, 8);
    }
    return { href, label };
  });

  if (crumbs.length === 0) return null;

  return (
    <Breadcrumb>
      <BreadcrumbList>
        {crumbs.map((crumb, index) => {
          const last = index === crumbs.length - 1;
          return (
            <Fragment key={crumb.href}>
              {index > 0 ? <BreadcrumbSeparator /> : null}
              <BreadcrumbItem>
                {last ? (
                  <BreadcrumbPage>{crumb.label}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink asChild>
                    <Link href={crumb.href}>{crumb.label}</Link>
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
            </Fragment>
          );
        })}
      </BreadcrumbList>
    </Breadcrumb>
  );
}
