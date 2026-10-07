// Static validation and report normalisation for the Node runner (BE-3.4, BE-4.9).
//
// Both halves live in one file because both must work with no network and no
// dependency beyond what the image already baked in.

import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs';
import { join, extname } from 'node:path';
import { execFileSync } from 'node:child_process';

const WORKSPACE = '/workspace';

/** Every file the platform wrote, excluding its own report directory. */
function sourceFiles(dir = WORKSPACE, found = []) {
  for (const entry of readdirSync(dir)) {
    if (entry === '.qavia' || entry === 'node_modules') continue;
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) sourceFiles(path, found);
    else if (['.ts', '.js', '.mjs', '.cjs'].includes(extname(entry))) found.push(path);
  }
  return found;
}

/**
 * Patterns that mean the model produced the shape of a test rather than a test.
 *
 * They are checked here, next to the parse, because a file that compiles and asserts
 * nothing is worse than one that does not compile: it passes, forever, and reports
 * coverage it does not have (BE-3.4).
 */
const PLACEHOLDERS = [
  { pattern: /\/\/\s*TODO\b/i, message: 'contains a TODO instead of an assertion' },
  { pattern: /\/\*\s*TODO\b/i, message: 'contains a TODO instead of an assertion' },
  { pattern: /expect\(\s*true\s*\)\s*\.\s*toBe\(\s*true\s*\)/, message: 'asserts expect(true).toBe(true), which cannot fail' },
  { pattern: /expect\(\s*1\s*\)\s*\.\s*toBe\(\s*1\s*\)/, message: 'asserts expect(1).toBe(1), which cannot fail' },
  { pattern: /\b(it|test)\s*\(\s*(['"`])[^'"`]*\2\s*,\s*(async\s*)?\(\s*\)\s*=>\s*\{\s*\}\s*\)/, message: 'has a test with an empty body' },
  { pattern: /\b(it|test)\s*\.\s*(todo|skip)\s*\(/, message: 'has a test marked todo or skip' },
  { pattern: /throw new Error\(\s*(['"`])not implemented/i, message: 'throws "not implemented"' },
];

/** placeholders reports lines that assert nothing, with the line number. */
function placeholders(file) {
  const found = [];
  const lines = readFileSync(file, 'utf8').split('\n');

  for (const [index, line] of lines.entries()) {
    for (const { pattern, message } of PLACEHOLDERS) {
      if (pattern.test(line)) {
        found.push({
          file: file.replace(`${WORKSPACE}/`, ''),
          line: index + 1,
          code: 'QAVIA_PLACEHOLDER',
          message,
        });
      }
    }
  }
  return found;
}

/**
 * validate parses and type-checks without executing. A generated file that does
 * not compile is caught here rather than by a run that looks like a test failure.
 */
function validate(reportPath) {
  const files = sourceFiles();
  const problems = [];

  if (files.length === 0) {
    problems.push({ file: '', line: 0, message: 'no test files were written' });
  }

  for (const file of files) problems.push(...placeholders(file));

  const typescript = files.filter((file) => extname(file) === '.ts');
  if (typescript.length > 0) {
    // A generated workspace has no tsconfig, and the toolchain lives outside it, so
    // the config is written here: baseUrl and paths point tsc at the image's own
    // node_modules, which is the same place NODE_PATH points the run itself.
    const configPath = join(WORKSPACE, '.qavia', 'tsconfig.json');
    writeFileSync(configPath, JSON.stringify({
      compilerOptions: {
        noEmit: true,
        skipLibCheck: true,
        esModuleInterop: true,
        allowJs: false,
        target: 'es2022',
        module: 'esnext',
        moduleResolution: 'bundler',
        resolveJsonModule: true,
        types: ['node'],
        typeRoots: ['/opt/qavia/node_modules/@types'],
        baseUrl: '/opt/qavia',
        paths: { '*': ['node_modules/*'] },
      },
      files: typescript,
    }));

    try {
      execFileSync('tsc', ['--project', configPath], {
        cwd: WORKSPACE,
        stdio: ['ignore', 'pipe', 'pipe'],
        encoding: 'utf8',
      });
    } catch (error) {
      // tsc reports "file(line,col): error TS1234: message" on stdout.
      const output = `${error.stdout ?? ''}${error.stderr ?? ''}`;
      for (const line of output.split('\n')) {
        const match = line.match(/^(.+?)\((\d+),(\d+)\): error (TS\d+): (.+)$/);
        if (match) {
          problems.push({
            file: match[1].replace(`${WORKSPACE}/`, ''),
            line: Number(match[2]),
            code: match[4],
            message: match[5],
          });
        } else if (line.trim()) {
          problems.push({ file: '', line: 0, message: line.trim() });
        }
      }
    }
  }

  for (const script of files.filter((candidate) => extname(candidate) !== '.ts')) {
    try {
      execFileSync('node', ['--check', script], { stdio: ['ignore', 'ignore', 'pipe'] });
    } catch (error) {
      problems.push({ file: script, line: 0, message: String(error.stderr ?? error.message).trim() });
    }
  }

  writeFileSync(reportPath, `${JSON.stringify({
    schema: 'qavia.validate/1',
    files: files.map((file) => file.replace(`${WORKSPACE}/`, '')),
    problems,
  })}\n`);

  return problems.length === 0 ? 0 : 1;
}

/**
 * normalise turns a framework's own report into the one shape the platform reads,
 * so adding a framework never changes the Go side (BE-4.9).
 */
function normalise(inputPath, reportPath) {
  const raw = JSON.parse(readFileSync(inputPath, 'utf8'));
  const results = [];

  for (const suite of raw.testResults ?? []) {
    const file = String(suite.name ?? '').replace(`${WORKSPACE}/`, '');
    for (const test of suite.testResults ?? suite.assertionResults ?? []) {
      results.push({
        name: (test.fullName ?? test.title ?? '').trim(),
        file,
        status: { passed: 'passed', failed: 'failed', pending: 'skipped', todo: 'skipped', skipped: 'skipped' }[test.status] ?? 'failed',
        durationMs: Math.round(test.duration ?? 0),
        failureMessage: (test.failureMessages ?? []).join('\n').slice(0, 8000) || undefined,
      });
    }
  }

  writeFileSync(reportPath, `${JSON.stringify({
    schema: 'qavia.run/1',
    framework: 'node',
    startedAt: raw.startTime ? new Date(raw.startTime).toISOString() : undefined,
    results,
  })}\n`);

  return 0;
}

const [mode, ...rest] = process.argv.slice(2);
if (mode === 'validate') process.exit(validate(rest[0] ?? '/workspace/.qavia/report.json'));
else if (mode === 'normalise') process.exit(normalise(rest[0], rest[1]));
else {
  console.error(`validate.mjs: unknown mode '${mode}'`);
  process.exit(64);
}
