import type { Incident, IncidentList } from './generated';
import { DashboardSession, ManagementError } from './session';
import { object } from './value';

const invalid = () => new ManagementError('invalid', 'The server returned an unsupported incident page.');
const identity = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 256 && value !== '.' && value !== '..' && !/[\\/?#%\x00-\x1f]/.test(value);

/** Retain incident observations only, never arbitrary echoed resource/configuration fields. */
function incident(value: unknown): Incident {
  if (!object(value) || !identity(value.id) || !identity(value.monitorID) || !identity(value.revision) || typeof value.state !== 'string') throw invalid();
  const result: Incident = { id: value.id, monitorID: value.monitorID, revision: value.revision,
    state: value.state === 'open' || value.state === 'closed' ? value.state : 'unrecognized' };
  for (const key of ['openedAt', 'closedAt', 'acknowledgedAt'] as const) {
    if (value[key] !== undefined) {
      if (typeof value[key] !== 'string' || !Number.isFinite(Date.parse(value[key]))) throw invalid();
      // Existing Go time.Time fields serialize their zero value despite omitempty.
      if (value[key].startsWith('0001-01-01')) continue;
      result[key] = value[key];
    }
  }
  if (value.acknowledgedBy !== undefined) {
    if (typeof value.acknowledgedBy !== 'string' || value.acknowledgedBy.length > 4096) throw invalid();
    result.acknowledgedBy = value.acknowledgedBy;
  }
  if (value.dismissed !== undefined) {
    if (typeof value.dismissed !== 'boolean') throw invalid();
    result.dismissed = value.dismissed;
  }
  return result;
}

/** The server freezes the latest incident per monitor; older events are in history. */
export async function listIncidents(session: DashboardSession, cursor = '', monitorID = '', signal?: AbortSignal): Promise<IncidentList> {
  if (cursor.length > 8192 || (monitorID !== '' && !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(monitorID))) {
    throw new ManagementError('invalid', 'Use an exact monitor ID and the original incident cursor.');
  }
  const query = new URLSearchParams({ limit: '100' });
  if (cursor) query.set('cursor', cursor);
  if (monitorID) query.set('monitorID', monitorID);
  const value = (await session.get<unknown>(`/api/v2/incidents?${query}`, { signal })).data;
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 100) throw invalid();
  const items = value.items.map(incident);
  if (new Set(items.map(item => item.id)).size !== items.length || new Set(items.map(item => item.monitorID)).size !== items.length) throw invalid();
  if (monitorID && items.some(item => item.monitorID !== monitorID)) throw invalid();
  const result: IncidentList = { items };
  for (const key of ['nextCursor', 'snapshot'] as const) {
    if (value[key] !== undefined) {
      if (typeof value[key] !== 'string' || value[key].length > (key === 'snapshot' ? 256 : 8192)) throw invalid();
      result[key] = value[key];
    }
  }
  if (value.generatedAt !== undefined) {
    if (typeof value.generatedAt !== 'string' || value.generatedAt.startsWith('0001-01-01') || !Number.isFinite(Date.parse(value.generatedAt))) throw invalid();
    result.generatedAt = value.generatedAt;
  }
  if (result.nextCursor && (!result.snapshot || !result.generatedAt)) throw invalid();
  if (cursor && result.nextCursor === cursor) throw new ManagementError('invalid', 'The incident page did not advance. Refresh from the first page.');
  return result;
}
