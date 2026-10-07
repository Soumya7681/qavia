// @vitest-environment happy-dom
import { waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";

import { eventStream, server, setupMockApi } from "@/test/msw";
import { renderHookWithProviders } from "@/test/render";

import { useJob } from "./use-job";

setupMockApi();

const jobID = "11111111-1111-1111-1111-111111111111";

function job(status: string, progress = 0) {
  return {
    id: jobID,
    type: "noop",
    status,
    progress,
    attempts: 1,
    maxAttempts: 3,
    queuedAt: "2026-10-07T08:00:00Z",
  };
}

describe("useJob", () => {
  it("replays history to a late joiner, follows live, and invalidates on finish", async () => {
    let current = job("running", 40);
    const stream = eventStream();
    let streamOpens = 0;
    server.use(
      http.get(`*/api/v1/jobs/${jobID}`, () => HttpResponse.json(current)),
      http.get(`*/api/v1/jobs/${jobID}/events`, () => {
        streamOpens += 1;
        return stream.response();
      }),
    );

    const affected = ["get", "/api/v1/projects/{projectID}/jobs"];
    const { result, client, unmount } = renderHookWithProviders(() =>
      useJob(jobID, { invalidate: [affected] }),
    );
    client.setQueryData(affected, { items: [] });

    await waitFor(() => expect(result.current.job?.status).toBe("running"));

    // History first, then a live line, out of order on purpose.
    stream.send(
      "event",
      { id: "2", level: "info", message: "Working", at: "2026-10-07T08:00:02Z" },
      "2",
    );
    stream.send(
      "event",
      { id: "1", level: "info", message: "Preparing", at: "2026-10-07T08:00:01Z" },
      "1",
    );
    await waitFor(() =>
      expect(result.current.events.map((e) => e.message)).toEqual(["Preparing", "Working"]),
    );
    expect(result.current.connection).toBe("open");

    // The job finishes: a status frame, then the server ends the stream.
    current = job("succeeded", 100);
    stream.send("status", { anything: true });
    stream.end();

    await waitFor(() => expect(result.current.finished).toBe(true));
    await waitFor(() => expect(client.getQueryState(affected)?.isInvalidated).toBe(true));
    expect(streamOpens).toBe(1);
    unmount();
  });
});
