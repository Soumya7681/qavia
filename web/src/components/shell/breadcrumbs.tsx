"use client";

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
import { api } from "@/lib/api/client";

import { segmentLabels } from "./nav";

/** Path segments with no page of their own: shown, but not links to a 404. */
const UNLINKED = new Set(["jobs", "admin"]);

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function titleCase(segment: string) {
  return segment.replace(/-/g, " ").replace(/^\w/, (c) => c.toUpperCase());
}

/**
 * Derived from the URL, not maintained per page: a new route gets breadcrumbs by
 * existing. A project ID shows the project's name when it is already cached; any
 * other ID shows its first eight characters, which is how people quote them.
 */
function useProjectName(projectID: string | undefined) {
  const query = api.useQuery(
    "get",
    "/api/v1/projects/{projectID}",
    { params: { path: { projectID: projectID ?? "" } } },
    // A 403 or 404 here is the page's to show; the breadcrumb just falls back.
    { enabled: Boolean(projectID), retry: false, meta: { quiet: true } },
  );
  return query.data?.name;
}

/**
 * Derived from the URL, not maintained per page: a new route gets breadcrumbs by
 * existing. A project ID shows the project's name; any other ID shows its first
 * eight characters, which is how people quote them.
 */
export function Breadcrumbs() {
  const pathname = usePathname();
  const segments = pathname.split("/").filter(Boolean);
  const projectID =
    segments[0] === "projects" && UUID.test(segments[1] ?? "") ? segments[1] : undefined;
  const projectName = useProjectName(projectID);

  const crumbs = segments.map((segment, index) => {
    const href = `/${segments.slice(0, index + 1).join("/")}`;
    let label = segmentLabels[segment] ?? titleCase(segment);
    if (UUID.test(segment)) {
      label = (segment === projectID && projectName) || segment.slice(0, 8);
    }
    return { href, label, linked: !UNLINKED.has(segment) };
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
                ) : !crumb.linked ? (
                  <span>{crumb.label}</span>
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
