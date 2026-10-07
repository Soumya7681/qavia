"use client";

import {
  BanIcon,
  CheckCircle2Icon,
  CircleDashedIcon,
  CircleXIcon,
  LoaderIcon,
  WifiOffIcon,
} from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";
import { useFormat } from "@/lib/hooks/use-format";
import { useJob } from "@/lib/hooks/use-job";
import { useNow } from "@/lib/hooks/use-now";
import { cn } from "@/lib/utils";

import { chainLabels, resultLink, stageLabel } from "./job-labels";

type Job = components["schemas"]["Job"];
type Status = Job["status"];

const statusBadge: Record<
  Status,
  { label: string; variant: "info" | "success" | "destructive" | "outline" | "secondary" }
> = {
  queued: { label: "Queued", variant: "outline" },
  running: { label: "Running", variant: "info" },
  succeeded: { label: "Succeeded", variant: "success" },
  failed: { label: "Failed", variant: "destructive" },
  cancelled: { label: "Cancelled", variant: "secondary" },
};

function StageIcon({ status }: { status: Status }) {
  const props = { "aria-hidden": true, className: "size-4 shrink-0" } as const;
  switch (status) {
    case "succeeded":
      return <CheckCircle2Icon {...props} className={cn(props.className, "text-success")} />;
    case "failed":
      return <CircleXIcon {...props} className={cn(props.className, "text-destructive")} />;
    case "running":
      return <LoaderIcon {...props} className={cn(props.className, "animate-spin text-info")} />;
    case "cancelled":
      return <BanIcon {...props} className={cn(props.className, "text-muted-foreground")} />;
    default:
      return (
        <CircleDashedIcon {...props} className={cn(props.className, "text-muted-foreground")} />
      );
  }
}

function elapsed(job: Pick<Job, "startedAt" | "finishedAt">, now: Date): number | undefined {
  if (!job.startedAt) return undefined;
  const end = job.finishedAt ? new Date(job.finishedAt) : now;
  return end.getTime() - new Date(job.startedAt).getTime();
}

function Connection({ state }: { state: string }) {
  if (state === "open") {
    return (
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <span className="size-2 rounded-full bg-success" aria-hidden /> Live
      </span>
    );
  }
  if (state === "reconnecting" || state === "connecting") {
    return (
      <span role="status" className="flex items-center gap-1.5 text-xs text-warning">
        <WifiOffIcon aria-hidden className="size-3.5" />{" "}
        {state === "connecting" ? "Connecting…" : "Reconnecting…"}
      </span>
    );
  }
  return null;
}

/**
 * One job, live (FE-0.7): stages, progress, elapsed time, and the event log over
 * SSE, with history for a late joiner and a visible reconnecting state.
 */
