// Playwright's attachments into the platform's artifact channel (BE-7.5).
//
// A failing UI test is worth a video, a trace, and a screenshot, and none of them can
// be copied out of the container: the workspace is a tmpfs mount, Docker cannot copy
// a file out of one, and the workspace is a tmpfs precisely because the root
// filesystem is read-only. So they leave the way the PDF does — base64, through the
// report channel, which is a text stream the driver already reads.
//
// The caps are the interesting part. A trace of a long test is tens of megabytes, and
// base64 adds a third; a suite where forty tests fail would otherwise try to push a
// gigabyte through a pipe. What is kept is bounded per file and per run, and what is
// dropped is named in the manifest rather than silently missing: "no video" and "the
// video was 40 MB" are different facts.

import { readFileSync, writeFileSync, statSync } from 'node:fs';

const MAX_FILE = 12 * 1024 * 1024;
const MAX_TOTAL = 48 * 1024 * 1024;

const KINDS = {
  video: 'video',
  trace: 'trace',
  screenshot: 'screenshot',
};

const CONTENT_TYPES = {
  video: 'video/webm',
  trace: 'application/zip',
  screenshot: 'image/png',
};

const [input, output] = process.argv.slice(2);

let raw;
try {
  raw = JSON.parse(readFileSync(input, 'utf8'));
} catch {
  // No report means no run, and the failure is already the run's own. An empty
  // manifest keeps the contract rather than making the driver handle a missing file.
  writeFileSync(output, `${JSON.stringify({ schema: 'qavia.artifacts/1', items: [], dropped: [] })}\n`);
  process.exit(0);
}

const items = [];
const dropped = [];
let total = 0;

/** Every attachment worth keeping, with the test and attempt it belongs to. */
function walk(suite, trail) {
  const here = suite.title ? [...trail, suite.title] : trail;

  for (const spec of suite.specs ?? []) {
    const name = [...here, spec.title].filter(Boolean).join(' > ');

    for (const test of spec.tests ?? []) {
      for (const [index, attempt] of (test.results ?? []).entries()) {
        for (const attachment of attempt.attachments ?? []) {
          const kind = KINDS[attachment.name];
          if (!kind || !attachment.path) continue;

          let size = 0;
          try {
            size = statSync(attachment.path).size;
          } catch {
            dropped.push({ test: name, attempt: index + 1, kind, why: 'the file was not written' });
            continue;
          }

          if (size === 0) {
            dropped.push({ test: name, attempt: index + 1, kind, why: 'the file was empty' });
            continue;
          }
          if (size > MAX_FILE) {
            dropped.push({ test: name, attempt: index + 1, kind, why: `${size} bytes, over the ${MAX_FILE} limit` });
            continue;
          }
          if (total + size > MAX_TOTAL) {
            dropped.push({ test: name, attempt: index + 1, kind, why: 'the run is already at its artifact budget' });
            continue;
          }

          total += size;
          items.push({
            test: name,
            attempt: index + 1,
            kind,
            name: attachment.path.split('/').pop(),
            contentType: attachment.contentType || CONTENT_TYPES[kind] || 'application/octet-stream',
            bytes: size,
            base64: readFileSync(attachment.path).toString('base64'),
          });
        }
      }
    }
  }

  for (const child of suite.suites ?? []) walk(child, here);
}

for (const suite of raw.suites ?? []) walk(suite, []);

writeFileSync(output, `${JSON.stringify({ schema: 'qavia.artifacts/1', items, dropped })}\n`);
