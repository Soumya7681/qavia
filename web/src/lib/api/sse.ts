import { EventSourceParserStream } from "eventsource-parser/stream";

import { CORRELATION_HEADER } from "./client";
import { ApiError, apiErrorFromResponse } from "./errors";

export type StreamState = "connecting" | "open" | "reconnecting" | "closed";

export type StreamMessage = { event: string; data: string; id?: string };

export type StreamOptions = {
  /** Return "stop" to close the stream from inside a handler. */
  onMessage: (message: StreamMessage) => void | "stop";
  onState?: (state: StreamState, error?: ApiError) => void;
  /**
   * The server ended the stream cleanly. That happens when the thing being watched
   * finished, and also when the server restarted; only the caller can tell which.
   * Resolve true to reconnect.
   */
  onEnd?: () => boolean | Promise<boolean>;
  fetch?: typeof globalThis.fetch;
  baseDelayMs?: number;
  maxDelayMs?: number;
  /**
   * Silence longer than this, with not even a heartbeat comment, means the
   * connection is dead even though nothing said so: a proxy can hold the browser's
   * side open after the server behind it has gone. The API sends a heartbeat every
   * 15 seconds, so the default allows two to go missing.
   */
  idleTimeoutMs?: number;
};

export const DEFAULT_IDLE_TIMEOUT_MS = 35_000;

/** 0.5s, 1s, 2s, 4s ... capped, with jitter so a fleet of tabs does not reconnect in step. */
export function backoffDelay(attempt: number, base = 500, max = 15_000, random = Math.random) {
  const exponential = Math.min(max, base * 2 ** Math.max(0, attempt - 1));
  return Math.round(exponential / 2 + (random() * exponential) / 2);
}

function sleep(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

/**
 * A server-sent event stream that reconnects and resumes.
 *
 * Built on fetch rather than EventSource because resuming needs the
 * `Last-Event-ID` header on a reconnect this code starts, and EventSource only
 * sends it on reconnects it starts itself, with no backoff control. Never silently
 * stops: every gap is reported through `onState` as "reconnecting" until it ends
 * in "open" or a "closed" carrying the reason.
 */
export function openEventStream(url: string, options: StreamOptions): { close: () => void } {
  const controller = new AbortController();
  const fetchImpl = options.fetch ?? globalThis.fetch;
  let lastEventId: string | undefined;
  let stopped = false;

  const setState = (state: StreamState, error?: ApiError) => {
    if (!stopped || state === "closed") {
      options.onState?.(state, error);
    }
  };

  const close = (error?: ApiError) => {
    if (stopped) return;
    stopped = true;
    controller.abort();
    options.onState?.("closed", error);
  };

  const run = async () => {
    let attempt = 0;
    while (!stopped) {
      setState(attempt === 0 ? "connecting" : "reconnecting");
      // One controller per attempt, so the watchdog can end this connection
      // without ending the stream.
      const attemptController = new AbortController();
      const onClose = () => attemptController.abort();
      controller.signal.addEventListener("abort", onClose, { once: true });
      let watchdog: ReturnType<typeof setTimeout> | undefined;
      let reader:
        ReadableStreamDefaultReader<{ id?: string; event?: string; data: string }> | undefined;
      let silent = false;
      const feed = () => {
        clearTimeout(watchdog);
        watchdog = setTimeout(() => {
          silent = true;
          attemptController.abort();
          // Not every body honours the abort signal; cancelling the reader ends the
          // read either way.
          void reader?.cancel().catch(() => {});
        }, options.idleTimeoutMs ?? DEFAULT_IDLE_TIMEOUT_MS);
      };
      try {
        const headers: Record<string, string> = {
          Accept: "text/event-stream",
          [CORRELATION_HEADER]: crypto.randomUUID(),
        };
        if (lastEventId) headers["Last-Event-ID"] = lastEventId;

        const target = new URL(url, globalThis.location?.href ?? "http://localhost").toString();
        const response = await fetchImpl(target, {
          headers,
          credentials: "same-origin",
          cache: "no-store",
          signal: attemptController.signal,
        });

        if (!response.ok) {
          const error = await apiErrorFromResponse(response, headers[CORRELATION_HEADER]);
          // A 4xx is an answer, not an outage: retrying it only repeats it.
          if (response.status < 500 && response.status !== 429) {
            close(error);
            return;
          }
          throw error;
        }
        if (!response.body) {
          throw new Error("event stream has no body");
        }

        attempt = 0;
        setState("open");
        feed();

        reader = response.body
          .pipeThrough(
            // Every byte counts as a sign of life, heartbeat comments included,
            // which the parser below would otherwise swallow unseen.
            new TransformStream<Uint8Array, AllowSharedBufferSource>({
              transform(chunk, out) {
                feed();
                out.enqueue(chunk);
              },
            }),
          )
          .pipeThrough(new TextDecoderStream())
          .pipeThrough(new EventSourceParserStream())
          .getReader();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          if (value.id) lastEventId = value.id;
          const verdict = options.onMessage({
            event: value.event ?? "message",
            data: value.data,
            id: value.id,
          });
          if (verdict === "stop") {
            close();
            return;
          }
        }

        if (silent) throw new Error("event stream went silent");
        if (stopped) return;
        const again = options.onEnd ? await options.onEnd() : true;
        if (!again) {
          close();
          return;
        }
      } catch {
        if (stopped || controller.signal.aborted) return;
      } finally {
        clearTimeout(watchdog);
        controller.signal.removeEventListener("abort", onClose);
      }

      attempt += 1;
      setState("reconnecting");
      await sleep(
        backoffDelay(attempt, options.baseDelayMs, options.maxDelayMs),
        controller.signal,
      );
    }
  };

  void run();
  return { close: () => close() };
}
