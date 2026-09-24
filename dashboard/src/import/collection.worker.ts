import './generated/wasm_exec.js';
import parserURL from './generated/collection-parser.wasm?url';
import parserBuild from './generated/BUILD.json';
import { fileNormalizationProfile, WorkerCollectionInventory, workerCollectionLimits } from '../api/collectionInventory';
import { orderedCollectionSources, type SelectedCollectionSource } from './files';
import { CollectionImportError, type ImportLocation, type WorkerReply, type WorkerRequest } from './protocol';

type ParserResult = { valid: boolean; document?: number; item?: number };
interface GoRuntime { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> }
const scope = globalThis as unknown as {
  Go: new () => GoRuntime;
  cpraCollectionNormalizationProfile: string;
  cpraDecodeCollection(bytes: Uint8Array, visit: (id: string, document: number, item: number, raw: string) => boolean): ParserResult;
  onmessage: (event: MessageEvent<WorkerRequest>) => void;
  postMessage(value: WorkerReply, transfer?: Transferable[]): void;
  close(): void;
};
const send = (value: WorkerReply, transfer?: Transferable[]) => scope.postMessage(value, transfer ?? []);
let inventory: WorkerCollectionInventory | undefined;
let preparing = false, frozen = false, busy = false;

async function initialize(): Promise<void> {
  const target = new URL(parserURL, import.meta.url);
  if (target.origin !== location.origin) throw new CollectionImportError('unavailable');
  const response = await fetch(target, { credentials: 'omit', redirect: 'error', cache: 'no-store' });
  if (!response.ok || response.redirected || !response.body) throw new CollectionImportError('unavailable');
  const maximum = parserBuild.files['collection-parser.wasm'].bytes;
  const reader = response.body.getReader(), pieces: Uint8Array[] = [];
  let count = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      count += value.byteLength;
      if (count > maximum) throw new CollectionImportError('unavailable');
      pieces.push(value);
    }
  } finally { await reader.cancel().catch(() => undefined); reader.releaseLock(); }
  if (count !== maximum) throw new CollectionImportError('unavailable');
  const bytes = new Uint8Array(count);
  let offset = 0;
  for (const part of pieces) { bytes.set(part, offset); offset += part.byteLength; part.fill(0); }
  try {
    const actual = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes));
    const digest = Array.from(actual, value => value.toString(16).padStart(2, '0')).join('');
    if (digest !== parserBuild.files['collection-parser.wasm'].sha256) throw new CollectionImportError('unavailable');
    const go = new scope.Go();
    const result = await WebAssembly.instantiate(bytes, go.importObject);
    void go.run(result.instance).catch(() => { inventory?.close(); send({ id: 0, kind: 'error', code: 'unavailable' }); scope.close(); });
    if (typeof scope.cpraDecodeCollection !== 'function' || scope.cpraCollectionNormalizationProfile !== fileNormalizationProfile) throw new CollectionImportError('unavailable');
  } finally { bytes.fill(0); }
}

async function prepare(sources: SelectedCollectionSource[]): Promise<void> {
  const ordered = orderedCollectionSources(sources);
  inventory = await WorkerCollectionInventory.create(ordered.length, fileNormalizationProfile);
  for (let index = 0; index < ordered.length; index++) {
    const file = ordered[index].file;
    const raw = new Uint8Array(await file.slice(0, workerCollectionLimits.sourceBytes + 1).arrayBuffer());
    try {
      if (raw.byteLength !== file.size) throw new CollectionImportError('quota', { source: index + 1 });
      await inventory.addSource(raw);
      let callbackError: CollectionImportError | undefined;
      send({ kind: 'progress', phase: 'parsing', sources: index, items: inventory.itemCount });
      const parsed = scope.cpraDecodeCollection(raw, (id, document, item, text) => {
        let encoded: Uint8Array | undefined;
        try {
          encoded = new TextEncoder().encode(text);
          if (encoded.byteLength > workerCollectionLimits.resourceBytes) throw new CollectionImportError('quota');
          inventory!.addItem(encoded, id, document, item);
          encoded = undefined; // The inventory now owns these bytes.
          return true;
        } catch (error) {
          encoded?.fill(0);
          const duplicate = error instanceof Error && error.message === 'The collection contains duplicate resource identities.';
          const quota = error instanceof Error && error.message === 'Collection preparation exceeds the configured browser import quota.';
          callbackError = new CollectionImportError(duplicate ? 'duplicate' : quota ? 'quota' : 'invalid', { source: index + 1, document, item });
          return false;
        }
      });
      if (callbackError) throw callbackError;
      if (!parsed.valid) {
        const location: ImportLocation = { source: index + 1 };
        if (Number.isSafeInteger(parsed.document) && parsed.document! > 0) location.document = parsed.document;
        if (Number.isSafeInteger(parsed.item) && parsed.item! > 0) location.item = parsed.item;
        throw new CollectionImportError('invalid', location);
      }
      send({ kind: 'progress', phase: 'parsed', sources: index + 1, items: inventory.itemCount });
    } finally { raw.fill(0); }
  }
  send({ kind: 'progress', phase: 'identity', sources: ordered.length, items: inventory.itemCount });
  await inventory.finish();
  frozen = true;
}

const initialized = initialize();
void initialized.then(() => send({ kind: 'ready' })).catch(() => { send({ id: 0, kind: 'error', code: 'unavailable' }); scope.close(); });
scope.onmessage = async ({ data }) => {
  if (!data || !Number.isSafeInteger(data.id) || data.id < 1 || busy) { send({ id: data?.id ?? 0, kind: 'error', code: 'invalid' }); return; }
  busy = true;
  try {
    await initialized;
    if (data.kind === 'prepare') {
      if (preparing || frozen) throw new CollectionImportError('closed');
      preparing = true;
      await prepare(data.sources);
      send({ id: data.id, kind: 'prepared', summary: inventory!.summary });
    } else {
      if (!frozen || !inventory) throw new CollectionImportError('closed');
      if (data.kind === 'page') send({ id: data.id, kind: 'page', items: inventory.page(data.offset, data.limit) });
      else {
        let bytes: Uint8Array<ArrayBuffer>, nextOffset: number | undefined;
        if (data.kind === 'create') bytes = inventory.createBody();
        else if (data.kind === 'preflight') bytes = await inventory.preflightBody();
        else if (data.kind === 'upload') { const chunk = await inventory.uploadBody(data.offset); bytes = chunk.bytes; nextOffset = chunk.nextOffset; }
        else throw new CollectionImportError('invalid');
        send({ id: data.id, kind: 'body', body: bytes.buffer, ...(nextOffset === undefined ? {} : { nextOffset }) }, [bytes.buffer]);
      }
    }
  } catch (error) {
    const reason = error instanceof CollectionImportError ? error : new CollectionImportError('invalid');
    inventory?.close(); inventory = undefined; frozen = false;
    send({ id: data.id, kind: 'error', code: reason.code, ...(reason.location ? { location: reason.location } : {}) });
  } finally { busy = false; }
};
