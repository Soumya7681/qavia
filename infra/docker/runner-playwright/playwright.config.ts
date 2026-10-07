// The Playwright configuration the platform runs suites with (BE-4.3, BE-7.5).
//
// Baked into the image rather than generated into the workspace, and that is the
// interesting decision. A config written beside the specs would be a file a model
// could overwrite: a generated `playwright.config.ts` that turns tracing off, points
// the base URL somewhere else, or raises the timeout is a config the platform has to
// re-check on every generation. Keeping it here means the suite cannot change how it
// is run — only what it does.
//
// What it settles:
//
//   * **The base URL comes from the environment**, so a generated spec navigates with
//     paths and the same file runs against staging, against a developer's machine, and
//     in a client's own CI with nothing edited (BE-3.6).
//   * **Evidence is captured on failure and thrown away on success.** A trace per
//     passing test is megabytes nobody opens; a trace for the one test that failed at
//     2am is the difference between a bug report somebody can act on and "it was red
//     yesterday" (F-7.10).
//   * **Retries are the platform's job, not Playwright's.** The execute stage re-runs
//     failed tests and records every attempt separately, which is what makes flake
//     detection arithmetic rather than a guess (BE-4.13).

import { defineConfig } from '@playwright/test';

export default defineConfig({
  // The workspace the driver delivers the suite into.
  testDir: '/workspace',

  // Artifacts land beside the report, inside the workspace tmpfs, and leave through
  // the report channel: Docker cannot copy a file out of a tmpfs mount, and the
  // workspace is a tmpfs because the container's root filesystem is read-only.
  outputDir: '/workspace/.qavia/artifacts',

  // One test at a time. A runner container is sized for one browser, and parallel
  // workers in a memory-capped container are an OOM kill reported as a test failure.
  workers: 1,
  fullyParallel: false,

  // Zero, deliberately. The platform retries failed tests itself so that each attempt
  // is a row somebody can inspect.
  retries: 0,

  // Bounds one test. The driver enforces the run's whole wall clock; this is what
  // stops one hung navigation from consuming it.
  timeout: 60_000,
  expect: { timeout: 10_000 },

  // Nothing here reaches out for a browser: the image already has them.
  forbidOnly: true,

  use: {
    baseURL: process.env.QAVIA_TARGET_URL || undefined,

    // Kept only for a test that failed. Passing tests produce nothing.
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',

    // Not negotiable: a suite that ignores certificate errors is a suite that cannot
    // tell a misconfigured environment from a working one.
    ignoreHTTPSErrors: false,

    actionTimeout: 15_000,
    navigationTimeout: 20_000,

    launchOptions: {
      // The container is the boundary — gVisor, dropped capabilities, no network but
      // the allowlist — so Chromium's own sandbox has nothing left to add and cannot
      // start without privileges this container refuses to hold.
      args: ['--no-sandbox', '--disable-dev-shm-usage'],
    },
  },
});
