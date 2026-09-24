import { operationContracts, type AccessInfo, type Problem } from './generated';
import { allocationNotSubmitted, decodeErrorJSON } from './admission';
export type { AccessInfo, Problem } from './generated';

/** Only this tab's live session owns the token. It is never placed in React/query state. */

export interface SessionState {
  phase: 'discovering' | 'legacy' | 'signed-out' | 'signing-in' | 'authenticated' | 'unavailable';
  epoch: number;
  access?: AccessInfo;
  message?: string;
}

export class ManagementError extends Error {
  constructor(
    public readonly reason: 'http' | 'not-admitted' | 'unconfirmed' | 'cancelled' | 'session-changed' | 'unavailable' | 'invalid' | 'too-large' | 'forbidden',
    message: string,
    public readonly status?: number,
    public readonly problem?: Problem,
    public readonly responseIssue?: 'too-large' | 'invalid',
    public readonly response?: Omit<APIResponse<unknown>, 'data'>,
  ) {
    super(message);
    this.name = 'ManagementError';
  }
}

export interface APIResponse<T> {
  data: T;
  status: number;
  requestID: string;
  operationID: string;
  resourceVersion: string;
  retryAfterMs: number;
}

export interface ReadOptions { signal?: AbortSignal; /** May only lower the ordinary decoded-response ceiling. */ maxResponseBytes?: number }
export type MutationOptions = ReadOptions & {
  /** Exact OpenAPI operation ID granted by /self. A role alone grants nothing. */
  operation: string;
  body?: unknown;
} & (
  { method: 'POST'; resourceVersion?: string } |
  { method: 'PUT' | 'PATCH' | 'DELETE'; resourceVersion: string }
);

type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
type RequestOptions = ReadOptions & { method?: string; body?: unknown; rawBody?: Uint8Array<ArrayBuffer>; rawContentType?: 'application/octet-stream'; maxErrorBytes?: number; readOnlyPost?: boolean; resourceVersion?: string; createOnly?: boolean; token?: string; legacy?: boolean; discovery?: boolean; expireOn401?: boolean; metrics?: boolean };

export type CollectionRequestOperation = 'PreflightCollection' | 'PrepareCollection' | 'CreateOperation' | 'UploadOperation' | 'ValidateOperation' | 'ActivateOperation' | 'CancelOperation';
const collectionRequests = new Set<string>(['PreflightCollection', 'PrepareCollection', 'CreateOperation', 'UploadOperation', 'ValidateOperation', 'ActivateOperation', 'CancelOperation']);

export type ReselectionRequestOperation = 'CreateCollectionReselection' | 'GetCollectionReselection' | 'UploadCollectionReselectionSource' | 'VerifyCollectionReselection' | 'ResumeCollectionReselection' | 'DiscardCollectionReselection';
const reselectionRequests = new Set<string>(['CreateCollectionReselection', 'GetCollectionReselection', 'UploadCollectionReselectionSource', 'VerifyCollectionReselection', 'ResumeCollectionReselection', 'DiscardCollectionReselection']);
export interface ReselectionRequestOptions {
  id: string;
  attempt?: string;
  body?: Uint8Array<ArrayBuffer>;
  source?: number;
  offset?: number;
  end?: boolean;
  signal?: AbortSignal;
}

const MAX_RESPONSE = 64 * 1024 * 1024;
const MAX_ERROR = 64 * 1024;
const REQUEST_TIMEOUT = 10_000;
/** Fixed decoded-response ceiling for the on-demand browser metrics viewer. */
export const PROMETHEUS_MAX_BYTES = 1 << 20;

