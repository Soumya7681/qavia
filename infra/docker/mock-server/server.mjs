// The mock server (BE-8.6, F-10.6 to F-10.8).
//
// It serves a shape, not a system. The platform generates schema-valid response bodies
// in Go — seeded, deterministic, from the client's own specification — and hands them
// here as configuration; this process matches a request to a route and returns one.
// Keeping the generator out of the container means the mock has no dependencies, no
// npm install, and nothing to keep in step with the faker.
//
// Three things it does that a static file server could not:
//
//   * **Path parameters.** `/orders/{id}` matches `/orders/42`, because a client app
//     built against the real API sends real-looking URLs.
//   * **Fault injection.** A configurable delay, a failure rate, chosen status codes,
//     and a timeout rate — because an app that has only ever seen a 200 has never
//     exercised its own error handling, and that is what a mock is uniquely good for
//     (F-10.7).
//   * **A record of what it was asked.** Every request is counted and the last few are
//     kept, so "the app said it called us" is checkable.
//
// The configuration arrives on stdin as one JSON line and is never written to disk: the
// container's root filesystem is read-only, and a mock that needed a config file would
// need something to write one first. One *line* rather than everything up to end of
// file, because closing the write half of a hijacked Docker stream tears down the whole
// connection — so the platform writes the line and leaves stdin open, and this reads
// exactly one.

import { createServer } from 'node:http';
import { createInterface } from 'node:readline';

const PORT = Number(process.env.QAVIA_MOCK_PORT || 8080);
const MAX_RECENT = 50;

/** Read one line of configuration from stdin, leaving the stream open. */
async function readConfig() {
  const lines = createInterface({ input: process.stdin });

  for await (const line of lines) {
    const raw = line.trim();
    if (!raw) continue;

    const config = JSON.parse(raw);
    if (config.schema !== 'qavia.mock/1') {
      throw new Error(`unknown config schema ${config.schema}`);
    }

    // Stop consuming stdin: nothing else is sent on it, and holding the reader open
    // would keep the process referenced after the server closes.
    lines.close();
    process.stdin.pause();
    return config;
  }

  throw new Error('no configuration arrived on stdin');
}

const config = await readConfig();

// Deterministic rotation rather than a random pick: the same sequence of requests gets
// the same sequence of responses, which is what makes a client-side test written
// against the mock reproducible.
const cursors = new Map();

// A seeded generator for the fault decisions, so "30% failures" is reproducible too. An
// unseeded Math.random would make a flaky client test indistinguishable from a mock that
// injected a fault at a different moment.
let seed = BigInt(config.faults?.seed ?? 1);
function nextRandom() {
  // xorshift64*, which is short, has no dependencies, and is plenty for choosing
  // whether this request is the one that fails.
  seed ^= seed >> 12n;
  seed ^= (seed << 25n) & 0xffffffffffffffffn;
  seed ^= seed >> 27n;
  const value = (seed * 2685821657736338717n) & 0xffffffffffffffffn;
  return Number(value >> 11n) / Number(1n << 53n);
}

/** Compile a route's path into a matcher that understands {parameters}. */
function compile(route) {
  const pattern = route.path
    .replace(/[.*+?^${}()|[\]\\]/g, (character) => (character === '{' || character === '}' ? character : `\\${character}`))
    .replace(/\{[^/}]+\}/g, '([^/]+)');

  return {
    ...route,
    matcher: new RegExp(`^${pattern}$`),
  };
}

const routes = (config.routes ?? []).map(compile);

const stats = {
  startedAt: new Date().toISOString(),
  requests: 0,
  matched: 0,
  unmatched: 0,
  faults: 0,
  recent: [],
};

/** The route for a request, or null. */
function route(method, path) {
  for (const candidate of routes) {
    if (candidate.method !== method) continue;
    if (candidate.matcher.test(path)) return candidate;
  }
  return null;
}

