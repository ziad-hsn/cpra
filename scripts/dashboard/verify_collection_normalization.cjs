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
const fixtures = JSON.parse(fs.readFileSync(path.join(__dirname, '../../sdk/go/collection/testdata/file-base-v1.json'), 'utf8'));
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
  process.stdout.write(JSON.stringify({ result: 'passed', profile: fixtures.profile, fixedCases: fixtures.cases.length, nativeBuilds: natives.length, wasmBuilds: 1 }) + '\n', () => process.exit(0));
})().catch(error => { process.stderr.write(error.message + '\n', () => process.exit(1)); });
