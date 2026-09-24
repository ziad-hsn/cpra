// @vitest-environment node
import { afterEach, describe, expect, it, vi } from 'vitest';
import vectors from '../../sdk/go/collection/commitment/testdata/v1.json';
import { fileNormalizationProfile, WorkerCollectionInventory, workerCollectionLimits } from '../src/api/collectionInventory';
import { collectionFileSources, orderedCollectionSources } from '../src/import/files';
import { BrowserCollection, PrivateCollectionBody } from '../src/import/collection';

const encode = (text: string) => new TextEncoder().encode(text), decode = (bytes: Uint8Array) => new TextDecoder().decode(bytes);
const fromHex = (text: string) => new Uint8Array(text.match(/../g)!.map(byte => Number.parseInt(byte, 16)));
const raw = (id: string, length = 0) => encode(JSON.stringify({ apiVersion: 'cpra.io/v2', kind: 'Credential', metadata: { id }, spec: { value: 'x'.repeat(length) } }));
afterEach(() => vi.restoreAllMocks());

describe('private worker inventory', () => {
  it('records a profile only when the parser caller supplies that exact contract', async () => {
    const inventory = await WorkerCollectionInventory.create(1, fileNormalizationProfile);
    try {
      await inventory.addSource(encode('profile source'));
      inventory.addItem(raw('profile'), 'Credential/profile', 1, 1);
      expect((await inventory.finish()).normalizationProfile).toBe(fileNormalizationProfile);
      expect(JSON.parse(decode(inventory.createBody())).normalizationProfile).toBe(fileNormalizationProfile);
      // Ephemeral preflight uses exact resource bytes, independently of later
      // file reselection. It does not claim or store a file profile.
      expect(JSON.parse(decode(await inventory.preflightBody())).normalizationProfile).toBeUndefined();
    } finally { inventory.close(); }
  });
  it('matches independent Python/Go vectors and sends exact original JSON bytes', async () => {
    vi.spyOn(crypto, 'getRandomValues').mockImplementation(array => { new Uint8Array(array!.buffer, array!.byteOffset, array!.byteLength).set(fromHex(vectors.KeyHex)); return array; });
    const inventory = await WorkerCollectionInventory.create(vectors.Sources.length);
    try {
      for (const source of vectors.Sources) {
        await inventory.addSource(fromHex(source.BytesHex));
        for (const item of vectors.Items.filter(item => item.Position.Source.Token === source.Token)) inventory.addItem(fromHex(item.ResourceBytesHex), item.Position.ID, item.Position.Source.Document, item.Position.Source.Item);
      }
      expect((await inventory.finish()).contentDigest).toBe(vectors.InventoryHex);
      expect(JSON.parse(decode(inventory.createBody()))).toEqual({ identityFormat: vectors.Format, identityKey: vectors.KeyHex, sourceFingerprint: vectors.SourceFingerprintHex, contentDigest: vectors.InventoryHex, itemCount: 2 });
      const wire = decode(await inventory.preflightBody());
      for (const item of vectors.Items) expect(wire).toContain(`"resource":${decode(fromHex(item.ResourceBytesHex))}`);
      expect(JSON.parse(wire).items.map((item: { contentDigest: string }) => item.contentDigest)).toEqual(vectors.Items.map(item => item.MAC));
      expect(inventory.page(0).map(item => item.source.document)).toEqual(vectors.Items.map(item => item.Position.Source.Document));
      expect(Object.keys(inventory)).toEqual([]);
      expect(() => JSON.stringify(inventory)).toThrow('cannot be serialized');
      expect(String(inventory)).not.toContain(vectors.KeyHex);
    } finally { inventory.close(); }
  });
  it('accepts sources above 4MiB and bounds upload item counts', async () => {
    const inventory = await WorkerCollectionInventory.create(1);
    try {
      await inventory.addSource(new Uint8Array(5 << 20));
      for (let i = 0; i < 300; i++) inventory.addItem(raw(`c-${i}`), `Credential/c-${i}`, 1, i + 1);
      expect((await inventory.finish()).sourceBytes).toBe(5 << 20);
      expect(inventory.page(0)).toHaveLength(100);
      expect(() => inventory.page(0, 101)).toThrow();
      const first = await inventory.uploadBody(0), last = await inventory.uploadBody(first.nextOffset);
      expect(first.nextOffset).toBe(256);
      expect(JSON.parse(decode(first.bytes)).items).toHaveLength(256);
      expect(last.nextOffset).toBe(300);
      expect(JSON.parse(decode(last.bytes)).items).toHaveLength(44);
    } finally { inventory.close(); }
  });
  it('bounds upload bytes while retaining a large frozen collection for staging', async () => {
    const inventory = await WorkerCollectionInventory.create(1);
    try {
      await inventory.addSource(encode('source'));
      for (let i = 0; i < 6; i++) inventory.addItem(raw(`large-${i}`, 800_000), `Credential/large-${i}`, 1, i + 1);
      expect((await inventory.finish()).preflightAvailable).toBe(false);
      await expect(inventory.preflightBody()).rejects.toThrow('4 MiB');
      expect(JSON.parse(decode(inventory.createBody())).itemCount).toBe(6);
      const first = await inventory.uploadBody(0), last = await inventory.uploadBody(first.nextOffset);
      expect(first.bytes.length).toBeLessThanOrEqual(4 << 20);
      expect(first.nextOffset).toBe(5);
      expect(last.nextOffset).toBe(6);
    } finally { inventory.close(); }
  });
  it('detects changed owned bytes before transmission and clears them on close', async () => {
    const inventory = await WorkerCollectionInventory.create(1), owned = raw('tamper');
    await inventory.addSource(encode('source')); inventory.addItem(owned, 'Credential/tamper', 1, 1); await inventory.finish();
    owned[owned.length - 4] ^= 1;
    await expect(inventory.uploadBody(0)).rejects.toThrow('changed');
    inventory.close(); expect(owned.every(byte => byte === 0)).toBe(true);
    expect(() => inventory.createBody()).toThrow('closed');
    await expect(inventory.uploadBody(0)).rejects.toThrow('closed');
  });
  it('rejects duplicates, oversized resources and incomplete sources', async () => {
    const inventory = await WorkerCollectionInventory.create(2);
    try {
      await inventory.addSource(encode('first')); inventory.addItem(raw('one'), 'Credential/one', 1, 1);
      expect(() => inventory.addItem(raw('one'), 'Credential/one', 1, 2)).toThrow('duplicate');
      expect(() => inventory.addItem(new Uint8Array((1 << 20) + 1), 'Credential/large', 1, 2)).toThrow('quota');
      await expect(inventory.finish()).rejects.toThrow('incomplete');
      expect(workerCollectionLimits.normalizedBytes).toBe(512 << 20);
    } finally { inventory.close(); }
  });
});

