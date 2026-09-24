import { DashboardSession, ManagementError } from './session';
import { object } from './value';

export interface RuntimeObservation {
  legacy: boolean;
  generatedAt?: string;
  ready?: boolean;
  live?: boolean;
  readinessReason?: string;
  controllerReady?: boolean;
  controllerReason?: string;
  projectionFresh?: boolean;
  projectionAgeMs?: number;
  storage: { available: boolean; mode: string; appliedIndex?: number; commitLatencyMs?: number; snapshotDurationMs?: number; error: boolean };
}

const boolean = (value: unknown) => typeof value === 'boolean' ? value : undefined;
const nonnegative = (value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined;
const count = (value: unknown) => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
const timestamp = (value: unknown) => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T.+(?:Z|[+-]\d{2}:\d{2})$/.test(value) && !/^0001-01-01T00:00:00(?:\.0+)?(?:Z|[+-]00:00)$/.test(value) && Number.isFinite(Date.parse(value)) ? value : undefined;
const measurement = (value: unknown) => object(value) && value.available === true ? nonnegative(value.value) : undefined;
const storageMode = (value: unknown) => value === 'raft' ? 'Raft' : value === 'memory' ? 'Memory (not persistent)' : value === 'unavailable' ? 'Unavailable' : 'Unrecognized storage mode';
const readinessReasons: Record<string, string> = {
  'Controller initialization or progress is unavailable.': 'Controller initialization or progress is unavailable.',
  'The durable management catalog is unavailable.': 'The durable management catalog is unavailable.',
  'Durable storage is unavailable.': 'Durable storage is unavailable.',
  'Admission has stopped while CPRa drains.': 'Admission has stopped while CPRa drains.',
};
const controllerReasons: Record<string, string> = { not_recorded: 'Controller progress is not recorded.', controller_not_ready: 'Controller initialization, progress or admission is not ready.' };
function reason(value: unknown, known: Record<string, string>): string | undefined {
  if (value === undefined || value === '') return undefined;
  return typeof value === 'string' && Object.hasOwn(known, value) ? known[value] : 'The server reported an unrecognized reason.';
}
function invalid(): never { throw new ManagementError('invalid', 'The runtime observation has an unsupported format.'); }

/** Decode only operational observations; raw storage errors, action arrays and
 * future payload fields never enter the dashboard query cache. */
export function runtimeView(value: unknown, legacy = false): RuntimeObservation {
  if (!object(value) || !object(value.storage)) return invalid();
  const store = value.storage;
  const available = boolean(legacy ? store.ready : store.available);
  if (available === undefined || typeof store.mode !== 'string') return invalid();
  const storage: RuntimeObservation['storage'] = { available, mode: storageMode(store.mode), error: typeof store.error === 'string' && store.error.length > 0 };
  if (legacy) {
    storage.appliedIndex = count(store.committed_index);
    // The legacy contract has no measurement availability bit; zero includes
    // "never measured", so it cannot be presented as a successful timing sample.
    storage.commitLatencyMs = (nonnegative(store.commit_latency_ms) ?? 0) > 0 ? store.commit_latency_ms as number : undefined;
    storage.snapshotDurationMs = (nonnegative(store.snapshot_duration_ms) ?? 0) > 0 ? store.snapshot_duration_ms as number : undefined;
    return { legacy: true, storage };
  }
  const generatedAt = timestamp(value.generatedAt);
  if (!generatedAt || typeof value.ready !== 'boolean' || typeof value.live !== 'boolean') return invalid();
  storage.appliedIndex = count(store.appliedIndex);
  storage.commitLatencyMs = measurement(store.commitLatency);
  storage.snapshotDurationMs = measurement(store.snapshotDuration);
  return { legacy: false, generatedAt, ready: value.ready, live: value.live, storage,
    readinessReason: reason(value.readinessReason, readinessReasons),
    controllerReady: value.controllerAvailable === true ? boolean(value.controllerReady) : undefined,
    controllerReason: reason(value.controllerReason, controllerReasons),
    projectionFresh: value.projectionAvailable === true ? boolean(value.projectionFresh) : undefined,
    projectionAgeMs: value.projectionAvailable === true ? nonnegative(value.projectionAgeMs) : undefined,
  };
}

export async function loadRuntime(session: DashboardSession, signal?: AbortSignal): Promise<RuntimeObservation> {
  if (session.getSnapshot().phase === 'legacy') return runtimeView(await session.legacy<unknown>('/api/v1/state', { signal }), true);
  if (!session.can('GetState')) throw new ManagementError('forbidden', 'This identity cannot read runtime state.');
  return runtimeView((await session.get<unknown>('/api/v2/state', { signal })).data);
}

export interface ReadinessObservation { ready: boolean; generatedAt?: string }
export async function loadReadiness(session: DashboardSession, signal?: AbortSignal): Promise<ReadinessObservation> {
  if (!session.can('GetReady')) throw new ManagementError('forbidden', 'This identity cannot read readiness.');
  try {
    const response = await session.get<unknown>('/api/v2/readyz', { signal });
    if (!object(response.data) || typeof response.data.available !== 'boolean') return invalid();
    return { ready: response.data.available, generatedAt: timestamp(response.data.generatedAt) };
  } catch (error) {
    if (error instanceof ManagementError && error.status === 503 && !error.responseIssue && error.problem?.code === 'notReady') return { ready: false };
    throw error;
  }
}
