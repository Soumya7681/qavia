// @vitest-environment happy-dom
import { renderHook, waitFor } from "@testing-library/react";
import { http } from "msw";
import { describe, expect, it, vi } from "vitest";

import { eventStream, server, setupMockApi } from "@/test/msw";

import { useSSE } from "./use-sse";

setupMockApi();

describe("useSSE", () => {
  it("closes the connection on unmount", async () => {
    const stream = eventStream();
    server.use(http.get("*/stream", () => stream.response()));
    const realFetch = globalThis.fetch;
    const signals: AbortSignal[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation((input, init) => {
      if (init?.signal) signals.push(init.signal);
      return realFetch(input, init);
    });

    const messages: string[] = [];
    const { result, unmount } = renderHook(() =>
      useSSE("/stream", { onMessage: (m) => void messages.push(m.data) }),
    );
    await waitFor(() => expect(result.current.state).toBe("open"));
    stream.send("log", "line");
    await waitFor(() => expect(messages).toEqual(['"line"']));

    unmount();
    expect(signals).toHaveLength(1);
    expect(signals[0].aborted).toBe(true);
  });

  it("does nothing without a URL", () => {
    const { result } = renderHook(() => useSSE(null, { onMessage: () => {} }));
    expect(result.current.state).toBe("closed");
  });
});
