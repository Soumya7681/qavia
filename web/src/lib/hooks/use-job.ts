"use client";

import { type QueryKey, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { api } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";

import { useSSE } from "./use-sse";

type Job = components["schemas"]["Job"];
type JobEvent = components["schemas"]["JobEvent"];

const TERMINAL: ReadonlySet<Job["status"]> = new Set(["succeeded", "failed", "cancelled"]);

export function isTerminal(status: Job["status"] | undefined): boolean {
  return status !== undefined && TERMINAL.has(status);
}

/**
 * A job's state and its live event log.
 *
 * The stream replays history to a late joiner and then follows live. A `status`
 * frame is taken as "something changed" and the job is re-read from
 * `GET /jobs/{id}`: the frame's own shape is not in the contract, and a typed
 * re-read is never wrong. When the job finishes, the queries it affects are
 * invalidated so every screen showing them catches up (FE-S.5).
 */
export function useJob(jobID: string | undefined, { invalidate = [] as QueryKey[] } = {}) {
  const queryClient = useQueryClient();
  const job = api.useQuery(
    "get",
    "/api/v1/jobs/{jobID}",
    { params: { path: { jobID: jobID ?? "" } } },
    { enabled: Boolean(jobID) },
  );

  const [events, setEvents] = useState<Map<string, JobEvent>>(() => new Map());

  const stream = useSSE(jobID ? `/api/v1/jobs/${jobID}/events` : null, {
    onMessage: ({ event, data }) => {
      if (event === "event") {
        const parsed = JSON.parse(data) as JobEvent;
        // Keyed by id, so a resumed stream can never show a line twice.
        setEvents((current) => new Map(current).set(parsed.id, parsed));
      } else if (event === "status") {
        void job.refetch();
      }
    },
    onEnd: async () => {
      const latest = await job.refetch();
      return !isTerminal(latest.data?.status);
    },
  });

  const status = job.data?.status;
  const finished = isTerminal(status);
  const invalidateKey = JSON.stringify(invalidate);
  useEffect(() => {
    if (finished) {
      for (const key of JSON.parse(invalidateKey) as QueryKey[]) {
        void queryClient.invalidateQueries({ queryKey: key });
      }
    }
  }, [finished, invalidateKey, queryClient]);

  const log = useMemo(
    () => [...events.values()].sort((a, b) => Number(a.id) - Number(b.id)),
    [events],
  );

  return {
    job: job.data,
    isLoading: job.isLoading,
    error: job.error,
    events: log,
    finished,
    /** "reconnecting" is shown to the user: a live view must never go quiet unannounced. */
    connection: stream.state,
    streamError: stream.error,
  };
}
