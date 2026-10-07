"use client";

import { useEffect, useRef, useState } from "react";

import type { ApiError } from "@/lib/api/errors";
import { openEventStream, type StreamMessage, type StreamState } from "@/lib/api/sse";

export type UseSSEOptions = {
  onMessage: (message: StreamMessage) => void | "stop";
  onEnd?: () => boolean | Promise<boolean>;
};

/**
 * Subscribes to a server-sent event stream for as long as the component is
 * mounted and `url` is set. Unmounting, or changing the URL, closes the stream: a
 * leaked connection is a real bug, because each one holds a server goroutine.
 */
export function useSSE(url: string | null, options: UseSSEOptions) {
  const [state, setState] = useState<StreamState>(url ? "connecting" : "closed");
  const [error, setError] = useState<ApiError | undefined>();

  // The latest handlers, without reopening the stream every render.
  const handlers = useRef(options);
  useEffect(() => {
    handlers.current = options;
  });

  useEffect(() => {
    if (!url) {
      return;
    }
    const stream = openEventStream(url, {
      onMessage: (message) => handlers.current.onMessage(message),
      onEnd: () => handlers.current.onEnd?.() ?? true,
      onState: (next, failure) => {
        setState(next);
        setError(failure);
      },
    });
    return () => stream.close();
  }, [url]);

  return { state: url ? state : ("closed" as const), error };
}
