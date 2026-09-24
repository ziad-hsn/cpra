import type { Action, ActionReview } from './generated';
import { DashboardSession, ManagementError } from './session';
import { object } from './value';

function invalid(): never { throw new ManagementError('invalid', 'The action response could not be safely identified.'); }

/** Cache only the published observation fields. A provider payload never becomes
 * editable configuration, and an operator assertion never replaces its outcome. */
export function actionView(value: unknown, monitorID: string, expectedID?: string): Action {
  if (!object(value) || typeof value.id !== 'string' || !value.id || value.id.length > 256 || (expectedID && value.id !== expectedID) || value.monitorID !== monitorID || typeof value.state !== 'string' || typeof value.held !== 'boolean' || typeof value.executorFenced !== 'boolean') return invalid();
  const action: Action = { id: value.id, monitorID, state: value.state, held: value.held, executorFenced: value.executorFenced };
  for (const key of ['incarnationUID', 'incidentID', 'kind', 'executionID', 'executionRevision', 'createdAt', 'updatedAt', 'reason', 'receiptID', 'reviewRevision', 'outcome'] as const) {
    if (typeof value[key] === 'string') action[key] = value[key];
  }
  for (const key of ['held', 'executorFenced'] as const) if (typeof value[key] === 'boolean') action[key] = value[key];
  for (const key of ['lateEvidence', 'conflictingEvidence'] as const) {
    if (value[key] === undefined) continue;
    const evidence = value[key];
    if (!object(evidence) || ['outcome', 'executionStart', 'executionEnd', 'recordedAt'].some(field => typeof evidence[field] !== 'string')) return invalid();
    action[key] = { outcome: evidence.outcome as string, executionStart: evidence.executionStart as string, executionEnd: evidence.executionEnd as string, recordedAt: evidence.recordedAt as string };
  }
  if (value.review !== undefined) {
    const review = value.review;
    if (!object(review) || !['accepted', 'rejected', 'inconclusive'].includes(String(review.resolution)) || ['revision', 'actor', 'reviewedAt', 'reason'].some(key => typeof review[key] !== 'string')) return invalid();
    action.review = { revision: review.revision as string, resolution: review.resolution as ActionReview['resolution'], actor: review.actor as string, reviewedAt: review.reviewedAt as string, reason: review.reason as string };
    if (typeof review.note === 'string') action.review.note = review.note;
    if (typeof review.conflict === 'boolean') action.review.conflict = review.conflict;
    if (review.evidenceRefs !== undefined) {
      if (!Array.isArray(review.evidenceRefs) || review.evidenceRefs.length > 8 || review.evidenceRefs.some(ref => typeof ref !== 'string' || ref.length > 2048)) return invalid();
      action.review.evidenceRefs = [...review.evidenceRefs] as string[];
    }
  }
  return action;
}

export async function loadAction(session: DashboardSession, monitorID: string, id: string, signal?: AbortSignal): Promise<Action> {
  const response = await session.get<unknown>(`/api/v2/actions/${encodeURIComponent(id)}`, { signal });
  return actionView(response.data, monitorID, id);
}

export async function loadActions(session: DashboardSession, monitorID: string, cursor = '', signal?: AbortSignal): Promise<{ items: Action[]; nextCursor?: string }> {
  const query = new URLSearchParams({ monitorID, limit: '100' });
  if (cursor) query.set('cursor', cursor);
  const response = await session.get<unknown>(`/api/v2/actions?${query}`, { signal });
  if (!object(response.data) || !Array.isArray(response.data.items) || response.data.items.length > 500) return invalid();
  const ids = new Set<string>();
  const items = response.data.items.map(value => {
    const item = actionView(value, monitorID);
    if (ids.has(item.id)) return invalid();
    ids.add(item.id); return item;
  });
  if (response.data.nextCursor !== undefined && (typeof response.data.nextCursor !== 'string' || response.data.nextCursor.length > 8192)) return invalid();
  return { items, ...(response.data.nextCursor ? { nextCursor: response.data.nextCursor as string } : {}) };
}
