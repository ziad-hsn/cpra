/**
 * Bounded browser counterpart of collection/commitment in the Go SDK.
 *
 * Callers must bound file reads before supplying these already-loaded bytes.
 * This is a cryptographic inventory helper, not a YAML parser or resource/schema
 * validator. Web Crypto HMAC has no streaming interface: raw sources and the
 * encoded preflight request each have a 4 MiB ceiling. Large-file browser
 * streaming and persistent/cross-tab resume are not provided here.
 */
export const collectionIdentityFormat = 'cpra.collection.hmac-sha256-json-bytes.v1' as const;
export const fileNormalizationProfile = 'cpra.file.base.v1' as const;
export const browserCollectionLimits = Object.freeze({ sourceBytes: 4 << 20, requestBytes: 4 << 20, resourceBytes: 1 << 20, items: 10_000, sources: 10_000 });

export interface CollectionInventoryPosition {
  readonly ordinal: number;
  readonly id: string;
  readonly source: { readonly token: string; readonly document: number; readonly item: number };
}
export interface CollectionInventoryItem extends CollectionInventoryPosition {
  /** Exact UTF-8 JSON object bytes to transmit; numbers/escapes are not rewritten. */
  readonly resourceJSON: Uint8Array;
}
export interface CollectionInventorySummary {
  readonly identityFormat: typeof collectionIdentityFormat;
  readonly contentDigest: string;
  readonly itemCount: number;
  readonly sourceCount: number;
  readonly sourceBytes: number;
  readonly requestBytes: number;
}

type StoredItem = { readonly position: CollectionInventoryPosition; readonly text: string; readonly mac: string };
const encoder = new TextEncoder();
const zeroHex = '0'.repeat(64);
const invalid = () => new Error('Invalid collection input or inventory position.');
const excessive = () => new Error('Browser imports support at most 4 MiB of sources and request data, 1 MiB per resource, and 10000 items or sources.');
const closed = () => new Error('The private collection has been closed.');
const cancelled = () => new DOMException('Collection preparation was cancelled.', 'AbortError');
const checkCancellation = (signal?: AbortSignal) => { if (signal?.aborted) throw cancelled(); };
const integer = (value: number, maximum: number) => Number.isSafeInteger(value) && value >= 1 && value <= maximum;
const hex = (value: Uint8Array) => Array.from(value, byte => byte.toString(16).padStart(2, '0')).join('');

/** Canonical source attribution contains no local path, URL or user label. */
export function collectionSourceToken(ordinal: number): string {
  if (!integer(ordinal, 1_000_000)) throw invalid();
  return `source.${String(ordinal).padStart(20, '0')}`;
}

