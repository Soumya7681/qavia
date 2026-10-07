// The in-container browser server (BE-7.1).
//
// It owns one Chromium instance and answers commands on stdin, one JSON object per
// line, replying with one JSON object per line on stdout. That shape is deliberate:
//
//   * A browser session is stateful — page three only makes sense after the login on
//     page one — so a container per command would mean replaying the whole flow for
//     every step. One container per session, destroyed when the session ends, keeps
//     the platform's one-shot rule at the level where it matters.
//   * Line-delimited JSON needs no port, so the container still runs with no network
//     interface except the one the target's allowlist opened. A control channel over
//     HTTP would have meant listening on something.
//   * Every reply is bounded here rather than in Go. A snapshot of a page with four
//     thousand elements is not a snapshot anybody can act on, and it is prompt input
//     the platform pays for by the token.
//
// What this file will not do: evaluate arbitrary JavaScript supplied by the caller.
// The commands are a fixed vocabulary, because "run this script in the page" is a
// tool that can read any credential the browser holds and post it anywhere the
// allowlist permits.

import { chromium } from '@playwright/test';
import { createInterface } from 'node:readline';
import { mkdirSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

const ARTIFACTS = '/workspace/.qavia';
mkdirSync(ARTIFACTS, { recursive: true });

// Bounds on what one reply may carry. Each is a token cost as much as a memory one.
const MAX_ELEMENTS = 120;
const MAX_TEXT = 4000;
const MAX_LABEL = 120;

const target = process.env.QAVIA_TARGET_URL ?? '';
const token = process.env.QAVIA_AUTH_TOKEN ?? '';
const authMode = process.env.QAVIA_AUTH_MODE ?? 'none';
const username = process.env.QAVIA_AUTH_USERNAME ?? '';
const password = process.env.QAVIA_AUTH_PASSWORD ?? '';

// Everything secret this process knows, so a reply can be scrubbed before it leaves.
// The redaction happens here, at the boundary, rather than in Go: a value that never
// crosses the pipe cannot be logged, recorded in a trace, or written into a generated
// file (BE-7.3.3).
const secrets = [token, password].filter((value) => value && value.length >= 4);

function redact(value) {
  if (typeof value !== 'string') return value;
  let out = value;
  for (const secret of secrets) out = out.split(secret).join('[redacted]');
  return out;
}

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});

const contextOptions = {
  ignoreHTTPSErrors: false,
  // Recorded for every session, because a discovery run that found something odd is
  // worth watching back, and a failing UI test with no video is a bug report nobody
  // can act on (BE-7.5).
  recordVideo: { dir: `${ARTIFACTS}/video`, size: { width: 1280, height: 720 } },
  viewport: { width: 1280, height: 720 },
};

// Header and basic auth are applied to the context rather than typed into a form,
// because they are transport concerns. A form login is a flow the agent drives.
if (authMode === 'bearer' && token) {
  contextOptions.extraHTTPHeaders = { Authorization: `Bearer ${token}` };
} else if (authMode === 'api-key' && token) {
  contextOptions.extraHTTPHeaders = { [process.env.QAVIA_AUTH_HEADER || 'X-API-Key']: token };
} else if (authMode === 'basic' && username) {
  contextOptions.httpCredentials = { username, password };
}

const context = await browser.newContext(contextOptions);
await context.tracing.start({ screenshots: true, snapshots: true, sources: false });

const page = await context.newPage();

// The console and the failed requests are worth keeping: half of what makes a UI flaky
// shows up here and nowhere else.
const console_lines = [];
page.on('console', (message) => {
  if (console_lines.length < 200) {
    console_lines.push(redact(`${message.type()}: ${message.text()}`.slice(0, 300)));
  }
});
const failed = [];
page.on('requestfailed', (request) => {
  if (failed.length < 100) {
    failed.push(redact(`${request.method()} ${request.url()} — ${request.failure()?.errorText ?? ''}`.slice(0, 300)));
  }
});