function strongETag(version: string): string {
  const value = version.startsWith('"') && version.endsWith('"') ? version.slice(1, -1) : version;
  if (!value || !/^[\x21\x23-\x7e]+$/.test(value) || value.includes(',') || value.includes('*') || value.startsWith('W/')) {
    throw new ManagementError('invalid', 'A single strong resource version is required.');
  }
  return `"${value}"`;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function accessInfo(value: unknown): AccessInfo {
  if (!isObject(value) || typeof value.principalId !== 'string' || !value.principalId ||
    (value.role !== 'reader' && value.role !== 'operator') || !Array.isArray(value.permissions) ||
    !value.permissions.every(p => typeof p === 'string')) {
    throw new ManagementError('invalid', 'The server returned an unsupported access response.');
  }
  return { principalId: value.principalId, role: value.role, permissions: [...value.permissions] };
}

function problemDetails(value: unknown): Problem | undefined {
  if (!isObject(value)) return undefined;
  const problem: Problem = {};
  for (const key of ['type', 'title', 'detail', 'code', 'requestID', 'operationID'] as const) {
    if (typeof value[key] === 'string') problem[key] = value[key];
  }
  if (typeof value.status === 'number') problem.status = value.status;
  if (Array.isArray(value.errors)) problem.errors = value.errors.filter(isObject)
    .filter(e => typeof e.field === 'string' && typeof e.message === 'string')
    .map(e => ({ field: e.field as string, message: e.message as string, ...(typeof e.reason === 'string' ? { reason: e.reason } : {}) }));
  return problem;
}

async function readJSON(response: Response, limit: number, signal: AbortSignal, strictError = false): Promise<unknown> {
  if (response.status === 204) return undefined;
  const declared = response.headers.get('Content-Length');
  if (declared !== null && /^\d+$/.test(declared) && Number(declared) > limit) {
    await response.body?.cancel();
    throw new ManagementError('too-large', 'The response exceeds the supported size.');
  }
  if (!response.body) throw new ManagementError('invalid', 'The server returned an empty response.');
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener('abort', cancel, { once: true });
  try {
    while (true) {
      if (signal.aborted) throw new ManagementError('cancelled', 'The request was cancelled.');
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) throw new ManagementError('too-large', 'The response exceeds the supported size.');
      chunks.push(value);
    }
    if (signal.aborted) throw new ManagementError('cancelled', 'The request was cancelled.');
    const bytes = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
    try { return strictError ? decodeErrorJSON(bytes) : JSON.parse(new TextDecoder().decode(bytes)) as unknown; }
    catch { throw new ManagementError('invalid', 'The server returned an invalid response.'); }
  } finally {
    signal.removeEventListener('abort', cancel);
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}

async function readMetricsText(response: Response, signal: AbortSignal): Promise<string> {
  const media = (response.headers.get('Content-Type') ?? '').split(';').map(part => part.trim().toLowerCase());
  if (!['text/plain', 'application/openmetrics-text'].includes(media[0]) || media.some(part => part.startsWith('charset=') && !/^charset=(?:utf-8|"utf-8")$/.test(part))) {
    await response.body?.cancel().catch(() => undefined);
    throw new ManagementError('invalid', 'The metrics endpoint did not return UTF-8 exposition text.');
  }
  const declared = response.headers.get('Content-Length');
  if (declared !== null && /^\d+$/.test(declared) && Number(declared) > PROMETHEUS_MAX_BYTES) {
    await response.body?.cancel().catch(() => undefined);
    throw new ManagementError('too-large', 'Metrics exceed the 1 MiB browser display limit.');
  }
  if (!response.body) throw new ManagementError('invalid', 'The metrics endpoint returned no response body.');
  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
  let size = 0, text = '';
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener('abort', cancel, { once: true });
  try {
    while (true) {
      if (signal.aborted) throw new ManagementError('cancelled', 'The metrics request was cancelled.');
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > PROMETHEUS_MAX_BYTES) throw new ManagementError('too-large', 'Metrics exceed the 1 MiB browser display limit.');
      try { text += decoder.decode(value, { stream: true }); }
      catch { throw new ManagementError('invalid', 'The metrics endpoint returned invalid UTF-8 text.'); }
    }
    if (signal.aborted) throw new ManagementError('cancelled', 'The metrics request was cancelled.');
    try { text += decoder.decode(); }
    catch { throw new ManagementError('invalid', 'The metrics endpoint returned invalid UTF-8 text.'); }
    if (/^\s*</.test(text)) throw new ManagementError('invalid', 'The metrics endpoint returned a document instead of exposition text.');
    return text;
  } finally {
    signal.removeEventListener('abort', cancel);
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}

export class DashboardSession {
  private state: SessionState = { phase: 'discovering', epoch: 0 };
  #token?: string;
  private listeners = new Set<() => void>();
  private resets = new Set<() => void>();
  private pending = new Set<AbortController>();
  private discovered = false;
  readonly origin: string;

  constructor(private readonly options: { origin?: string; fetch?: Fetcher; maxResponseBytes?: number; timeoutMs?: number } = {}) {
    this.origin = new URL(options.origin ?? window.location.origin).origin;
  }

  getSnapshot = (): SessionState => this.state;
  subscribe = (listener: () => void): (() => void) => { this.listeners.add(listener); return () => this.listeners.delete(listener); };
  onReset = (listener: () => void): (() => void) => { this.resets.add(listener); return () => this.resets.delete(listener); };
  can = (operation: string): boolean => this.state.phase === 'authenticated' && this.state.access?.permissions.includes(operation) === true;

  private publish(state: SessionState) {
    this.state = state;
    this.listeners.forEach(listener => listener());
  }

  private reset(phase: SessionState['phase'], message?: string): number {
    this.#token = undefined;
    this.pending.forEach(controller => controller.abort());
    this.pending.clear();
    const epoch = this.state.epoch + 1;
    // Query data and mutation variables are removed before subscribers see a new identity.
    this.resets.forEach(listener => listener());
    this.publish({ phase, epoch, message });
    return epoch;
  }

  signOut = () => { this.reset('signed-out'); };
  showSignIn = () => { this.reset('signed-out'); };

  async discover(): Promise<void> {
    if (this.discovered) return;
    this.discovered = true;
    const epoch = this.state.epoch;
    try {
      // Existing v1 servers protect every path with browser Basic auth, including
      // unknown v2 paths. Preserve that read-only credential for this same-origin
      // GET so the server can return its actual 404 instead of a misleading 401.
      await this.request('/api/v2/self', { discovery: true, expireOn401: false });
      if (this.state.epoch === epoch) this.publish({ phase: 'signed-out', epoch });
    } catch (error) {
      if (this.state.epoch !== epoch) return;
      const status = error instanceof ManagementError ? error.status : undefined;
      this.publish({ phase: status === 404 ? 'legacy' : status === 401 || status === 403 ? 'signed-out' : 'unavailable', epoch,
        ...(status === 404 || status === 401 || status === 403 ? {} : { message: 'Unable to discover server access. Retry when the server is available.' }) });
    }
  }

  retryDiscovery = async () => {
    this.reset('discovering');
    this.discovered = false;
    await this.discover();
  };

  async signIn(token: string): Promise<void> {
    const epoch = this.reset('signing-in');
    try {
      if (!token || token.length > 8192 || /\s/.test(token)) throw new ManagementError('invalid', 'Enter a valid bearer token.');
      if (new URL(this.origin).protocol !== 'https:') throw new ManagementError('invalid', 'Bearer sign-in requires HTTPS. Use the configured TLS endpoint.');
      const response = await this.request<unknown>('/api/v2/self', { token, expireOn401: false });
      const access = accessInfo(response.data);
      if (this.state.epoch !== epoch) return;
      this.#token = token;
      this.publish({ phase: 'authenticated', epoch, access });
    } catch (error) {
      if (this.state.epoch !== epoch) return;
      const message = error instanceof ManagementError && error.reason === 'invalid' ? error.message
        : error instanceof ManagementError && error.status === 404 ? 'This server does not support management access.'
          : 'Sign-in failed. Check your token and server connection.';
      this.publish({ phase: 'signed-out', epoch, message });
    }
  }

  async get<T>(path: string, options: ReadOptions = {}): Promise<APIResponse<T>> {
    if (!this.#token || this.state.phase !== 'authenticated') throw new ManagementError('forbidden', 'Sign in to access management data.');
    return this.request<T>(path, { ...options, token: this.#token });
  }

  /** Fixed same-origin read; it cannot carry a caller-selected URL or bearer query. */
  async prometheus(options: ReadOptions = {}): Promise<string> {
    const legacy = this.state.phase === 'legacy';
    // The server maps /metrics to GetMetrics in AuthorizeObservation.
    if (!legacy && (!this.#token || !this.can('GetMetrics'))) throw new ManagementError('forbidden', 'Your identity cannot read metrics.');
    return (await this.request<string>('/metrics', { ...options, metrics: true, legacy, token: legacy ? undefined : this.#token })).data;
  }

  async mutate<T>(path: string, options: MutationOptions): Promise<APIResponse<T>> {
    if (this.state.access?.role !== 'operator' || !this.can(options.operation)) throw new ManagementError('forbidden', 'This operation is not permitted for your identity.');
    if (options.method !== 'POST' && !options.resourceVersion) throw new ManagementError('invalid', 'The observed resource version is required.');
    const contract = (operationContracts as Record<string, { createOnly: boolean }>)[options.operation];
    if (contract?.createOnly && options.resourceVersion !== undefined) throw new ManagementError('invalid', 'Creation cannot also replace an observed version.');
    return this.request<T>(path, { ...options, createOnly: contract?.createOnly, token: this.#token });
  }

  /** Takes ownership of exact worker-produced JSON bytes. Never decode/re-encode
   * their MAC-bound resource spans. All exit paths clear the transferred buffer.
   * Upload is the generated non-CAS PUT contract; ordinary resource PUT/PATCH/
   * DELETE continue to require their observed version through mutate(). */
  async collectionRequest<T>(operation: CollectionRequestOperation, body?: Uint8Array<ArrayBuffer>, id?: string, signal?: AbortSignal): Promise<APIResponse<T>> {
    try {
      if (!collectionRequests.has(operation) || this.state.access?.role !== 'operator' || !this.can(operation)) throw new ManagementError('forbidden', 'This collection operation is not permitted for your identity.');
      const contract = operationContracts[operation];
      if (!contract || contract.cas) throw new ManagementError('unavailable', 'This collection contract is unavailable.');
      const needsID = contract.path.includes('{id}');
      if (needsID && (typeof id !== 'string' || !id || id.length > 256 || id === '.' || id === '..' || /[\\/?#%\x00-\x1f]/.test(id))) throw new ManagementError('invalid', 'The original operation ID is required.');
      if (!needsID && id !== undefined) throw new ManagementError('invalid', 'This request does not take an operation ID.');
      const needsBody = ['PreflightCollection', 'PrepareCollection', 'CreateOperation', 'UploadOperation'].includes(operation);
      if (needsBody !== (body !== undefined) || (body !== undefined && (!(body instanceof Uint8Array) || !(body.buffer instanceof ArrayBuffer) || body.byteLength === 0 || body.byteLength > 4 << 20))) throw new ManagementError('invalid', 'A bounded private collection request is required.');
      return await this.request<T>(needsID ? contract.path.replace('{id}', encodeURIComponent(id!)) : contract.path, {
        method: contract.method, rawBody: body, readOnlyPost: operation === 'PreflightCollection', token: this.#token, signal,
      });
    } finally { body?.fill(0); }
  }

  /** Owns and clears each private body, including explicit empty source ends.
   * Routes and media are fixed by operation; there is no filename, source URL,
   * multipart wrapper or automatic retry. Ordinary resource CAS is unchanged. */
  async reselectionRequest<T>(operation: ReselectionRequestOperation, options: ReselectionRequestOptions): Promise<APIResponse<T>> {
    const body = options.body;
    try {
      if (!reselectionRequests.has(operation) || this.state.access?.role !== 'operator' || !this.can(operation)) throw new ManagementError('forbidden', 'This reselection operation is not permitted for your identity.');
      const contract = operationContracts[operation];
      if (!contract || contract.cas || contract.createOnly) throw new ManagementError('unavailable', 'This reselection contract is unavailable.');
      const { id, attempt, source, offset, end, signal } = options;
      if (typeof id !== 'string' || !id || id.length > 128 || id === '.' || id === '..' || /[\\/?#%\x00-\x20\x7f]/.test(id)) throw new ManagementError('invalid', 'The original operation ID is required.');
      const create = operation === 'CreateCollectionReselection';
      const upload = operation === 'UploadCollectionReselectionSource';
      if (create ? attempt !== undefined : typeof attempt !== 'string' || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(attempt)) throw new ManagementError('invalid', 'The original reselection attempt ID is required.');
      if (create || upload) {
        if (!(body instanceof Uint8Array) || !(body.buffer instanceof ArrayBuffer) || body.byteLength > (upload ? 1 << 20 : 4096) || (create && body.byteLength === 0)) throw new ManagementError('invalid', 'A bounded private reselection body is required.');
      } else if (body !== undefined) throw new ManagementError('invalid', 'This reselection operation does not accept a body.');
      if (upload) {
        if (!Number.isSafeInteger(source) || source! < 1 || source! > 1000 || !Number.isSafeInteger(offset) || offset! < 0 || offset! > 64 << 20 || typeof end !== 'boolean' || body!.byteLength > (64 << 20) - offset! || body!.byteLength === 0 && !end) throw new ManagementError('invalid', 'A bounded ordered source part is required.');
      } else if (source !== undefined || offset !== undefined || end !== undefined) throw new ManagementError('invalid', 'This reselection operation does not accept source coordinates.');
      let path: string = contract.path.replace('{id}', encodeURIComponent(id));
      if (attempt !== undefined) path = path.replace('{attempt}', attempt);
      if (upload) path = `${path.replace('{source}', String(source))}?offset=${offset}&end=${end}`;
      return await this.request<T>(path, { method: contract.method, rawBody: body, rawContentType: upload ? 'application/octet-stream' : undefined,
        token: this.#token, signal, maxResponseBytes: 16 << 10, maxErrorBytes: 16 << 10 });
    } finally { body?.fill(0); }
  }

  /** Legacy reads keep their existing base URL behavior, but a bearer never leaves this origin. */
  async legacy<T>(path: string, options: ReadOptions = {}): Promise<T> {
    const url = new URL(path, this.origin);
    if (this.#token && (url.origin !== this.origin || !url.pathname.startsWith('/api/v1/'))) {
      throw new ManagementError('invalid', 'Authenticated reads must use this server origin.');
    }
    return (await this.request<T>(url.href, { ...options, legacy: true, token: this.#token })).data;
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<APIResponse<T>> {
    const epoch = this.state.epoch;
    const configuredLimit = this.options.maxResponseBytes ?? MAX_RESPONSE;
    const requestedLimit = options.maxResponseBytes ?? MAX_RESPONSE;
    if (![configuredLimit, requestedLimit].every(limit => Number.isSafeInteger(limit) && limit >= 1 && limit <= MAX_RESPONSE)) throw new ManagementError('invalid', 'A bounded response limit is required.');
    const responseLimit = Math.min(configuredLimit, requestedLimit);
    const url = new URL(path, this.origin);
    if (options.metrics && (path !== '/metrics' || (options.method !== undefined && options.method !== 'GET') || options.body !== undefined)) {
      throw new ManagementError('invalid', 'Metrics requests must use the fixed read-only endpoint.');
    }
    if (!options.metrics && !options.legacy && (url.origin !== this.origin || !url.pathname.startsWith('/api/v2/') || url.username || url.password || url.hash || path.includes('\\'))) {
      throw new ManagementError('invalid', 'Management requests must use this server API origin.');
    }
    const method = options.method ?? 'GET';
    const mutation = method !== 'GET' && !options.readOnlyPost;
    // Serialize before dispatch: an invalid body is a local failure, not an uncertain write.
    let body: string | Uint8Array<ArrayBuffer> | undefined;
    try { body = options.rawBody ?? (options.body === undefined ? undefined : JSON.stringify(options.body)); }
    catch { throw new ManagementError('invalid', 'The request could not be encoded.'); }
    const headers = new Headers({ Accept: options.metrics ? 'text/plain; version=0.0.4; charset=utf-8, application/openmetrics-text; version=1.0.0; charset=utf-8' : 'application/json, application/problem+json' });
    if (options.token) headers.set('Authorization', `Bearer ${options.token}`);
    if (body !== undefined) headers.set('Content-Type', options.rawContentType ?? (method === 'PATCH' ? 'application/merge-patch+json' : 'application/json'));
    if (options.resourceVersion !== undefined) headers.set('If-Match', strongETag(options.resourceVersion));
    if (options.createOnly) headers.set('If-None-Match', '*');
    const controller = new AbortController();
    const abort = () => controller.abort();
    options.signal?.addEventListener('abort', abort, { once: true });
    if (options.signal?.aborted) controller.abort();
    this.pending.add(controller);
    const timeout = setTimeout(abort, this.options.timeoutMs ?? REQUEST_TIMEOUT);
    let dispatched = false;
    let metadata: Omit<APIResponse<unknown>, 'data'> | undefined;
    try {
      if (controller.signal.aborted) throw new ManagementError('cancelled', 'The request was cancelled before sending.');
      dispatched = true;
      const response = await (this.options.fetch ?? fetch)(url.href, { method, headers, body, signal: controller.signal,
        redirect: 'error', cache: 'no-store', credentials: (options.legacy || options.discovery) && !options.token ? 'same-origin' : 'omit' });
      const retry = response.headers.get('Retry-After') ?? '';
      const etag = response.headers.get('ETag') ?? '';
      metadata = { status: response.status, requestID: response.headers.get('X-Request-ID') ?? '',
        operationID: response.headers.get('X-Operation-ID') ?? '', resourceVersion: response.headers.get('X-Resource-Version') ?? (etag.startsWith('"') && etag.endsWith('"') ? etag.slice(1, -1) : ''),
        retryAfterMs: /^\d+$/.test(retry) ? Number(retry) * 1000 : Math.max(0, (Date.parse(retry) || 0) - Date.now()) };
      if (this.state.epoch !== epoch) { await response.body?.cancel(); throw new ManagementError('session-changed', 'The session changed during this request.'); }
      if (controller.signal.aborted) { await response.body?.cancel(); throw new ManagementError('cancelled', 'The request was cancelled.'); }
      if (response.redirected || (response.status >= 300 && response.status < 400)) {
        await response.body?.cancel();
        throw new ManagementError('invalid', 'API redirects are not supported.');
      }
      let value: unknown;
      if (!response.ok) {
        if (options.metrics) {
          await response.body?.cancel().catch(() => undefined);
          if (this.state.epoch !== epoch) throw new ManagementError('session-changed', 'The session changed during this request.');
          if (response.status === 401 && options.token) this.reset('signed-out', 'Your session is no longer authorized. Sign in again.');
          throw new ManagementError('http', `Metrics request rejected (HTTP ${response.status}).`, response.status);
        }
        let responseIssue: 'too-large' | 'invalid' | undefined;
        try { value = await readJSON(response, options.maxErrorBytes ?? MAX_ERROR, controller.signal, true); }
        catch (error) {
          if (controller.signal.aborted) throw error;
          responseIssue = error instanceof ManagementError && error.reason === 'too-large' ? 'too-large' : 'invalid';
        }
        if (this.state.epoch !== epoch) throw new ManagementError('session-changed', 'The session changed during this request.');
        if (response.status === 401 && options.token && options.expireOn401 !== false) this.reset('signed-out', 'Your session is no longer authorized. Sign in again.');
        const problem = problemDetails(value);
        // Only this complete, explicit server contract proves that allocation
        // failed before any resource/control/action command was submitted.
        // Generic 5xx or malformed/truncated problems remain uncertain writes.
        if (mutation && !responseIssue && allocationNotSubmitted(response, value)) {
          throw new ManagementError('not-admitted', 'The server could not confirm operation allocation. No resource change or action was submitted. No automatic retry was made.', response.status, problem, undefined, metadata);
        }
        if (mutation && (response.status >= 500 || problem?.code === 'outcomeUnconfirmed' || problem?.code === 'requestInterrupted')) {
          throw new ManagementError('unconfirmed', 'The server could not confirm the mutation outcome. Reconcile its original operation before taking further action.', response.status, problem, responseIssue, metadata);
        }
        throw new ManagementError('http', `Request rejected (HTTP ${response.status}).`, response.status, problem, responseIssue, metadata);
      }
      value = options.metrics ? await readMetricsText(response, controller.signal) : await readJSON(response, responseLimit, controller.signal);
      if (this.state.epoch !== epoch) throw new ManagementError('session-changed', 'The session changed during this request.');
      return { data: value as T, ...metadata };
    } catch (error) {
      if (error instanceof ManagementError && (error.reason === 'http' || error.reason === 'not-admitted' || error.reason === 'unconfirmed')) throw error;
      if (mutation && dispatched) throw new ManagementError('unconfirmed', 'The outcome is unconfirmed. Inspect the original resource or operation before taking further action.', undefined, undefined, undefined, metadata);
      if (this.state.epoch !== epoch) throw new ManagementError('session-changed', 'The session changed during this request.');
      if (controller.signal.aborted) throw new ManagementError('cancelled', 'The request was cancelled or timed out.');
      if (error instanceof ManagementError) throw error;
      throw new ManagementError('unavailable', 'The server could not be reached.');
    } finally {
      clearTimeout(timeout);
      options.signal?.removeEventListener('abort', abort);
      this.pending.delete(controller);
    }
  }
}

export const dashboardSession = new DashboardSession();