function validBytes(value: Uint8Array): boolean {
  // SharedArrayBuffer input cannot be frozen consistently against another thread.
  return value instanceof Uint8Array && value.buffer instanceof ArrayBuffer;
}
function positionCopy(item: CollectionInventoryItem, index: number, sourceCount: number): CollectionInventoryPosition {
  if (item.ordinal !== index + 1 || typeof item.id !== 'string' || item.id.length > 321) throw invalid();
  const slash = item.id.indexOf('/');
  const kind = item.id.slice(0, slash), id = item.id.slice(slash + 1);
  const encoded = encoder.encode(id);
  if (slash < 1 || !/^[a-zA-Z][a-zA-Z0-9]{0,63}$/.test(kind) || encoded.length < 1 || encoded.length > 256 ||
      new TextDecoder('utf-8', { ignoreBOM: true }).decode(encoded) !== id || id === '.' || id === '..' || /[\/\\?#%\x00\r\n]/.test(id)) throw invalid();
  const source = item.source;
  if (!source || typeof source.token !== 'string' || !/^source\.\d{20}$/.test(source.token) ||
      !integer(Number(source.token.slice(7)), sourceCount) || !integer(source.document, 10_000_000) || !integer(source.item, 10_000_000)) throw invalid();
  return { ordinal: item.ordinal, id: item.id, source: { token: source.token, document: source.document, item: source.item } };
}
function itemPrefix(position: CollectionInventoryPosition, contentDigest: string): string {
  return JSON.stringify({ id: position.id, contentDigest, ordinal: position.ordinal, source: position.source.token,
    sourceDocument: position.source.document, sourceItem: position.source.item }).slice(0, -1) + ',"resource":';
}
function requestPrefix(itemCount: number, key: string, fingerprint: string, contentDigest: string): string {
  return JSON.stringify({ identityFormat: collectionIdentityFormat, identityKey: key, sourceFingerprint: fingerprint, contentDigest, itemCount }).slice(0, -1) + ',"items":[';
}

function u64(value: number): Uint8Array {
  const result = new Uint8Array(8);
  new DataView(result.buffer).setBigUint64(0, BigInt(value), false);
  return result;
}
function field(value: Uint8Array): Uint8Array[] { return [u64(value.byteLength), value]; }
function number(value: number): Uint8Array[] { return field(u64(value)); }
async function mac(key: CryptoKey, domain: string, parts: readonly Uint8Array[], signal?: AbortSignal, maximumBytes = browserCollectionLimits.sourceBytes + (1 << 20)): Promise<Uint8Array<ArrayBuffer>> {
  checkCancellation(signal);
  const framed = [...field(encoder.encode(domain)), ...parts];
  const size = framed.reduce((n, part) => n + part.byteLength, 0);
  // Source/item/inventory framing is bounded separately from the encoded request.
  if (size > maximumBytes) throw excessive();
  const input = new Uint8Array(size);
  let offset = 0;
  for (const part of framed) { input.set(part, offset); offset += part.byteLength; }
  try {
    const result = new Uint8Array(await globalThis.crypto.subtle.sign('HMAC', key, input));
    if (signal?.aborted) { result.fill(0); throw cancelled(); }
    return result;
  } finally { input.fill(0); }
}

/**
 * Private in-memory input. JSON serialization is rejected; the explicit method
 * below is the only method that returns key-bearing request data. Never place
 * that string in URLs, browser storage, diagnostics or query/React state. Once
 * returned, its lifetime belongs to the caller and JavaScript cannot erase it.
 */
export class FrozenCollectionInventory {
  #key: Uint8Array;
  #fingerprint: Uint8Array;
  #items: readonly StoredItem[];
  #closed = false;
  readonly #summary: CollectionInventorySummary;

  private constructor(key: Uint8Array, fingerprint: Uint8Array, items: readonly StoredItem[], summary: CollectionInventorySummary) {
    this.#key = key;
    this.#fingerprint = fingerprint;
    this.#items = items;
    this.#summary = Object.freeze(summary);
  }
  get summary(): CollectionInventorySummary { return this.#summary; }
  toString(): string { return 'Private collection inventory (key and input omitted).'; }
  toJSON(): never { throw new Error('Private collection inventories cannot be serialized.'); }

  /** Exact request bytes for the configured authenticated server; not a resume file. */
  serializePreflight(): string {
    if (this.#closed) throw closed();
    return requestPrefix(this.#summary.itemCount, hex(this.#key), hex(this.#fingerprint), this.#summary.contentDigest) +
      this.#items.map(item => itemPrefix(item.position, item.mac) + item.text + '}').join(',') + ']}';
  }
  /** Clear owned byte buffers and drop references. Runtime/HTTP copies are not erasable. */
  close(): void {
    this.#closed = true;
    this.#key.fill(0);
    this.#fingerprint.fill(0);
    this.#items = [];
  }

  /** Every call generates a new random key, including for byte-identical input. */
  static async freeze(sources: readonly Uint8Array[], items: readonly CollectionInventoryItem[], signal?: AbortSignal): Promise<FrozenCollectionInventory> {
    checkCancellation(signal);
    if (!Array.isArray(sources) || !Array.isArray(items)) throw invalid();
    if (sources.length > browserCollectionLimits.sources || items.length > browserCollectionLimits.items) throw excessive();
    let sourceBytes = 0;
    for (const source of sources) {
      if (!validBytes(source)) throw invalid();
      sourceBytes += source.byteLength;
      if (sourceBytes > browserCollectionLimits.sourceBytes) throw excessive();
    }
    const positions: CollectionInventoryPosition[] = [];
    const identities = new Set<string>();
    let requestBytes = encoder.encode(requestPrefix(items.length, zeroHex, zeroHex, zeroHex) + ']}').byteLength;
    for (let index = 0; index < items.length; index++) {
      const item = items[index];
      if (!item || !validBytes(item.resourceJSON)) throw invalid();
      if (item.resourceJSON.byteLength < 1 || item.resourceJSON.byteLength > browserCollectionLimits.resourceBytes) throw excessive();
      const position = positionCopy(item, index, sources.length);
      if (identities.has(position.id)) throw invalid();
      identities.add(position.id);
      requestBytes += encoder.encode(itemPrefix(position, zeroHex)).byteLength + item.resourceJSON.byteLength + 1 + (index > 0 ? 1 : 0);
      if (requestBytes > browserCollectionLimits.requestBytes) throw excessive();
      positions.push(position);
    }
    // Copy only after cumulative bounds are established, before the first await.
    const ownedSources = sources.map(source => new Uint8Array(source));
    const resources = items.map(item => new Uint8Array(item.resourceJSON));
    const key = new Uint8Array(32);
    let fingerprint = new Uint8Array(0);
    const privateMACs: Uint8Array[] = [];
    const stored: StoredItem[] = [];
    let success = false;
    try {
      if (!globalThis.crypto?.subtle || !globalThis.crypto.getRandomValues) throw new Error('Secure Web Crypto is required to prepare collection imports.');
      globalThis.crypto.getRandomValues(key);
      const cryptoKey = await globalThis.crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
      checkCancellation(signal);
      const sourceFields: Uint8Array[] = [...number(ownedSources.length)];
      for (let index = 0; index < ownedSources.length; index++) {
        const source = ownedSources[index];
        const leaf = await mac(cryptoKey, 'cpra.collection.source-bytes.v1', [source], signal);
        privateMACs.push(leaf);
        sourceFields.push(...number(index + 1), ...field(encoder.encode(collectionSourceToken(index + 1))), ...number(source.byteLength), ...field(leaf));
        source.fill(0);
      }
      fingerprint = await mac(cryptoKey, 'cpra.collection.sources.v1', sourceFields, signal);
      const inventoryFields = [...number(resources.length), ...field(fingerprint)];
      for (let index = 0; index < resources.length; index++) {
        const raw = resources[index], position = positions[index];
        let text: string;
        try {
          text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(raw);
          const value: unknown = JSON.parse(text);
          if (value === null || typeof value !== 'object' || Array.isArray(value)) throw invalid();
          const resource = value as Record<string, unknown>;
          if (resource.metadata === null || typeof resource.metadata !== 'object' || Array.isArray(resource.metadata)) throw invalid();
          const metadata = resource.metadata as Record<string, unknown>;
          if (typeof resource.kind !== 'string' || typeof metadata.id !== 'string' || `${resource.kind}/${metadata.id}` !== position.id) throw invalid();
        } catch { throw invalid(); }
        const digest = await mac(cryptoKey, 'cpra.collection.item.v1', [...number(position.ordinal), ...field(encoder.encode(position.id)),
          ...field(encoder.encode(position.source.token)), ...number(position.source.document), ...number(position.source.item), ...field(raw)], signal);
        privateMACs.push(digest);
        inventoryFields.push(...number(position.ordinal), ...field(encoder.encode(position.id)), ...field(digest));
        stored.push({ position, text, mac: hex(digest) });
        raw.fill(0);
      }
      const digest = await mac(cryptoKey, 'cpra.collection.inventory.v1', inventoryFields, signal);
      privateMACs.push(digest);
      const result = new FrozenCollectionInventory(key, fingerprint, stored, { identityFormat: collectionIdentityFormat, contentDigest: hex(digest),
        itemCount: resources.length, sourceCount: ownedSources.length, sourceBytes, requestBytes });
      success = true;
      return result;
    } catch (error) {
      if (signal?.aborted) throw cancelled();
      if (error instanceof Error && [invalid().message, excessive().message].includes(error.message)) throw error;
      // Native errors are never wrapped: callers should not see input diagnostics.
      throw new Error('Collection identity preparation failed; secure Web Crypto is required.');
    } finally {
      for (const value of [...ownedSources, ...resources, ...privateMACs]) value.fill(0);
      if (!success) { key.fill(0); fingerprint.fill(0); stored.length = 0; }
    }
  }
}

/** Separate worker import quotas. The normalized buffer bound is an explicit
 * implementation safety limit, not a promise that source bytes equal heap use. */
export const workerCollectionLimits = Object.freeze({ sources: 1_000, sourceBytes: 64 << 20, normalizedBytes: 512 << 20, resourceBytes: 1 << 20, items: 10_000, pageItems: 100, uploadItems: 256, requestBytes: 4 << 20 });
export interface WorkerCollectionSummary {
  readonly identityFormat: typeof collectionIdentityFormat;
  readonly normalizationProfile?: typeof fileNormalizationProfile;
  readonly contentDigest: string;
  readonly itemCount: number;
  readonly sourceCount: number;
  readonly sourceBytes: number;
  readonly normalizedBytes: number;
  readonly preflightAvailable: boolean;
}
export interface WorkerCollectionItemSummary extends CollectionInventoryPosition { readonly bytes: number }
type WorkerItem = { position: CollectionInventoryPosition; raw: Uint8Array; digest?: Uint8Array };
const workerLimit = () => new Error('Collection preparation exceeds the configured browser import quota.');
const concatenate = (parts: readonly Uint8Array[], maximum: number): Uint8Array<ArrayBuffer> => {
  const size = parts.reduce((sum, part) => sum + part.byteLength, 0);
  if (size > maximum) throw workerLimit();
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) { bytes.set(part, offset); offset += part.byteLength; }
  return bytes;
};

/** Worker-only builder. addItem transfers ownership of raw bytes to this object.
 * Retains one normalized byte representation per resource and no plaintext file.
 * Keys/fingerprints never leave it except in explicit bounded request bodies.
 * Terminating its dedicated Worker is the cancellation boundary during parsing. */
export class WorkerCollectionInventory {
  #key: Uint8Array;
  #cryptoKey?: CryptoKey;
  #fingerprint?: Uint8Array;
  #sourceFields: Uint8Array[] = [];
  #items: WorkerItem[] = [];
  #identities = new Set<string>();
  #sources = 0;
  #sourceBytes = 0;
  #normalizedBytes = 0;
  #closed = false;
  #summary?: WorkerCollectionSummary;
  #preflightBytes = 0;
  readonly #expectedSources: number;
  readonly #normalizationProfile?: typeof fileNormalizationProfile;

  private constructor(key: Uint8Array, cryptoKey: CryptoKey, sources: number, profile?: typeof fileNormalizationProfile) {
    this.#key = key; this.#cryptoKey = cryptoKey; this.#expectedSources = sources;
    this.#normalizationProfile = profile;
    this.#sourceFields.push(...number(sources));
  }
  static async create(sources: number, profile?: typeof fileNormalizationProfile): Promise<WorkerCollectionInventory> {
    if (!integer(sources, workerCollectionLimits.sources)) throw workerLimit();
    if (profile !== undefined && profile !== fileNormalizationProfile) throw invalid();
    const key = new Uint8Array(32);
    try {
      globalThis.crypto.getRandomValues(key);
      const imported = await globalThis.crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
      return new WorkerCollectionInventory(key, imported, sources, profile);
    } catch { key.fill(0); throw new Error('Secure Web Crypto is required for collection preparation.'); }
  }
  toString(): string { return 'Private worker collection (input and identity key omitted).'; }
  toJSON(): never { throw new Error('Private worker collections cannot be serialized.'); }
  get itemCount(): number { return this.#items.length; }
  get normalizedBytes(): number { return this.#normalizedBytes; }
  get summary(): WorkerCollectionSummary { this.#requireFrozen(); return this.#summary!; }
  #requireOpen(): CryptoKey { if (this.#closed || !this.#cryptoKey) throw closed(); return this.#cryptoKey; }
  #requireFrozen(): CryptoKey { const key = this.#requireOpen(); if (!this.#summary) throw new Error('Collection preparation is incomplete.'); return key; }

  /** The caller retains the source buffer only until the shared parser returns. */
  async addSource(raw: Uint8Array): Promise<string> {
    const key = this.#requireOpen();
    if (this.#summary || !validBytes(raw) || this.#sources >= this.#expectedSources || raw.byteLength > workerCollectionLimits.sourceBytes - this.#sourceBytes) throw workerLimit();
    const ordinal = this.#sources + 1, token = collectionSourceToken(ordinal);
    const leaf = await mac(key, 'cpra.collection.source-bytes.v1', [raw], undefined, workerCollectionLimits.sourceBytes + 1024);
    if (this.#closed) { leaf.fill(0); throw closed(); }
    this.#sourceFields.push(...number(ordinal), ...field(encoder.encode(token)), ...number(raw.byteLength), ...field(leaf));
    this.#sources = ordinal; this.#sourceBytes += raw.byteLength;
    return token;
  }
  /** Takes ownership; the caller must never read or mutate raw after this call. */
  addItem(raw: Uint8Array, id: string, document: number, item: number): void {
    this.#requireOpen();
    if (this.#summary || this.#sources === 0 || !validBytes(raw) || raw.byteLength < 1 || raw.byteLength > workerCollectionLimits.resourceBytes ||
        this.#items.length >= workerCollectionLimits.items || raw.byteLength > workerCollectionLimits.normalizedBytes - this.#normalizedBytes) throw workerLimit();
    const position = positionCopy({ ordinal: this.#items.length + 1, id, resourceJSON: raw,
      source: { token: collectionSourceToken(this.#sources), document, item } }, this.#items.length, this.#expectedSources);
    if (this.#identities.has(id)) throw new Error('The collection contains duplicate resource identities.');
    // Parse identity only. Numeric tokens are never reconstructed or serialized.
    try {
      const resource = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(raw));
      if (!resource || Array.isArray(resource) || `${resource.kind}/${resource.metadata?.id}` !== id) throw invalid();
    } catch { throw invalid(); }
    this.#items.push({ position, raw }); this.#identities.add(id); this.#normalizedBytes += raw.byteLength;
  }
  async #digest(item: WorkerItem): Promise<Uint8Array<ArrayBuffer>> {
    const position = item.position;
    return mac(this.#requireOpen(), 'cpra.collection.item.v1', [...number(position.ordinal), ...field(encoder.encode(position.id)),
      ...field(encoder.encode(position.source.token)), ...number(position.source.document), ...number(position.source.item), ...field(item.raw)]);
  }
  async finish(): Promise<WorkerCollectionSummary> {
    const key = this.#requireOpen();
    if (this.#summary || this.#sources !== this.#expectedSources) throw new Error('Collection source preparation is incomplete.');
    this.#fingerprint = await mac(key, 'cpra.collection.sources.v1', this.#sourceFields);
    const fields = [...number(this.#items.length), ...field(this.#fingerprint)];
    let preflightBytes = encoder.encode(requestPrefix(this.#items.length, zeroHex, zeroHex, zeroHex) + ']}').byteLength;
    for (const item of this.#items) {
      item.digest = await this.#digest(item);
      fields.push(...number(item.position.ordinal), ...field(encoder.encode(item.position.id)), ...field(item.digest));
      preflightBytes += encoder.encode(itemPrefix(item.position, zeroHex)).byteLength + item.raw.byteLength + 1 + (item.position.ordinal > 1 ? 1 : 0);
    }
    const digest = await mac(key, 'cpra.collection.inventory.v1', fields);
    if (this.#closed) { digest.fill(0); throw closed(); }
    this.#summary = Object.freeze({ identityFormat: collectionIdentityFormat, contentDigest: hex(digest), itemCount: this.#items.length,
      ...(this.#normalizationProfile ? { normalizationProfile: this.#normalizationProfile } : {}),
      sourceCount: this.#sources, sourceBytes: this.#sourceBytes, normalizedBytes: this.#normalizedBytes, preflightAvailable: preflightBytes <= workerCollectionLimits.requestBytes });
    digest.fill(0); this.#preflightBytes = preflightBytes;
    for (const bytes of this.#sourceFields) bytes.fill(0);
    this.#sourceFields = [];
    return this.#summary;
  }
  page(offset: number, limit: number = workerCollectionLimits.pageItems): WorkerCollectionItemSummary[] {
    this.#requireFrozen();
    if (!Number.isSafeInteger(offset) || offset < 0 || offset > this.#items.length || !integer(limit, workerCollectionLimits.pageItems)) throw invalid();
    return this.#items.slice(offset, offset + limit).map(({ position, raw }) => ({ ...position, source: { ...position.source }, bytes: raw.byteLength }));
  }
  /** Explicit write-only operation-creation JSON; contains the private key. */
  createBody(): Uint8Array<ArrayBuffer> {
    this.#requireFrozen();
    return encoder.encode(JSON.stringify({ identityFormat: collectionIdentityFormat, identityKey: hex(this.#key), sourceFingerprint: hex(this.#fingerprint!),
      ...(this.#normalizationProfile ? { normalizationProfile: this.#normalizationProfile } : {}),
      contentDigest: this.#summary!.contentDigest, itemCount: this.#items.length }));
  }
  async #itemParts(item: WorkerItem): Promise<Uint8Array[]> {
    const actual = await this.#digest(item);
    const matches = hex(actual) === hex(item.digest!); actual.fill(0);
    if (!matches) throw new Error('The frozen collection changed; prepare the original input again.');
    return [encoder.encode(itemPrefix(item.position, hex(item.digest!))), item.raw, encoder.encode('}')];
  }
  async preflightBody(): Promise<Uint8Array<ArrayBuffer>> {
    this.#requireFrozen();
    if (this.#preflightBytes > workerCollectionLimits.requestBytes) throw new Error('This collection requires bounded staged uploads; ephemeral preflight is limited to 4 MiB.');
    const parts: Uint8Array[] = [encoder.encode(requestPrefix(this.#items.length, hex(this.#key), hex(this.#fingerprint!), this.#summary!.contentDigest))];
    for (const item of this.#items) { if (item.position.ordinal > 1) parts.push(encoder.encode(',')); parts.push(...await this.#itemParts(item)); }
    this.#requireFrozen(); parts.push(encoder.encode(']}'));
    return concatenate(parts, workerCollectionLimits.requestBytes);
  }
  async uploadBody(offset: number): Promise<{ bytes: Uint8Array<ArrayBuffer>; nextOffset: number }> {
    this.#requireFrozen();
    if (!Number.isSafeInteger(offset) || offset < 0 || offset >= this.#items.length) throw invalid();
    const parts: Uint8Array[] = [encoder.encode('{"items":[')];
    let size = parts[0].byteLength + 2, next = offset;
    while (next < this.#items.length && next - offset < workerCollectionLimits.uploadItems) {
      const item = this.#items[next];
      const estimated = encoder.encode(itemPrefix(item.position, zeroHex)).byteLength + item.raw.byteLength + 1 + (next > offset ? 1 : 0);
      if (size + estimated > workerCollectionLimits.requestBytes) break;
      if (next > offset) parts.push(encoder.encode(','));
      parts.push(...await this.#itemParts(item)); size += estimated; next++;
    }
    this.#requireFrozen(); parts.push(encoder.encode(']}'));
    return { bytes: concatenate(parts, workerCollectionLimits.requestBytes), nextOffset: next };
  }
  close(): void {
    this.#closed = true; this.#key.fill(0); this.#cryptoKey = undefined; this.#fingerprint?.fill(0);
    for (const bytes of this.#sourceFields) bytes.fill(0);
    for (const item of this.#items) { item.raw.fill(0); item.digest?.fill(0); }
    this.#sourceFields = []; this.#items = []; this.#identities.clear();
  }
}