/** A compact description of what a person could do on this page. */
async function snapshot() {
  const url = page.url();
  const title = await page.title().catch(() => '');

  // Interactive elements only, and described the way the selector policy wants them
  // named: a test id, then a role and an accessible name, then text (BE-7.4.2).
  const elements = await page.evaluate((limits) => {
    const selector = 'a, button, input, select, textarea, [role="button"], [role="link"], [role="tab"], [contenteditable="true"]';
    const seen = [];

    for (const node of Array.from(document.querySelectorAll(selector))) {
      if (seen.length >= limits.max) break;

      const style = window.getComputedStyle(node);
      if (style.display === 'none' || style.visibility === 'hidden') continue;
      const box = node.getBoundingClientRect();
      if (box.width === 0 && box.height === 0) continue;

      const testid = node.getAttribute('data-testid') || node.getAttribute('data-test-id')
        || node.getAttribute('data-cy') || '';
      const label = (node.getAttribute('aria-label')
        || node.labels?.[0]?.textContent
        || node.getAttribute('placeholder')
        || node.textContent || '').trim().replace(/\s+/g, ' ').slice(0, limits.label);

      seen.push({
        tag: node.tagName.toLowerCase(),
        type: node.getAttribute('type') || '',
        role: node.getAttribute('role') || '',
        name: node.getAttribute('name') || '',
        testid,
        label,
        href: node.getAttribute('href') || '',
        disabled: Boolean(node.disabled),
      });
    }
    return seen;
  }, { max: MAX_ELEMENTS, label: MAX_LABEL });

  const text = (await page.evaluate(() => document.body?.innerText ?? '')).slice(0, MAX_TEXT);

  return {
    url: redact(url),
    title: redact(title),
    elements: elements.map((element) => ({ ...element, label: redact(element.label) })),
    text: redact(text),
    console: console_lines.slice(-10),
    failedRequests: failed.slice(-10),
  };
}

/** Resolve a command's target into a locator, preferring the stable selectors. */
function locate(command) {
  if (command.testid) return page.getByTestId(command.testid);
  if (command.role && command.name) return page.getByRole(command.role, { name: command.name, exact: false });
  if (command.label) return page.getByLabel(command.label, { exact: false });
  if (command.text) return page.getByText(command.text, { exact: false }).first();
  if (command.selector) return page.locator(command.selector).first();
  return null;
}

// Artifacts leave over this same channel, base64 encoded, and that is not a
// convenience: Docker cannot copy a file out of a tmpfs mount, and the workspace is a
// tmpfs because the container's root filesystem is read-only. A trace that cannot be
// copied has to be written down the pipe or not collected at all (BE-7.5).
const MAX_ARTIFACT = 24 * 1024 * 1024;

/** Every file under the artifact directory, with its size. */
function listArtifacts(directory = ARTIFACTS, prefix = '.qavia') {
  const found = [];
  for (const entry of readdirSync(directory)) {
    const path = join(directory, entry);
    const info = statSync(path);
    if (info.isDirectory()) {
      found.push(...listArtifacts(path, `${prefix}/${entry}`));
      continue;
    }
    found.push({ name: `${prefix}/${entry}`, bytes: info.size });
  }
  return found;
}

