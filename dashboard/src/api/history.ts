import { api } from './client';
import { DashboardSession, ManagementError } from './session';
import { object } from './value';
import type { HistoryResponse, MonitorEvent } from './types';

/** Keep v1 only for an explicitly discovered legacy server. Named v2 access
 * never falls back to a less restrictive read after denial or unavailable data. */
export async function loadTimeline(session: DashboardSession, monitorID: string, cursor = '', signal?: AbortSignal): Promise<HistoryResponse> {
  if (session.getSnapshot().phase === 'legacy') return api.getHistory(monitorID, cursor);
  if (!session.can('GetHistory')) throw new ManagementError('forbidden', 'This identity cannot read monitor history.');
  const params = new URLSearchParams({ monitorID, limit: '100' });
  if (cursor) params.set('cursor', cursor);
  const response = await session.get<unknown>(`/api/v2/history?${params}`, { signal });
  if (!object(response.data) || !Array.isArray(response.data.items) || response.data.items.length > 500) {
    throw new ManagementError('invalid', 'The server returned an unsupported event page.');
  }
  const events = response.data.items.map(value => {
    if (!object(value) || typeof value.id !== 'string' || !value.id || value.monitorID !== monitorID || typeof value.time !== 'string' || typeof value.kind !== 'string') {
      throw new ManagementError('invalid', 'An event could not be matched to this monitor.');
    }
    const event: MonitorEvent = { id: value.id, monitor_id: monitorID, at: value.time, type: value.kind, revision: typeof value.executionRevision === 'string' ? value.executionRevision : '' };
    for (const [source, target] of [['actor', 'actor'], ['reason', 'reason'], ['note', 'note'], ['actionID', 'action_id'], ['incidentID', 'incident_id'], ['controlRevision', 'control_revision'], ['actionKind', 'kind'], ['color', 'color'], ['outcome', 'outcome']] as const) {
      if (typeof value[source] === 'string') event[target] = value[source];
    }
    if (Number.isSafeInteger(value.endpoint) && (value.endpoint as number) >= 0) event.endpoint = value.endpoint as number;
    if (value.evidenceRefs !== undefined) {
      if (!Array.isArray(value.evidenceRefs) || value.evidenceRefs.length > 8 || value.evidenceRefs.some(ref => typeof ref !== 'string' || new TextEncoder().encode(ref).byteLength > 2048)) {
        throw new ManagementError('invalid', 'An event contains unsupported evidence references.');
      }
      event.evidence_refs = [...value.evidenceRefs] as string[];
    }
    return event;
  });
  if (response.data.nextCursor !== undefined && (typeof response.data.nextCursor !== 'string' || response.data.nextCursor.length > 8192)) {
    throw new ManagementError('invalid', 'The server returned an unsupported history cursor.');
  }
  return { events, retention_days: 30, ...(response.data.nextCursor ? { next_cursor: response.data.nextCursor as string } : {}) };
}