/** The next response for a route, rotating through whatever samples it was given. */
function sample(matched) {
  const responses = matched.responses ?? [];
  if (responses.length === 0) return { status: matched.status ?? 200, body: null };

  const index = (cursors.get(matched.key) ?? 0) % responses.length;
  cursors.set(matched.key, index + 1);
  return responses[index];
}

/** The fault to inject for this request, or null for none. */
function fault() {
  const faults = config.faults ?? {};

  if (faults.timeoutRate > 0 && nextRandom() < faults.timeoutRate) {
    return { kind: 'timeout' };
  }
  if (faults.failureRate > 0 && nextRandom() < faults.failureRate) {
    const codes = faults.statusCodes?.length ? faults.statusCodes : [500];
    const status = codes[Math.floor(nextRandom() * codes.length) % codes.length];
    return { kind: 'status', status };
  }
  return null;
}

function record(method, path, status, injected) {
  stats.requests += 1;
  if (injected) stats.faults += 1;

  stats.recent.push({
    at: new Date().toISOString(),
    method,
    path,
    status,
    fault: injected?.kind ?? null,
  });
  if (stats.recent.length > MAX_RECENT) stats.recent.shift();
}

function send(response, status, body, headers = {}) {
  const payload = body === null || body === undefined ? '' : JSON.stringify(body);
  response.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(payload),
    // Said in a header on every response, because a mock nobody can tell apart from the
    // real API is a mock somebody will eventually mistake for one.
    'X-Qavia-Mock': '1',
    ...headers,
  });
  response.end(payload);
}

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

const server = createServer(async (request, response) => {
  const url = new URL(request.url ?? '/', 'http://mock.invalid');
  const path = url.pathname;
  const method = (request.method ?? 'GET').toUpperCase();

  // The control endpoints answer before any fault is considered: a mock injecting a
  // 500 into its own health check is a mock nobody can supervise.
  if (path === '/__qavia/health') {
    return send(response, 200, { ok: true, routes: routes.length });
  }
  if (path === '/__qavia/stats') {
    return send(response, 200, stats);
  }

  // Drained so a client that sent a body is not left with an unwritten socket, and
  // capped so a mock cannot be used to fill a container's memory.
  let received = 0;
  for await (const chunk of request) {
    received += chunk.length;
    if (received > 1 << 20) break;
  }

  const matched = route(method, path);
  if (!matched) {
    stats.unmatched += 1;
    record(method, path, 404, null);
    return send(response, 404, {
      error: 'no such route in this mock',
      hint: 'the mock serves only what the specification declares',
      method,
      path,
    });
  }
  stats.matched += 1;

  const injected = fault();
  const faults = config.faults ?? {};

  if (faults.delayMs > 0) {
    await delay(faults.delayMs);
  }

  if (injected?.kind === 'timeout') {
    // Deliberately no response, and the socket stays open: this is what a real timeout
    // looks like to a client, and a 504 would be a different test.
    record(method, path, 0, injected);
    return undefined;
  }

  if (injected?.kind === 'status') {
    record(method, path, injected.status, injected);
    return send(response, injected.status, {
      error: 'injected failure',
      status: injected.status,
      mock: true,
    });
  }

  const chosen = sample(matched);
  record(method, path, chosen.status ?? 200, null);
  return send(response, chosen.status ?? 200, chosen.body ?? null);
});

// A slow client must not hold a connection forever, and a mock is often pointed at by
// something under development.
server.headersTimeout = 30_000;
server.requestTimeout = 60_000;

server.listen(PORT, '0.0.0.0', () => {
  // The ready line goes to stdout so the platform can wait for it rather than polling a
  // port that may not be open yet.
  process.stdout.write(`${JSON.stringify({ ok: true, ready: true, port: PORT, routes: routes.length })}\n`);
});

for (const signal of ['SIGTERM', 'SIGINT']) {
  process.on(signal, () => {
    server.close(() => process.exit(0));
    // A connection held open by a client must not stop the stop.
    setTimeout(() => process.exit(0), 5_000).unref();
  });
}