const handlers = {
  async goto(command) {
    const destination = command.url?.startsWith('http') ? command.url : `${target}${command.url ?? '/'}`;
    const response = await page.goto(destination, { waitUntil: 'domcontentloaded', timeout: 20_000 });
    return { status: response?.status() ?? 0, ...(await snapshot()) };
  },

  async click(command) {
    const locator = locate(command);
    if (!locator) throw new Error('click needs a testid, role and name, label, text, or selector');
    await locator.click({ timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded', { timeout: 10_000 }).catch(() => {});
    return snapshot();
  },

  async fill(command) {
    const locator = locate(command);
    if (!locator) throw new Error('fill needs a testid, role and name, label, or selector');

    // A command may ask for the configured credential by name rather than carrying it:
    // the value never leaves the settings store, never reaches the agent, and never
    // appears in a generated file (BE-7.3.2).
    let value = command.value ?? '';
    if (value === '$QAVIA_USERNAME') value = username;
    if (value === '$QAVIA_PASSWORD') value = password;

    await locator.fill(value, { timeout: 10_000 });
    return snapshot();
  },

  async press(command) {
    await page.keyboard.press(command.key ?? 'Enter');
    await page.waitForLoadState('domcontentloaded', { timeout: 10_000 }).catch(() => {});
    return snapshot();
  },

  async back() {
    await page.goBack({ waitUntil: 'domcontentloaded', timeout: 15_000 }).catch(() => {});
    return snapshot();
  },

  async snapshot() {
    return snapshot();
  },

  // finish flushes what only exists at the end of a session: the trace is written when
  // tracing stops, and the video when the context closes. Separate from `end` so the
  // caller can collect the artifacts before the container goes away.
  async finish() {
    await context.tracing.stop({ path: `${ARTIFACTS}/trace.zip` }).catch(() => {});
    await context.close().catch(() => {});
    finished = true;
    return { artifacts: listArtifacts() };
  },

  async artifacts() {
    return { artifacts: listArtifacts() };
  },

  async artifact(command) {
    const name = String(command.name || '');
    if (!name.startsWith('.qavia/')) {
      throw new Error('an artifact name has to be inside .qavia/');
    }

    const path = join('/workspace', name);
    const info = statSync(path);
    if (info.size > MAX_ARTIFACT) {
      throw new Error(`${name} is ${info.size} bytes, over the ${MAX_ARTIFACT} limit`);
    }

    return { name, bytes: info.size, base64: readFileSync(path).toString('base64') };
  },

  async screenshot(command) {
    const name = (command.screenshot || 'shot').replace(/[^a-zA-Z0-9_-]/g, '-').slice(0, 40);
    const file = `${ARTIFACTS}/${name}.png`;
    await page.screenshot({ path: file, fullPage: Boolean(command.fullPage) });
    return { file: file.replace('/workspace/', ''), ...(await snapshot()) };
  },
};

let finished = false;

const output = createInterface({ input: process.stdin });

// Ready first, so the caller knows the browser is up before it sends anything: a
// command written into a pipe nobody is reading yet is a command that disappears.
process.stdout.write(`${JSON.stringify({ ok: true, ready: true })}\n`);

for await (const line of output) {
  const trimmed = line.trim();
  if (!trimmed) continue;

  let command;
  try {
    command = JSON.parse(trimmed);
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ ok: false, error: 'unreadable command' })}\n`);
    continue;
  }

  if (command.action === 'end') break;

  const handler = handlers[command.action];
  if (!handler) {
    process.stdout.write(`${JSON.stringify({
      ok: false,
      error: `unknown action ${command.action}; use goto, click, fill, press, back, snapshot, screenshot, or end`,
    })}\n`);
    continue;
  }

  try {
    const result = await handler(command);
    process.stdout.write(`${JSON.stringify({ ok: true, ...result })}\n`);
  } catch (error) {
    // A failed step is a result, not the end of the session: a click on something that
    // moved is exactly what discovery is for, and the agent decides what to do next.
    process.stdout.write(`${JSON.stringify({
      ok: false,
      error: redact(String(error?.message ?? error)).slice(0, 600),
      ...(await snapshot().catch(() => ({}))),
    })}\n`);
  }
}

// A caller that never sent `finish` still gets its evidence written, so a session that
// ended abruptly is not a session with no trace (BE-7.5).
if (!finished) {
  await context.tracing.stop({ path: `${ARTIFACTS}/trace.zip` }).catch(() => {});
  await context.close().catch(() => {});
}
await browser.close().catch(() => {});
