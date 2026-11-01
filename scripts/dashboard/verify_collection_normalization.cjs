'use strict';
// Qualify exact immutable profile vectors against independently compiled
// native/Wasm adapters. Arguments are explicit test artifacts, never user input.
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { isDeepStrictEqual } = require('node:util');
const [runtime, wasm, ...natives] = process.argv.slice(2);
if (!runtime || !wasm || natives.length === 0) throw new Error('Expected runtime, Wasm and native parser artifacts');
require(path.resolve(runtime));
const fixtures = JSON.parse(fs.readFileSync(path.join(__dirname, '../../sdk/go/collection/testdata/file-base-v5.json'), 'utf8'));
const check = (condition, description) => { if (!condition) throw new Error(description); };
(async () => {
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(wasm), go.importObject);
  void go.run(instance);
  check(globalThis.cpraCollectionNormalizationProfile === fixtures.profile, 'Wasm profile identity differs');
  for (const fixture of fixtures.cases) {
    const raw = fixture.sourceBase64 ? Buffer.from(fixture.sourceBase64, 'base64') : Buffer.from(fixture.source);
    const rows = [];
    const result = globalThis.cpraDecodeCollection(new Uint8Array(raw), (id, document, item, json) => {
      rows.push({ id, document, item, json }); return true;
    });
    check(result.valid === fixture.valid && isDeepStrictEqual(rows, fixture.items), `Wasm fixed profile fixture differs: ${fixture.name}`);
    for (const native of natives) {
      const actual = JSON.parse(execFileSync(native, { input: raw, maxBuffer: 4 * 1024 * 1024 }).toString('utf8'));
      check(actual.valid === fixture.valid, `Native fixed profile acceptance differs: ${fixture.name}`);
      if (fixture.valid) {
        check(actual.profile === fixtures.profile && actual.items.length === fixture.items.length, 'Native profile or count differs');
        const rows = actual.items.map((json, i) => ({ json, document: actual.positions[i].document, item: actual.positions[i].item,
          id: JSON.parse(json).kind + '/' + JSON.parse(json).metadata.id }));
        check(isDeepStrictEqual(rows, fixture.items), `Native fixed profile output differs: ${fixture.name}`);
      }
    }
  }
  const warningCases = [
    { name: 'absent-blue', rules: {}, warn: true },
    { name: 'red-only', rules: { red: { endpointRefs: ['operations'] } }, warn: true },
    { name: 'disabled-blue', rules: { blue: { dispatch: false, endpointRefs: ['review'] } }, warn: true },
    { name: 'explicit-blue', rules: { blue: { endpointRefs: ['review'] } }, warn: false },
  ];
  for (const fixture of warningCases) {
    const raw = Buffer.from(JSON.stringify({ apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: { id: 'local-warning-fixture' },
      spec: { check: { interval: '60s', timeout: '5s', driver: { type: 'http', config: { url: 'https://example.invalid/PRIVATE_TARGET' } } },
        recovery: { driver: { type: 'webhook', config: { url: 'https://example.invalid/PRIVATE_RECOVERY' } } }, notifications: fixture.rules } }));
    let advisories;
    const result = globalThis.cpraDecodeCollection(new Uint8Array(raw), (_id, _document, _item, _json, _coordinates, warnings) => {
      check(typeof warnings === 'string' && warnings.length <= 16384, 'Wasm advisory boundary is unbounded');
      advisories = JSON.parse(warnings); return true;
    });
    check(result.valid && Array.isArray(advisories) && advisories.length === Number(fixture.warn), `Wasm shared configuration warning differs: ${fixture.name}; valid=${result.valid}; field=${result.field ?? 'none'}; reason=${result.reason ?? 'none'}; warningCount=${Array.isArray(advisories) ? advisories.length : 'unavailable'}`);
    check(!JSON.stringify(advisories).includes('PRIVATE_'), 'Wasm advisory exposed configuration values');
    if (fixture.warn) check(advisories[0].field === 'spec.notifications.blue' && advisories[0].reason === 'unknownRecoveryDestination', 'Wasm advisory lost its reviewed field/reason');
  }
  for (const count of [2, 32]) {
    const rules = Array.from({length: count}, (_, i) => ({id: `r${i}`, match: {codes: ['unexpected_http_status'], httpStatuses: i === 0 ? [100] : [100, 100 + i]}, none: true}));
    const raw = Buffer.from(JSON.stringify({apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: {id: 'overlap'}, spec: {check: {interval: '60s', timeout: '5s', driver: {type: 'http', config: {url: 'https://example.invalid'}}}, recovery: {taxonomyVersion: '1', rules}}}));
    let warnings;
    const result = globalThis.cpraDecodeCollection(new Uint8Array(raw), (_id, _doc, _item, _json, _coordinates, advisories) => {
      check(advisories.length <= 16384, 'Overlap advisories exceed byte limit'); warnings = JSON.parse(advisories); return true;
    });
    check(result.valid && warnings.length === count - 1, 'Valid overlapping rules rejected or warning count lost');
    check(warnings.every((w, i) => w.reason === 'partialOverlap' && w.field === `spec.recovery.rules[${i+1}]`), 'Overlap advisory contract differs');
  }
  {
    const rules = Array.from({length: 32}, (_, i) => ({id: `r${i}`, match: {codes: ['unexpected_http_status'], httpStatuses: i === 0 ? [100] : [100, 100 + i]}, ...(i === 0 ? {driver: {type: 'webhook', config: {url: 'https://example.invalid/PRIVATE_RECOVERY'}}} : {none: true})}));
    const raw = Buffer.from(JSON.stringify({apiVersion: 'cpra.io/v2', kind: 'Monitor', metadata: {id: 'all-warnings'}, spec: {check: {interval: '60s', timeout: '5s', driver: {type: 'http', config: {url: 'https://example.invalid'}}}, recovery: {taxonomyVersion: '1', rules}, notifications: {red: {trigger: 'recovery_failed', endpointRefs: ['operations']}}}}));
    let warnings;
    const result = globalThis.cpraDecodeCollection(new Uint8Array(raw), (_id, _doc, _item, _json, _coordinates, advisories) => {
      check(advisories.length <= 16384, 'Combined advisories exceed byte limit'); warnings = JSON.parse(advisories); return true;
    });
    check(result.valid && warnings.length === 33, 'Complete valid warning set was truncated or rejected');
    check(warnings.some(w => w.field === 'spec.notifications.red.trigger' && w.reason === 'recoveryOnlyCoverage'), 'Recovery coverage advisory missing');
    check(warnings.some(w => w.field === 'spec.notifications.blue' && w.reason === 'unknownRecoveryDestination'), 'Unknown recovery advisory missing');
    check(!JSON.stringify(warnings).includes('PRIVATE_'), 'Combined advisories exposed a value');
  }
  process.stdout.write(JSON.stringify({ result: 'passed', profile: fixtures.profile, fixedCases: fixtures.cases.length, warningCases: warningCases.length + 3, nativeBuilds: natives.length, wasmBuilds: 1 }) + '\n', () => process.exit(0));
})().catch(error => { process.stderr.write(error.message + '\n', () => process.exit(1)); });
