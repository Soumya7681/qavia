import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll } from "vitest";

/**
 * The API as a test sees it. Handlers are added per test with `server.use(...)`,
 * and an unhandled request fails the test: a component that calls something the
 * test did not expect is a finding, not noise.
 */
export const server = setupServer();

export function setupMockApi() {
  beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
  afterEach(() => server.resetHandlers());
  afterAll(() => server.close());
}

const encoder = new TextEncoder();

/** A text/event-stream response the test controls frame by frame. */
export function eventStream() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start: (c) => {
      controller = c;
      // A comment frame first, as a real server's first write would be: some fetch
      // implementations only resolve once the first body bytes arrive.
      c.enqueue(encoder.encode(": connected\n\n"));
    },
  });
  return {
    response: () =>
      new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
    send: (event: string, data: unknown, id?: string) =>
      controller.enqueue(
        encoder.encode(
          `${id ? `id: ${id}\n` : ""}event: ${event}\ndata: ${JSON.stringify(data)}\n\n`,
        ),
      ),
    end: () => controller.close(),
  };
}
