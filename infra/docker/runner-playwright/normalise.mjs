// Playwright's JSON reporter into the platform's one report shape (BE-4.9).
//
// Playwright nests suites arbitrarily deep and reports one entry per retry, which
// is what makes flake detection possible: the same test with a failed attempt and
// a passed attempt is flaky, not passed.

import { readFileSync, writeFileSync } from 'node:fs';

const results = [];

function walk(suite, trail) {
  const here = suite.title ? [...trail, suite.title] : trail;
  for (const spec of suite.specs ?? []) {
    for (const test of spec.tests ?? []) {
      for (const [index, attempt] of (test.results ?? []).entries()) {
        results.push({
          name: [...here, spec.title].filter(Boolean).join(' > '),
          file: spec.file ?? suite.file ?? '',
          status: { passed: 'passed', failed: 'failed', timedOut: 'failed', interrupted: 'failed', skipped: 'skipped' }[attempt.status] ?? 'failed',
          durationMs: Math.round(attempt.duration ?? 0),
          attempt: index + 1,
          failureMessage: (attempt.error?.message ?? attempt.errors?.map((error) => error.message).join('\n') ?? '').slice(0, 8000) || undefined,
        });
      }
    }
  }
  for (const child of suite.suites ?? []) walk(child, here);
}

const [input, output] = process.argv.slice(2);
const raw = JSON.parse(readFileSync(input, 'utf8'));
for (const suite of raw.suites ?? []) walk(suite, []);

writeFileSync(output, `${JSON.stringify({
  schema: 'qavia.run/1',
  framework: 'playwright',
  startedAt: raw.stats?.startTime,
  results,
})}\n`);
