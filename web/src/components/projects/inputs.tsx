"use client";

import { FileTextIcon, PlayIcon } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";

import { chainLabels } from "@/components/jobs/job-labels";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { useCursorList } from "@/lib/hooks/use-cursor-list";
import { useFormat } from "@/lib/hooks/use-format";

import { artifactKinds } from "./artifact-kinds";
import { useCanMutate, useProject } from "./project-context";
import { UploadPanel } from "./upload-panel";

/** The project's uploaded inputs, a way to add one, and a way to process one (FE-0.6). */
export function Inputs() {
  const project = useProject();
  const router = useRouter();
  const format = useFormat();
  const permission = useCanMutate("qa_engineer");
  const artifacts = useCursorList("/api/v1/projects/{projectID}/artifacts", {
    path: { projectID: project.id },
    query: { limit: 25 },
  });
  const jobs = api.useQuery("get", "/api/v1/projects/{projectID}/jobs", {
    params: { path: { projectID: project.id }, query: { limit: 5 } },
  });
  const submit = api.useMutation("post", "/api/v1/projects/{projectID}/jobs");
  const [processing, setProcessing] = useState<string | null>(null);

  const process = async (artifactId: string) => {
    setProcessing(artifactId);
    try {
      const ref = await submit.mutateAsync({
        params: { path: { projectID: project.id } },
        body: { chain: "ingest", artifactId },
        headers: { "Idempotency-Key": crypto.randomUUID() },
      });
      router.push(`/projects/${project.id}/jobs/${ref.jobId}?artifact=${artifactId}`);
    } catch (error) {
      if (!isApiError(error)) throw error;
      // Refusals at enqueue name what to configure (no provider, external AI not
      // approved, spend ceiling), so the API's sentence is the useful one.
      toast.error("Processing could not start", { description: error.message });
      setProcessing(null);
    }
  };

  const previous = (lineageId: string, version: number) =>
    artifacts.items.find((a) => a.lineageId === lineageId && a.version === version - 1);

  return (
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]">
      <section id="inputs" aria-labelledby="inputs-title" className="flex flex-col gap-4">
        <div>
          <h2 id="inputs-title" className="text-sm font-semibold">
            Inputs
          </h2>
          <p className="text-xs text-muted-foreground">
            What Qavia works from. Process a specification to extract requirements and generate
            tests.
          </p>
        </div>
        <UploadPanel onUploaded={() => void artifacts.refetch()} />
        {artifacts.isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : artifacts.error ? (
          <ErrorState error={artifacts.error} onRetry={() => void artifacts.refetch()} />
        ) : artifacts.items.length === 0 ? (
          <EmptyState
            icon={FileTextIcon}
            title="Nothing uploaded yet"
            description="Upload an OpenAPI spec, a Postman collection, or a requirements document to begin."
          />
        ) : (
          <div className="rounded-lg border bg-card">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>File</TableHead>
                  <TableHead className="hidden md:table-cell">Kind</TableHead>
                  <TableHead className="hidden lg:table-cell">Uploaded</TableHead>
                  <TableHead className="text-right">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {artifacts.items.map((artifact) => {
                  const prior =
                    artifact.version > 1
                      ? previous(artifact.lineageId, artifact.version)
                      : undefined;
                  const kind = artifactKinds[artifact.kind];
                  return (
                    <TableRow key={artifact.id} id={`artifact-${artifact.id}`}>
                      <TableCell className="max-w-0 w-1/2">
                        <p className="truncate font-medium">{artifact.filename}</p>
                        <p className="text-xs text-muted-foreground">
                          <Badge variant="secondary" className="mr-1">
                            v{artifact.version}
                          </Badge>
                          {format.bytes(artifact.sizeBytes)}
                          {prior ? (
                            <>
                              {" · replaces "}
                              <a href={`#artifact-${prior.id}`} className="underline">
                                v{prior.version}
                              </a>
                            </>
                          ) : artifact.version > 1 ? (
                            " · earlier versions kept"
                          ) : null}
                        </p>
                      </TableCell>
                      <TableCell className="hidden text-xs md:table-cell">
                        {kind?.label ?? artifact.kind}
                      </TableCell>
                      <TableCell className="hidden text-xs text-muted-foreground lg:table-cell">
                        <time dateTime={artifact.createdAt}>
                          {format.relative(artifact.createdAt)}
                        </time>
                      </TableCell>
                      <TableCell className="text-right">
                        {kind?.processes ? (
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={!permission.allowed || processing !== null}
                            title={permission.allowed ? undefined : permission.reason}
                            onClick={() => void process(artifact.id)}
                          >
                            <PlayIcon data-icon="inline-start" aria-hidden />
                            {processing === artifact.id ? "Starting…" : "Process"}
                          </Button>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
        {artifacts.hasMore ? (
          <Button variant="outline" className="w-fit" onClick={() => void artifacts.loadMore()}>
            Load more
          </Button>
        ) : null}
      </section>

      <section aria-labelledby="recent-title" className="flex flex-col gap-3">
        <h2 id="recent-title" className="text-sm font-semibold">
          Recent work
        </h2>
        {jobs.data?.items.length ? (
          <ul className="divide-y rounded-lg border bg-card">
            {jobs.data.items.map((job) => (
              <li key={job.id}>
                <Link
                  href={`/projects/${project.id}/jobs/${job.id}`}
                  className="flex items-center justify-between gap-2 px-4 py-2.5 text-sm hover:bg-muted"
                >
                  <span>{job.chain ? chainLabels[job.chain] : job.type}</span>
                  <span className="text-xs text-muted-foreground">
                    {job.status} · {format.relative(job.queuedAt)}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">
            {jobs.isLoading ? "Loading…" : "Nothing has run yet."}
          </p>
        )}
      </section>
    </div>
  );
}
