// The in-container security probe runner (BE-9.4).
//
// It reads a resolved probe list on stdin — each probe already carrying the exact
// payload string from the reviewed Go library, the request to send it in, and the rule
// to judge the response by — sends the requests, and writes one result per probe plus a
// finding for each that its rule matched. The payloads are never chosen here; this
// process substitutes and sends.
//
// The detection rules are deliberately concrete and conservative. A finding is a fact a
// person can confirm: a payload reflected unescaped, a database error the payload
// provoked, a request that should have been refused returning 2xx. A rule that guessed
// would produce findings nobody trusts, and a security report nobody trusts is worse
// than none.

import { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { readFileSync, writeFileSync } from 'node:fs';

const REPORT = '/workspace/.qavia/report.json';
const PROBES = '/workspace/probes.json';

// The probe list is delivered as a workspace file rather than on stdin, because the
// workspace is the channel the driver already uses and stdin carries the tar that
// created it. The file holds the resolved payloads and the credential; it lives only in
// the container's tmpfs and never leaves.
function readConfig() {
  const raw = readFileSync(PROBES, 'utf8').trim();
  if (!raw) throw new Error('no probe list was delivered');
  const config = JSON.parse(raw);
  if (config.schema !== 'qavia.probes/1') throw new Error(`unknown probe schema ${config.schema}`);
  return config;
}

/** One HTTP request, returning status, headers, and a bounded body. */
function send(target) {
  return new Promise((resolve) => {
    let url;
    try {
      url = new URL(target.url);
    } catch {
      resolve({ error: 'unbuildable url', status: 0, body: '' });
      return;
    }

    const transport = url.protocol === 'https:' ? httpsRequest : httpRequest;
    const options = {
      method: target.method || 'GET',
      headers: target.headers || {},
      // The container's egress allowlist already pins what is reachable; this timeout is
      // so a slow target does not hold the whole scan open.
      timeout: 15_000,
    };

    const req = transport(url, options, (res) => {
      const parts = [];
      let size = 0;
      res.on('data', (chunk) => {
        size += chunk.length;
        if (size <= 64 * 1024) parts.push(chunk);
      });
      res.on('end', () => {
        resolve({
          status: res.statusCode || 0,
          headers: res.headers,
          body: Buffer.concat(parts).toString('utf8'),
        });
      });
    });

    req.on('error', (error) => resolve({ error: String(error?.message ?? error), status: 0, body: '' }));
    req.on('timeout', () => { req.destroy(); resolve({ error: 'timeout', status: 0, body: '' }); });

    if (target.body) req.write(target.body);
    req.end();
  });
}

/** Signatures that mean the response leaked a database error a SQL payload provoked. */
const DB_ERRORS = [
  'sql syntax', 'syntax error at or near', 'unclosed quotation', 'quoted string not properly terminated',
  'ORA-0', 'pg_query', 'mysql_fetch', 'sqlite3.', 'psycopg2', 'SQLSTATE', 'you have an error in your sql',
];

/** Apply a probe's detection rule to a response, returning evidence or null. */
function judge(probe, response, baseline) {
  if (response.error) return null;

  switch (probe.detect) {
    case 'reflected': {
      // The exact payload marker comes back unescaped. The marker is unique, so a
      // reflection is unambiguous rather than a coincidental substring.
      if (probe.marker && response.body.includes(probe.marker)) {
        return `the payload was reflected unescaped in the response body (marker ${probe.marker})`;
      }
      return null;
    }
    case 'error': {
      const lower = response.body.toLowerCase();
      const hit = DB_ERRORS.find((signature) => lower.includes(signature.toLowerCase()));
      if (hit) return `the payload provoked a database error in the response ("${hit}")`;
      // A boolean pair is judged by difference from its baseline rather than an error.
      if (baseline && probe.pairKey && response.status !== baseline.status) {
        return `the response status (${response.status}) differed from the control request (${baseline.status}), evidence the input reaches the query`;
      }
      return null;
    }
    case 'status_success': {
      if (response.status >= 200 && response.status < 300) {
        return `a request that should have been refused returned ${response.status}`;
      }
      return null;
    }
    case 'not_rate_limited': {
      // Judged over the burst, not one response: handled by the caller.
      return null;
    }
    case 'accepted': {
      if (response.status >= 200 && response.status < 300) {
        return `the tampered token was accepted (status ${response.status})`;
      }
      return null;
    }
    default:
      return null;
  }
}

const config = readConfig();
const results = [];
const findings = [];

for (const probe of config.probes || []) {
  const name = `${probe.payloadId} ${probe.endpoint}`;

  if (probe.detect === 'not_rate_limited') {
    // A burst: send many and look for any 429. No 429 across the burst is the finding.
    let limited = false;
    let sent = 0;
    for (let i = 0; i < (probe.burst || 20); i += 1) {
      const response = await send(probe.request);
      sent += 1;
      if (response.status === 429) { limited = true; break; }
    }
    const found = !limited;
    results.push({ name, file: 'security', status: found ? 'failed' : 'passed', durationMs: 0,
      failureMessage: found ? `sent ${sent} requests with no 429` : null });
    if (found) {
      findings.push({ payloadId: probe.payloadId, category: probe.category, endpoint: probe.endpoint,
        parameter: probe.parameter || '', severity: probe.severity,
        evidence: `${sent} identical requests were accepted with no rate limiting (no 429)`,
        reproduction: `Send ${sent}+ ${probe.request.method} requests to ${probe.endpoint} in quick succession.` });
    }
    continue;
  }

  // A boolean-pair probe sends a control first, so a difference is measured rather than
  // guessed.
  let baseline = null;
  if (probe.control) baseline = await send(probe.control);

  const response = await send(probe.request);
  const evidence = judge(probe, response, baseline);
  const found = evidence !== null;

  results.push({ name, file: 'security', status: found ? 'failed' : 'passed', durationMs: 0,
    failureMessage: found ? evidence : null });

  if (found) {
    findings.push({
      payloadId: probe.payloadId, category: probe.category, endpoint: probe.endpoint,
      parameter: probe.parameter || '', severity: probe.severity,
      evidence,
      reproduction: `${probe.request.method} ${probe.reproUrl || probe.endpoint}` +
        (probe.parameter ? ` with ${probe.parameter} set to the payload` : '') +
        (probe.deliveryNote ? ` (${probe.deliveryNote})` : ''),
    });
  }
}

writeFileSync(REPORT, `${JSON.stringify({
  schema: 'qavia.run/1',
  framework: 'security',
  results,
  findings,
})}\n`);
