'use strict';
// Build-time inspection of the exact Wasm artifact; never parses user input.
const fs = require('node:fs');
require(process.argv[2]);
(async () => {
  const go = new Go();
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(process.argv[3]), go.importObject);
  void go.run(instance);
  const metadata = globalThis.cpraCollectionBuildJSON;
  if (typeof metadata !== 'string' || Buffer.byteLength(metadata) > 65536) throw new Error('Missing bounded parser build information');
  const information = JSON.parse(metadata);
  const profile = globalThis.cpraCollectionNormalizationProfile;
  if (typeof profile !== 'string' || profile !== 'cpra.file.base.v1') throw new Error('Missing supported parser normalization profile');
  information.NormalizationProfile = profile;
  process.stdout.write(JSON.stringify(information), () => process.exit(0));
})().catch(() => { process.stderr.write('Parser build inspection failed.\n'); process.exitCode = 1; });