describe('selection and explicit private bodies', () => {
  it('sorts UTF8 relative names and rejects ambiguity before reading', () => {
    const a = new File(['a'], 'a.yaml'), high = new File(['b'], '\ue000.yaml'), astral = new File(['c'], '💾.yaml');
    expect(collectionFileSources([astral, high, a]).map(source => source.name)).toEqual(['a.yaml', '\ue000.yaml', '💾.yaml']);
    expect(() => collectionFileSources([a, new File(['different'], 'a.yaml')])).toThrow('duplicate');
    expect(orderedCollectionSources([{ name: 'service-b/config.yaml', file: a }, { name: 'service-a/config.yaml', file: a }]).map(source => source.name)).toEqual(['service-a/config.yaml', 'service-b/config.yaml']);
    for (const name of ['/absolute.yaml', '../secret.yaml', 'folder\\input.yaml', 'file.txt']) expect(() => orderedCollectionSources([{ name, file: a }])).toThrow('invalid');
  });
  it('rejects counts/bytes and cancellation without starting a worker', async () => {
    const worker = vi.fn(); vi.stubGlobal('Worker', worker);
    try {
      const file = new File(['x'], 'file.yaml');
      expect(() => collectionFileSources(Array(1001).fill(file))).toThrow('quota');
      await expect(BrowserCollection.prepare([new File([new Uint8Array((64 << 20) + 1)], 'large.yaml')])).rejects.toMatchObject({ code: 'quota' });
      const controller = new AbortController(); controller.abort();
      await expect(BrowserCollection.prepare([file], { signal: controller.signal })).rejects.toMatchObject({ code: 'cancelled' });
      expect(worker).not.toHaveBeenCalled();
    } finally { vi.unstubAllGlobals(); }
  });
  it('never formats a private request and relinquishes its buffer once', () => {
    const bytes = encode('private-key-and-payload'), body = new PrivateCollectionBody(bytes.buffer, 4);
    expect(JSON.stringify({ ...body })).toBe('{"nextOffset":4}');
    expect(String(body)).not.toContain('private-key-and-payload');
    expect(() => JSON.stringify(body)).toThrow('cannot be serialized');
    expect(body.take()).toBeInstanceOf(Uint8Array);
    expect(() => body.take()).toThrow('closed');
    const other = new PrivateCollectionBody(bytes.buffer); other.close(); expect(bytes.every(byte => byte === 0)).toBe(true);
  });
});
