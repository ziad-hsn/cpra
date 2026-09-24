// @vitest-environment node
import { afterEach, describe, expect, it, vi } from 'vitest';
import vectors from '../../sdk/go/collection/commitment/testdata/v1.json';
import { browserCollectionLimits, collectionSourceToken, FrozenCollectionInventory, type CollectionInventoryItem } from '../src/api/collectionInventory';

const encode = (value: string) => new TextEncoder().encode(value);
const decode = (value: Uint8Array) => new TextDecoder().decode(value);
const fromHex = (value: string) => new Uint8Array(value.match(/../g)!.map(byte => Number.parseInt(byte, 16)));
const sources = () => vectors.Sources.map(source => fromHex(source.BytesHex));
const items = (): CollectionInventoryItem[] => vectors.Items.map(item => ({ ordinal: item.Position.Ordinal, id: item.Position.ID,
  source: { token: item.Position.Source.Token, document: item.Position.Source.Document, item: item.Position.Source.Item }, resourceJSON: fromHex(item.ResourceBytesHex) }));

function fixedKey(hex = vectors.KeyHex) {
  // Only the random fixture input is replaced. importKey/sign are the real native
  // Web Crypto HMAC implementation, checked against independently generated Python.
  return vi.spyOn(crypto, 'getRandomValues').mockImplementation(array => {
    if (!array) throw new Error('Missing random destination');
    const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
    if (bytes.length !== 32) throw new Error('Unexpected random destination size');
    bytes.set(fromHex(hex));
    return array;
  });
}
function resource(id = 'one', payload = 'private-fixture'): CollectionInventoryItem {
  return { ordinal: 1, id: `Credential/${id}`, source: { token: collectionSourceToken(1), document: 1, item: 1 },
    resourceJSON: encode(JSON.stringify({ apiVersion: 'cpra.io/v2', kind: 'Credential', metadata: { id }, spec: { value: payload } })) };
}
afterEach(() => vi.restoreAllMocks());

