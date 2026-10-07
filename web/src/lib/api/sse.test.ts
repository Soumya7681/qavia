import { afterEach, describe, expect, it, vi } from "vitest";

import { backoffDelay, openEventStream, type StreamMessage, type StreamState } from "./sse";

const encoder = new TextEncoder();

function streamResponse(frames: string[], { hold = false } = {}) {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const frame of frames) controller.enqueue(encoder.encode(frame));
      if (!hold) controller.close();
    },
  });
  return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

const waitFor = async (check: () => boolean, ms = 2000) => {
  const start = Date.now();
  while (!check()) {
    if (Date.now() - start > ms) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 5));
  }
};

afterEach(() => vi.useRealTimers());

describe("backoffDelay", () => {
  it("grows exponentially, is capped, and is jittered", () => {
    expect(backoffDelay(1, 500, 15_000, () => 1)).toBe(500);
    expect(backoffDelay(3, 500, 15_000, () => 1)).toBe(2000);
    expect(backoffDelay(20, 500, 15_000, () => 1)).toBe(15_000);
    expect(backoffDelay(3, 500, 15_000, () => 0)).toBe(1000);
  });
});

describe("openEventStream", () => {
  it("delivers named events in order", async () => {
    const messages: StreamMessage[] = [];
    const fetch = vi.fn(async () =>
      streamResponse([
        'event: status\ndata: {"a":1}\n\n',
        'id: 7\nevent: event\ndata: {"b":2}\n\n',
      ]),
    );
    const stream = openEventStream("http://api.test/s", {
      fetch,
      onMessage: (m) => void messages.push(m),
      onEnd: () => false,
    });
    await waitFor(() => messages.length === 2);
    expect(messages).toEqual([
      { event: "status", data: '{"a":1}', id: undefined },
      { event: "event", data: '{"b":2}', id: "7" },
    ]);
    stream.close();
  });

  it("resumes after a drop with Last-Event-ID, and says it is reconnecting", async () => {
    const states: StreamState[] = [];
    const seen: string[] = [];
    let call = 0;
    const fetch = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      call += 1;
      if (call === 1) return streamResponse(["id: 41\nevent: event\ndata: first\n\n"]);
      seen.push(new Headers(init?.headers).get("Last-Event-ID") ?? "");
      return streamResponse(["id: 42\nevent: event\ndata: second\n\n"], { hold: true });
    });
    const messages: string[] = [];
    const stream = openEventStream("http://api.test/s", {
      fetch,
      baseDelayMs: 1,
      onMessage: (m) => void messages.push(m.data),
      onState: (s) => void states.push(s),
      onEnd: () => true,
    });
    await waitFor(() => messages.length === 2);
    expect(seen).toEqual(["41"]);
    expect(states).toContain("reconnecting");
    expect(states.at(-1)).toBe("open");
    stream.close();
  });

  it("closes on a 4xx without retrying, carrying the API's error", async () => {
    let closedWith: unknown;
    const fetch = vi.fn(
      async () =>
        new Response(JSON.stringify({ code: "job_not_found", message: "Unknown job." }), {
          status: 404,
          headers: { "Content-Type": "application/json" },
        }),
    );
    openEventStream("http://api.test/s", {
      fetch,
      onMessage: () => {},
      onState: (s, e) => {
        if (s === "closed") closedWith = e;
      },
    });
    await waitFor(() => closedWith !== undefined);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(closedWith).toMatchObject({ code: "job_not_found", status: 404 });
  });

  it("retries a 5xx", async () => {
    let call = 0;
    const fetch = vi.fn(async () => {
      call += 1;
      return call === 1
        ? new Response("{}", { status: 503 })
        : streamResponse(["event: event\ndata: ok\n\n"], { hold: true });
    });
    const messages: string[] = [];
    const stream = openEventStream("http://api.test/s", {
      fetch,
      baseDelayMs: 1,
      onMessage: (m) => void messages.push(m.data),
    });
    await waitFor(() => messages.length === 1);
    expect(fetch).toHaveBeenCalledTimes(2);
    stream.close();
  });

  it("aborts the request when closed", async () => {
    let signal: AbortSignal | undefined;
    const fetch = vi.fn(async (_u: RequestInfo | URL, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      return streamResponse([], { hold: true });
    });
    const stream = openEventStream("http://api.test/s", { fetch, onMessage: () => {} });
    await waitFor(() => signal !== undefined);
    stream.close();
    expect(signal?.aborted).toBe(true);
  });
});
