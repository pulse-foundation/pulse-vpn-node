import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./scan-image.sh', import.meta.url));

const runScan = (t, options = {}) => {
  const directory = mkdtempSync(join(tmpdir(), 'image-security-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const binary = join(directory, 'trivy');
  writeFileSync(
    binary,
    `#!${process.execPath}
const fs = require('node:fs');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.CALLS, JSON.stringify(args) + '\\n');
const gate = args.includes('--pkg-types');
const stage = gate ? 'gate' : args[0];
if (stage === process.env.FAIL_STAGE) process.exit(2);
const output = args[args.indexOf('--output') + 1];
fs.writeFileSync(output, stage === 'image' ? process.env.REPORT : 'Readable report: CVE-fixture\\n');
process.exit(gate ? Number(process.env.GATE_STATUS) : 0);
`,
  );
  chmodSync(binary, 0o755);
  const report = join(directory, 'reports', 'image');
  const calls = join(directory, 'calls');
  const summary = join(directory, 'summary');
  const fixture = {
    Results: [
      {
        Class: 'lang-pkgs',
        Vulnerabilities: [{ VulnerabilityID: 'CVE-fixture', Severity: options.severity ?? 'HIGH' }],
      },
    ],
  };
  const result = spawnSync(
    'bash',
    [script, 'upstream@sha256:fixture', report, options.mode ?? 'built-image'],
    {
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${directory}:${process.env.PATH}`,
        CALLS: calls,
        REPORT: options.malformed ? '{broken' : JSON.stringify(fixture),
        FAIL_STAGE: options.failStage ?? '',
        GATE_STATUS: String(options.gateStatus ?? 0),
        GITHUB_STEP_SUMMARY: summary,
      },
    },
  );
  return {
    ...result,
    report,
    summary,
    fixture,
    calls: readFileSync(calls, 'utf8')
      .trim()
      .split('\n')
      .map((line) => JSON.parse(line)),
  };
};

test('upstream findings remain visible without blocking release', (t) => {
  const result = runScan(t);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /::warning/);
  assert.match(readFileSync(result.summary, 'utf8'), /High\/Critical findings: \*\*1\*\*/);
  assert.deepEqual(JSON.parse(readFileSync(`${result.report}.json`, 'utf8')), result.fixture);
  assert.match(readFileSync(`${result.report}.txt`, 'utf8'), /CVE-fixture/);
  const [inventory, , gate] = result.calls;
  assert.equal(inventory[inventory.indexOf('--exit-code') + 1], '0');
  assert.ok(!inventory.some((arg) => /ignore|skip|severity|pkg-types/.test(arg)));
  assert.equal(gate[gate.indexOf('--pkg-types') + 1], 'os');
  assert.ok(gate.includes('--ignore-unfixed'));
  assert.equal(gate[gate.indexOf('--severity') + 1], 'HIGH,CRITICAL');
  assert.equal(gate[gate.indexOf('--exit-code') + 1], '1');
});

test('fixable OS findings block release and retain the full report', (t) => {
  const result = runScan(t, { gateStatus: 1 });
  assert.equal(result.status, 1);
  assert.deepEqual(JSON.parse(readFileSync(`${result.report}.json`, 'utf8')), result.fixture);
  assert.match(result.stdout, /Readable report/);
});

test('ready-made upstream images retain findings without applying our build gate', (t) => {
  const result = runScan(t, { mode: 'upstream-image', gateStatus: 1 });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.calls.length, 2);
  assert.deepEqual(JSON.parse(readFileSync(`${result.report}.json`, 'utf8')), result.fixture);
  assert.match(result.stdout, /::warning/);
  assert.match(readFileSync(result.summary, 'utf8'), /upstream-image/);
});

test('an upstream image scanner error still fails CI', (t) => {
  const result = runScan(t, { mode: 'upstream-image', failStage: 'image' });
  assert.equal(result.status, 2);
});

test('an unsupported mode cannot accidentally bypass the gate', () => {
  const result = spawnSync('bash', [script, 'image', '/unused', 'upstream'], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /Expected built-image or upstream-image/);
});

test('low severity inventory does not create a High/Critical warning', (t) => {
  const result = runScan(t, { severity: 'LOW' });
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /::warning/);
  assert.match(readFileSync(result.summary, 'utf8'), /High\/Critical findings: \*\*0\*\*/);
});

for (const failStage of ['image', 'convert', 'gate']) {
  test(`scanner or report failure at ${failStage} is fatal`, (t) => {
    const result = runScan(t, { failStage });
    assert.equal(result.status, 2);
  });
}

test('malformed inventory is fatal instead of reporting no findings', (t) => {
  const result = runScan(t, { malformed: true });
  assert.notEqual(result.status, 0);
  assert.equal(result.calls.length, 2);
});
