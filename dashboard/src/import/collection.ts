import { workerCollectionLimits, type WorkerCollectionItemSummary, type WorkerCollectionSummary } from '../api/collectionInventory';
import { collectionFileSources } from './files';
import { CollectionImportError, type WorkerReply, type WorkerRequest } from './protocol';

export { CollectionImportError } from './protocol';
export { workerCollectionLimits } from '../api/collectionInventory';
export interface ImportProgress { readonly phase: 'parsing' | 'parsed' | 'identity'; readonly sources: number; readonly items: number }
export interface PrepareCollectionOptions { signal?: AbortSignal; onProgress?: (progress: ImportProgress) => void }
type Command = WorkerRequest extends infer R ? R extends WorkerRequest ? Omit<R, 'id'> : never : never;

/** Explicit one-use HTTP body; may contain credentials and the identity key.
 * Pass straight to authenticated transport, then clear/drop its owned buffer. */
export class PrivateCollectionBody {
  #bytes?: Uint8Array<ArrayBuffer>;
  readonly nextOffset?: number;
  constructor(bytes: ArrayBuffer, nextOffset?: number) {
    if (!(bytes instanceof ArrayBuffer) || bytes.byteLength > workerCollectionLimits.requestBytes) throw new CollectionImportError('quota');
    this.#bytes = new Uint8Array(bytes); this.nextOffset = nextOffset;
  }
  take(): Uint8Array<ArrayBuffer> {
    if (!this.#bytes) throw new CollectionImportError('closed');
    const bytes = this.#bytes; this.#bytes = undefined; return bytes;
  }
  close(): void { this.#bytes?.fill(0); this.#bytes = undefined; }
  toString(): string { return 'Private collection request (contents omitted).'; }
  toJSON(): never { throw new Error('Private collection requests cannot be serialized.'); }
}

/** One dedicated worker per selection. Close on cancellation, navigation,
 * identity change or completion. This foundation creates a fresh identity per
 * selection. Approved server-assisted reselection/resume remains pending; no key
 * persistence is introduced here. No API requests, activation or storage. */
export class BrowserCollection {
  #worker?: Worker;
  #pending?: { id: number; resolve: (reply: WorkerReply) => void; reject: (error: CollectionImportError) => void; timer: ReturnType<typeof setTimeout> };
  #nextID = 0;
  #summary?: WorkerCollectionSummary;
  #sourceNames: string[] = [];
  #resolveReady!: () => void;
  #rejectReady!: (error: CollectionImportError) => void;
  #ready: Promise<void>;
  #readyTimer: ReturnType<typeof setTimeout>;
  #abort?: () => void;
  readonly #options: PrepareCollectionOptions;
  private constructor(options: PrepareCollectionOptions) {
    this.#options = options;
    this.#ready = new Promise((resolve, reject) => { this.#resolveReady = resolve; this.#rejectReady = reject; });
    this.#readyTimer = setTimeout(() => this.#fail(new CollectionImportError('unavailable')), 30_000);
    try { this.#worker = new Worker(new URL('./collection.worker.ts', import.meta.url), { type: 'module', name: 'cpra-private-collection' }); }
    catch { this.#fail(new CollectionImportError('unavailable')); return; }
    this.#worker.onerror = event => { event.preventDefault(); this.#fail(new CollectionImportError('unavailable')); };
    this.#worker.onmessageerror = () => this.#fail(new CollectionImportError('unavailable'));
    this.#worker.onmessage = ({ data }: MessageEvent<WorkerReply>) => {
      if (!this.#worker || !data || typeof data !== 'object') return;
      if (data.kind === 'ready') { clearTimeout(this.#readyTimer); this.#resolveReady(); return; }
      if (data.kind === 'progress') {
        if (!['parsing', 'parsed', 'identity'].includes(data.phase) || !Number.isSafeInteger(data.sources) || data.sources < 0 || data.sources > workerCollectionLimits.sources || !Number.isSafeInteger(data.items) || data.items < 0 || data.items > workerCollectionLimits.items) { this.#fail(new CollectionImportError('invalid')); return; }
        try { this.#options.onProgress?.(Object.freeze({ phase: data.phase, sources: data.sources, items: data.items })); }
        catch { this.#fail(new CollectionImportError('cancelled')); }
        return;
      }
      if (data.kind === 'error') {
        const codes = ['invalid', 'quota', 'duplicate', 'unavailable', 'closed', 'cancelled'];
        this.#fail(new CollectionImportError(codes.includes(data.code) ? data.code : 'invalid', data.location)); return;
      }
      const pending = this.#pending;
      if (!pending || data.id !== pending.id) { this.#fail(new CollectionImportError('invalid')); return; }
      clearTimeout(pending.timer); this.#pending = undefined; pending.resolve(data);
    };
    this.#abort = () => this.#fail(new CollectionImportError('cancelled'));
    options.signal?.addEventListener('abort', this.#abort, { once: true });
    if (options.signal?.aborted) this.#abort();
  }
  static async prepare(files: readonly File[], options: PrepareCollectionOptions = {}): Promise<BrowserCollection> {
    if (options.signal?.aborted) throw new CollectionImportError('cancelled');
    const ordered = collectionFileSources(files), collection = new BrowserCollection(options);
    try {
      await collection.#ready;
      const response = await collection.#request({ kind: 'prepare', sources: ordered });
      if (response.kind !== 'prepared') throw new CollectionImportError('invalid');
      if (!collection.#worker) throw new CollectionImportError('closed');
      collection.#summary = Object.freeze({ ...response.summary });
      collection.#sourceNames = ordered.map(source => source.name);
      return collection;
    } catch (error) { collection.close(); throw error instanceof CollectionImportError ? error : new CollectionImportError('unavailable'); }
  }
  get summary(): WorkerCollectionSummary {
    if (!this.#summary || !this.#worker) throw new CollectionImportError('closed');
    return this.#summary;
  }
  /** Client-local selected name; never attach it to wire requests or diagnostics. */
  sourceName(ordinal: number): string {
    if (!this.#worker) throw new CollectionImportError('closed');
    if (!Number.isSafeInteger(ordinal) || ordinal < 1 || ordinal > this.#sourceNames.length) throw new CollectionImportError('invalid');
    return this.#sourceNames[ordinal - 1];
  }
  toString(): string { return 'Private browser collection (input and identity omitted).'; }
  toJSON(): never { throw new Error('Private browser collections cannot be serialized.'); }
  async #request(command: Command): Promise<WorkerReply> {
    if (!this.#worker) throw new CollectionImportError('closed');
    if (this.#pending) throw new CollectionImportError('invalid');
    const id = ++this.#nextID;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => this.#fail(new CollectionImportError('unavailable')), 120_000);
      this.#pending = { id, resolve, reject, timer };
      try { this.#worker!.postMessage({ ...command, id }); }
      catch { this.#fail(new CollectionImportError('unavailable')); }
    });
  }
  async page(offset = 0, limit: number = workerCollectionLimits.pageItems): Promise<readonly WorkerCollectionItemSummary[]> {
    const summary = this.summary;
    if (!Number.isSafeInteger(offset) || offset < 0 || offset > summary.itemCount || !Number.isSafeInteger(limit) || limit < 1 || limit > workerCollectionLimits.pageItems) throw new CollectionImportError('invalid');
    const response = await this.#request({ kind: 'page', offset, limit });
    if (response.kind !== 'page' || response.items.length > limit) throw new CollectionImportError('invalid');
    return response.items;
  }
  async #body(command: Command): Promise<PrivateCollectionBody> {
    const response = await this.#request(command);
    if (response.kind !== 'body') throw new CollectionImportError('invalid');
    if (!this.#worker) { new Uint8Array(response.body).fill(0); throw new CollectionImportError('closed'); }
    return new PrivateCollectionBody(response.body, response.nextOffset);
  }
  createBody(): Promise<PrivateCollectionBody> { return this.#body({ kind: 'create' }); }
  preflightBody(): Promise<PrivateCollectionBody> {
    if (!this.summary.preflightAvailable) return Promise.reject(new CollectionImportError('quota'));
    return this.#body({ kind: 'preflight' });
  }
  uploadBody(offset: number): Promise<PrivateCollectionBody> {
    if (!Number.isSafeInteger(offset) || offset < 0 || offset >= this.summary.itemCount) return Promise.reject(new CollectionImportError('invalid'));
    return this.#body({ kind: 'upload', offset });
  }
  #fail(error: CollectionImportError): void {
    this.#rejectReady(error);
    const pending = this.#pending; this.#pending = undefined;
    if (pending) { clearTimeout(pending.timer); pending.reject(error); }
    this.close();
  }
  close(): void {
    clearTimeout(this.#readyTimer);
    this.#worker?.terminate(); this.#worker = undefined; this.#summary = undefined; this.#sourceNames = [];
    if (this.#abort) this.#options.signal?.removeEventListener('abort', this.#abort);
    const pending = this.#pending; this.#pending = undefined;
    if (pending) { clearTimeout(pending.timer); pending.reject(new CollectionImportError('closed')); }
    this.#rejectReady(new CollectionImportError('closed'));
  }
}
