"use client";

import { ArchiveIcon, FolderPlusIcon, SearchIcon } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";

import { useCurrentUser } from "@/components/auth/current-user";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { hasRole } from "@/lib/auth/roles";
import { useCursorList } from "@/lib/hooks/use-cursor-list";
import { useFormat } from "@/lib/hooks/use-format";

import { CreateProjectDialog } from "./create-project-dialog";
import { testTypes } from "./test-types";

export function ProjectList() {
  const user = useCurrentUser();
  const format = useFormat();
  const router = useRouter();
  const pathname = usePathname();
  const search = useSearchParams();
  const [query, setQuery] = useState("");
  const [includeArchived, setIncludeArchived] = useState(false);
  const canCreate = hasRole(user, "qa_engineer");
  const creating = search.get("new") === "1" && canCreate;

  const projects = useCursorList("/api/v1/projects", { query: { limit: 50, includeArchived } });

  // The API has no name search, so this filters what has loaded and says so.
  const needle = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      needle
        ? projects.items.filter((p) => `${p.name} ${p.description}`.toLowerCase().includes(needle))
        : projects.items,
    [projects.items, needle],
  );

  const setCreating = (open: boolean) =>
    router.replace(open ? `${pathname}?new=1` : pathname, { scroll: false });

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Projects</h1>
          <p className="text-sm text-muted-foreground">
            {hasRole(user, "qa_lead")
              ? "Every project on the platform."
              : "The projects you own or are a member of."}
          </p>
        </div>
        {canCreate ? (
          <Button onClick={() => setCreating(true)}>
            <FolderPlusIcon data-icon="inline-start" aria-hidden />
            New project
          </Button>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center gap-4">
        <div className="relative w-full max-w-xs">
          <SearchIcon
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            aria-label="Filter projects"
            placeholder="Filter by name"
            className="pl-8"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div className="flex items-center gap-2">
          <Switch
            id="include-archived"
            checked={includeArchived}
            onCheckedChange={setIncludeArchived}
          />
          <Label htmlFor="include-archived">Show archived</Label>
        </div>
      </div>

      {projects.isLoading ? (
        <div className="flex flex-col gap-2" aria-busy="true" aria-label="Loading projects">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : projects.error ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.items.length === 0 ? (
        <EmptyState
          icon={FolderPlusIcon}
          title="No projects yet"
          description={
            canCreate
              ? "Create a project, upload an OpenAPI spec or a requirements document, and Qavia generates the tests."
              : "You are not on any project yet. Ask a QA Lead to add you to one."
          }
          action={
            canCreate ? <Button onClick={() => setCreating(true)}>New project</Button> : undefined
          }
        />
      ) : shown.length === 0 ? (
        <EmptyState
          icon={SearchIcon}
          title={`Nothing matches "${query}"`}
          description={
            projects.hasMore
              ? "Only the loaded projects were searched. Load more to search further."
              : "Try a shorter part of the name."
          }
        />
      ) : (
        <div className="rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Project</TableHead>
                <TableHead className="hidden md:table-cell">Produces</TableHead>
                <TableHead className="text-right">Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((project) => (
                <TableRow key={project.id} className="relative">
                  <TableCell className="max-w-0 w-1/2">
                    <Link
                      href={`/projects/${project.id}`}
                      className="font-medium after:absolute after:inset-0 hover:underline"
                    >
                      {project.name}
                    </Link>
                    {project.archived ? (
                      <Badge variant="outline" className="ml-2">
                        <ArchiveIcon aria-hidden /> Archived
                      </Badge>
                    ) : null}
                    {project.description ? (
                      <p className="truncate text-xs text-muted-foreground">
                        {project.description}
                      </p>
                    ) : null}
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    <span className="text-xs text-muted-foreground">
                      {project.testTypes.map((t) => testTypes[t.type]?.label ?? t.type).join(", ")}
                    </span>
                  </TableCell>
                  <TableCell className="text-right text-xs text-muted-foreground">
                    <time dateTime={project.createdAt}>{format.date(project.createdAt)}</time>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {projects.hasMore ? (
        <Button
          variant="outline"
          className="w-fit self-center"
          onClick={() => void projects.loadMore()}
          disabled={projects.isLoadingMore}
        >
          {projects.isLoadingMore ? "Loading…" : "Load more projects"}
        </Button>
      ) : null}

      {canCreate ? <CreateProjectDialog open={creating} onOpenChange={setCreating} /> : null}
    </div>
  );
}