describe('browser collection inventory', () => {
  it('matches independent Python and Go vectors with exact difficult JSON spans', async () => {
    const rng = fixedKey();
    const frozen = await FrozenCollectionInventory.freeze(sources(), items());
    try {
      expect(rng).toHaveBeenCalledTimes(1);
      expect(frozen.summary.contentDigest).toBe(vectors.InventoryHex);
      const wire = frozen.serializePreflight();
      const parsed = JSON.parse(wire);
      expect(parsed).toMatchObject({ identityFormat: vectors.Format, identityKey: vectors.KeyHex, sourceFingerprint: vectors.SourceFingerprintHex,
        contentDigest: vectors.InventoryHex, itemCount: 2 });
      expect(parsed.items.map((item: { contentDigest: string }) => item.contentDigest)).toEqual(vectors.Items.map(item => item.MAC));
      vectors.Items.forEach(item => expect(wire).toContain(`"resource":${decode(fromHex(item.ResourceBytesHex))}`));
      expect(wire).toContain('18446744073709551615');
      expect(wire).toContain('"n":1.00,"exp":1e+09,"negativeZero":-0');
      expect(encode(wire).byteLength).toBe(frozen.summary.requestBytes);
      expect(frozen.summary.sourceBytes).toBe(sources().reduce((n, bytes) => n + bytes.length, 0));
    } finally { frozen.close(); }
  });

  it('matches the independently computed empty inventory without a fabricated item', async () => {
    fixedKey();
    const frozen = await FrozenCollectionInventory.freeze([], []);
    try {
      expect(frozen.summary.contentDigest).toBe(vectors.EmptyInventoryHex);
      expect(JSON.parse(frozen.serializePreflight())).toMatchObject({ sourceFingerprint: vectors.EmptySourcesHex, itemCount: 0, items: [] });
    } finally { frozen.close(); }
  });

  it('generates a new identity for identical input and keeps caller data unchanged', async () => {
    const inputSources = sources(), inputItems = items();
    const originals = inputSources.map(source => Array.from(source));
    const first = await FrozenCollectionInventory.freeze(inputSources, inputItems);
    const second = await FrozenCollectionInventory.freeze(inputSources, inputItems);
    try {
      expect(first.summary.contentDigest).not.toBe(second.summary.contentDigest);
      expect(JSON.parse(first.serializePreflight()).identityKey).not.toBe(JSON.parse(second.serializePreflight()).identityKey);
      expect(inputSources.map(source => Array.from(source))).toEqual(originals);
      expect(inputItems.map(item => Array.from(item.resourceJSON))).toEqual(items().map(item => Array.from(item.resourceJSON)));
    } finally { first.close(); second.close(); }
  });

  it('freezes owned bytes and array lengths before asynchronous cryptography', async () => {
    fixedKey();
    const inputSources = sources(), inputItems = items();
    const pending = FrozenCollectionInventory.freeze(inputSources, inputItems);
    inputSources.forEach(source => source.fill(0));
    inputItems.forEach(item => item.resourceJSON.fill(0));
    inputSources.length = 0; inputItems.length = 0;
    const frozen = await pending;
    try { expect(frozen.summary.contentDigest).toBe(vectors.InventoryHex); }
    finally { frozen.close(); }
  });

  it('changes commitments when bytes, sources, key, order or declared size change', async () => {
    for (const mode of ['resource', 'source', 'key', 'order', 'count']) {
      const rng = fixedKey(mode === 'key' ? 'ff'.repeat(32) : vectors.KeyHex);
      const inputSources = sources();
      let inputItems = items();
      if (mode === 'source') inputSources[0][0] = 33;
      if (mode === 'resource') inputItems[0] = { ...inputItems[0], resourceJSON: encode(decode(inputItems[0].resourceJSON).replace('1.00', '1.01')) };
      if (mode === 'order') inputItems = [...inputItems].reverse().map((item, i) => ({ ...item, ordinal: i + 1 }));
      if (mode === 'count') inputItems.pop();
      const frozen = await FrozenCollectionInventory.freeze(inputSources, inputItems);
      try { expect(frozen.summary.contentDigest).not.toBe(vectors.InventoryHex); }
      finally { frozen.close(); rng.mockRestore(); }
    }
  });

  it('keeps private inputs absent from ordinary inspection and disables serialization after close', async () => {
    fixedKey();
    const input = resource();
    const frozen = await FrozenCollectionInventory.freeze([encode('private-source-path')], [input]);
    expect(Object.keys(frozen)).toEqual([]);
    expect({ ...frozen }).toEqual({});
    expect(String(frozen)).toBe('Private collection inventory (key and input omitted).');
    expect(() => JSON.stringify(frozen)).toThrow('cannot be serialized');
    expect(JSON.stringify(frozen.summary)).not.toContain(vectors.KeyHex);
    expect(JSON.stringify(frozen.summary)).not.toContain('private-');
    expect(frozen.serializePreflight()).not.toContain('private-source-path');
    expect(Object.isFrozen(frozen.summary)).toBe(true);
    frozen.close(); frozen.close();
    expect(() => frozen.serializePreflight()).toThrow('closed');
  });

  it('rejects syntax, UTF-8 and identity mismatches without reflecting input diagnostics', async () => {
    for (const raw of ['null', '[]', '{}', '{"kind":"Credential","metadata":{"id":"different"}}',
      '{"kind":"Credential","metadata":{"id":"one"}},"identityKey":"private-injection"', '\ufeff{}', '{"secret":"private-input",']) {
      await expect(FrozenCollectionInventory.freeze([encode('source')], [{ ...resource(), resourceJSON: encode(raw) }])).rejects.toThrow('Invalid collection input');
    }
    await expect(FrozenCollectionInventory.freeze([encode('source')], [{ ...resource(), resourceJSON: new Uint8Array([0xff]) }])).rejects.toThrow('Invalid collection input');
  });

  it('rejects invalid positions, duplicate identities and unsafe ordinal arithmetic before randomness', async () => {
    const rng = vi.spyOn(crypto, 'getRandomValues');
    for (const bad of [0, -1, 1.1, Number.MAX_SAFE_INTEGER + 1]) {
      await expect(FrozenCollectionInventory.freeze([encode('source')], [{ ...resource(), ordinal: bad }])).rejects.toThrow('Invalid collection input');
    }
    for (const token of ['source.00000000000000000002', '/private/source.json', 'source.1']) {
      await expect(FrozenCollectionInventory.freeze([encode('source')], [{ ...resource(), source: { token, document: 1, item: 1 } }])).rejects.toThrow('Invalid collection input');
    }
    await expect(FrozenCollectionInventory.freeze([encode('source')], [resource(), { ...resource(), ordinal: 2 }])).rejects.toThrow('Invalid collection input');
    expect(rng).not.toHaveBeenCalled();
  });

  it('enforces cumulative byte and encoded-request quotas before copying or random generation', async () => {
    const rng = vi.spyOn(crypto, 'getRandomValues');
    await expect(FrozenCollectionInventory.freeze([new Uint8Array(browserCollectionLimits.sourceBytes), new Uint8Array(1)], [])).rejects.toThrow('at most 4 MiB');
    await expect(FrozenCollectionInventory.freeze([encode('source')], [{ ...resource(), resourceJSON: new Uint8Array(browserCollectionLimits.resourceBytes + 1) }])).rejects.toThrow('at most 4 MiB');
    const repeated = Array.from({ length: 4 }, (_, index) => ({ ...resource(String(index)), ordinal: index + 1, resourceJSON: new Uint8Array(browserCollectionLimits.resourceBytes) }));
    await expect(FrozenCollectionInventory.freeze([encode('source')], repeated)).rejects.toThrow('at most 4 MiB');
    await expect(FrozenCollectionInventory.freeze(Array(10_001).fill(new Uint8Array()), [])).rejects.toThrow('10000');
    await expect(FrozenCollectionInventory.freeze([encode('source')], Array(10_001).fill(resource()))).rejects.toThrow('10000');
    expect(rng).not.toHaveBeenCalled();
  });

  it('honors cancellation before and during actual asynchronous cryptography', async () => {
    const before = new AbortController(); before.abort('private-abort-reason');
    await expect(FrozenCollectionInventory.freeze([], [], before.signal)).rejects.toMatchObject({ name: 'AbortError', message: 'Collection preparation was cancelled.' });
    const during = new AbortController();
    const pending = FrozenCollectionInventory.freeze(sources(), items(), during.signal);
    during.abort('private-abort-reason');
    await expect(pending).rejects.toMatchObject({ name: 'AbortError', message: 'Collection preparation was cancelled.' });
  });
});