export function JobView({
  jobID,
  projectID,
  retry,
}: {
  jobID: string;
  projectID: string;
  /** How to run the same work again, when the page knows what the input was. */
  retry?: { artifactId: string };
}) {
  const router = useRouter();
  const format = useFormat();
  const { job, events, finished, connection, isLoading, error, streamError } = useJob(jobID, {
    invalidate: [
      ["get", "/api/v1/projects/{projectID}/jobs"],
      ["get", "/api/v1/projects/{projectID}/requirements"],
    ],
  });
  const now = useNow(Boolean(job && !finished), 1000);
  const cancel = api.useMutation("post", "/api/v1/jobs/{jobID}/cancel");
  const submit = api.useMutation("post", "/api/v1/projects/{projectID}/jobs");
  const log = useRef<HTMLOListElement>(null);
  const [follow, setFollow] = useState(true);

  useEffect(() => {
    if (follow && log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [events.length, follow]);

  if (isLoading) return <Skeleton className="h-64 w-full" />;
  // Only a job never loaded is an error page. A failed re-read while the stream
  // reconnects keeps the last known state on screen, with the reconnecting badge.
  if (!job) return <ErrorState error={error ?? { message: "This job could not be found." }} />;

  const stages = job.stages?.length ? job.stages : [job];
  const badge = statusBadge[job.status];
  const result = job.status === "succeeded" ? resultLink(projectID, job.chain) : undefined;
  const took = elapsed(job, now);

  const doCancel = async () => {
    try {
      await cancel.mutateAsync({ params: { path: { jobID } } });
      toast.success("Cancelling");
    } catch (e) {
      if (!isApiError(e)) throw e;
      toast.error(e.message);
    }
  };

  const doRetry = async () => {
    if (!retry || !job.chain) return;
    try {
      const ref = await submit.mutateAsync({
        params: { path: { projectID } },
        body: { chain: job.chain, artifactId: retry.artifactId },
        headers: { "Idempotency-Key": crypto.randomUUID() },
      });
      router.push(`/projects/${projectID}/jobs/${ref.jobId}?artifact=${retry.artifactId}`);
    } catch (e) {
      if (!isApiError(e)) throw e;
      toast.error(e.message);
    }
  };

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h2 className="flex items-center gap-2 text-lg font-semibold">
            {job.chain ? chainLabels[job.chain] : stageLabel(job.type)}
            <Badge variant={badge.variant}>{badge.label}</Badge>
          </h2>
          <p className="text-xs text-muted-foreground">
            Queued <time dateTime={job.queuedAt}>{format.dateTime(job.queuedAt)}</time>
            {took !== undefined ? <> · {format.duration(took)}</> : null}
            {job.attempts > 1 ? (
              <>
                {" "}
                · attempt {job.attempts} of {job.maxAttempts}
              </>
            ) : null}
          </p>
        </div>
        <div className="flex items-center gap-3">
          {!finished ? <Connection state={connection} /> : null}
          {!finished ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => void doCancel()}
              disabled={cancel.isPending}
            >
              Cancel job
            </Button>
          ) : null}
        </div>
      </div>

      {!finished ? (
        <p className="rounded-md border bg-card px-4 py-3 text-sm text-muted-foreground">
          This keeps running on the server. You can close this tab; you will be notified when it
          finishes.
        </p>
      ) : null}

      {job.status === "succeeded" && result ? (
        <div
          role="status"
          className="flex flex-wrap items-center gap-3 rounded-md bg-success-wash px-4 py-3 text-sm text-success"
        >
          <CheckCircle2Icon aria-hidden className="size-4" /> Finished.
          <Button asChild size="sm">
            <Link href={result.href}>{result.label}</Link>
          </Button>
        </div>
      ) : null}
      {job.status === "failed" ? (
        <ErrorState
          title="This job failed"
          error={{
            message:
              job.error ?? "It stopped without saying why. The event log below has the last lines.",
          }}
          onRetry={retry ? () => void doRetry() : undefined}
        />
      ) : null}
      {job.status === "cancelled" ? (
        <p role="status" className="rounded-md border px-4 py-3 text-sm text-muted-foreground">
          Cancelled. Stages that finished before the cancel keep their results; nothing after them
          ran.
        </p>
      ) : null}
      {streamError ? (
        <ErrorState
          title="The live log stopped"
          error={streamError}
          onRetry={() => router.refresh()}
        />
      ) : null}

      <section aria-labelledby="stages" className="rounded-lg border bg-card">
        <div className="flex items-center gap-3 border-b px-4 py-3">
          <h3 id="stages" className="text-sm font-semibold">
            Stages
          </h3>
          <Progress
            value={job.progress}
            className="max-w-xs flex-1"
            aria-label="Overall progress"
          />
          <span className="text-xs tabular-nums text-muted-foreground">{job.progress}%</span>
        </div>
        <ol className="divide-y">
          {stages.map((stage) => {
            const t = elapsed(stage, now);
            return (
              <li key={stage.id} className="flex items-center gap-3 px-4 py-2.5 text-sm">
                <StageIcon status={stage.status} />
                <span className="flex-1">{stageLabel(stage.type)}</span>
                {stage.status === "running" ? (
                  <span className="text-xs tabular-nums text-muted-foreground">
                    {stage.progress}%
                  </span>
                ) : null}
                <span className="w-16 text-right text-xs tabular-nums text-muted-foreground">
                  {t !== undefined ? format.duration(t) : statusBadge[stage.status].label}
                </span>
              </li>
            );
          })}
        </ol>
      </section>

      <section aria-labelledby="event-log" className="rounded-lg border bg-card">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <h3 id="event-log" className="text-sm font-semibold">
            Event log
          </h3>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
            Follow
          </label>
        </div>
        {events.length === 0 ? (
          <p className="px-4 py-6 text-sm text-muted-foreground">
            {finished ? "Nothing was logged." : "Waiting for the first line…"}
          </p>
        ) : (
          <ol
            ref={log}
            aria-live={finished ? "off" : "polite"}
            className="max-h-96 overflow-y-auto px-4 py-2 font-mono text-xs leading-relaxed"
          >
            {events.map((event) => (
              <li key={event.id} className="flex gap-3">
                <time dateTime={event.at} className="shrink-0 text-muted-foreground tabular-nums">
                  {format.time(event.at)}
                </time>
                <span
                  className={cn(
                    event.level === "error" && "text-destructive",
                    event.level === "warn" && "text-warning",
                    event.level === "debug" && "text-muted-foreground",
                  )}
                >
                  {event.message}
                </span>
              </li>
            ))}
          </ol>
        )}
      </section>

      {retry === undefined && job.status === "failed" ? (
        <p className="text-xs text-muted-foreground">
          To try again, start it from the input it was working on.{" "}
          <Link href={`/projects/${projectID}`} className="underline">
            Go to the project
          </Link>
        </p>
      ) : null}
    </div>
  );
}
