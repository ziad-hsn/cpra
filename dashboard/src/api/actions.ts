import type { Action, ActionReview, AgentReceipt, RecoveryFailure, RecoverySelection, ResolutionNotice, ReviewNotice } from './generated';
import { DashboardSession, ManagementError } from './session';
import { object } from './value';
import { failureView } from './failure';

function invalid(): never { throw new ManagementError('invalid', 'The action response could not be safely identified.'); }

function recoverySelectionView(value: unknown): RecoverySelection {
  if (!object(value)) return invalid();
  const fields = ['ruleID', 'driver', 'configurationRevision', 'decisionRevision', 'incidentID', 'observationID'] as const;
  for (const key of fields) {
    if (typeof value[key] !== 'string' || !value[key] || new TextEncoder().encode(value[key]).byteLength > 256 || /\p{Cc}/u.test(value[key])) return invalid();
  }
  return { ruleID: value.ruleID as string, driver: value.driver as string,
    configurationRevision: value.configurationRevision as string, decisionRevision: value.decisionRevision as string,
    incidentID: value.incidentID as string, observationID: value.observationID as string };
}

/** Retain correlation only, without agent output or supplied links. */
export function agentReceiptView(value: unknown): AgentReceipt {
  if (!object(value) || typeof value.sequence !== 'string' || !/^[1-9][0-9]{0,19}$/.test(value.sequence) || typeof value.observedAt !== 'string' || value.observedAt.length > 64 || !Number.isFinite(Date.parse(value.observedAt))) return invalid();
  const receipt: AgentReceipt = { sequence: value.sequence, observedAt: value.observedAt };
  for (const key of ['taskID', 'contextID', 'messageID', 'state'] as const) {
    if (value[key] === undefined) continue;
    if (typeof value[key] !== 'string' || !value[key] || new TextEncoder().encode(value[key]).byteLength > 256 || /\p{Cc}/u.test(value[key])) return invalid();
    receipt[key] = value[key];
  }
  if (!!receipt.taskID === !!receipt.messageID || (receipt.taskID && !receipt.state) || (receipt.messageID && receipt.state)) return invalid();
  return receipt;
}

/** Cache only the published observation fields. A provider payload never becomes
 * editable configuration, and an operator assertion never replaces its outcome. */
export function recoveryFailureView(value: unknown): RecoveryFailure {
  if (!object(value) || typeof value.actionID !== 'string' || !value.actionID || new TextEncoder().encode(value.actionID).byteLength > 256 || /\p{Cc}/u.test(value.actionID) || typeof value.driver !== 'string' || !value.driver || new TextEncoder().encode(value.driver).byteLength > 128 || /\p{Cc}/u.test(value.driver) ||
    !Number.isSafeInteger(value.attempt) || (value.attempt as number) < 1 || (value.attempt as number) > 2147483647 || !Number.isSafeInteger(value.maxAttempts) || (value.maxAttempts as number) < 1 || (value.maxAttempts as number) > 2147483647 ||
    (value.trigger !== 'recovery_failed' && value.trigger !== 'recovery_exhausted') || (value.trigger === 'recovery_exhausted' && (value.attempt as number) < (value.maxAttempts as number) && value.terminationReason !== 'execution_deadline' && value.terminationReason !== 'permanent_rejection') || value.outcome !== 'failure' || typeof value.failedAt !== 'string' || value.failedAt.length > 64 || !Number.isFinite(Date.parse(value.failedAt))) return invalid();
  if (value.terminationReason !== undefined && value.terminationReason !== '' && !['attempt_limit', 'execution_deadline', 'permanent_rejection'].includes(String(value.terminationReason))) return invalid();
  return { actionID: value.actionID, driver: value.driver, attempt: value.attempt as number, maxAttempts: value.maxAttempts as number, trigger: value.trigger, outcome: 'failure', failedAt: value.failedAt, ...(value.terminationReason ? {terminationReason: value.terminationReason as string} : {}) };
}

export function resolutionNoticeView(value: unknown): ResolutionNotice {
  if (!object(value) || typeof value.conditionID !== 'string' || !value.conditionID || new TextEncoder().encode(value.conditionID).byteLength > 256 || /\p{Cc}/u.test(value.conditionID) || typeof value.recoveredAt !== 'string' || value.recoveredAt.length > 64 || value.recoveredAt.startsWith('0001-') || !Number.isFinite(Date.parse(value.recoveredAt)) || !['healthy', 'unhealthy', 'unknown'].includes(String(value.health))) return invalid();
  return { conditionID: value.conditionID, recoveredAt: value.recoveredAt, health: value.health as ResolutionNotice['health'] };
}

export function reviewNoticeView(value: unknown): ReviewNotice {
  if (!object(value) || !['open', 'close'].includes(String(value.phase)) || !['healthy', 'unhealthy', 'unknown'].includes(String(value.health))) return invalid();
  for (const key of ['alertID', 'actionID'] as const) if (typeof value[key] !== 'string' || !value[key] || new TextEncoder().encode(value[key]).byteLength > 256 || /\p{Cc}/u.test(value[key])) return invalid();
  if (value.phase === 'open' && value.reason !== undefined && value.reason !== '' || value.phase === 'close' && !['health_recovered', 'action_reviewed'].includes(String(value.reason))) return invalid();
  return { alertID: value.alertID as string, actionID: value.actionID as string, phase: value.phase as string, health: value.health as string, ...(value.reason ? { reason: value.reason as string } : {}) };
}

export function actionView(value: unknown, monitorID: string, expectedID?: string): Action {
  if (!object(value) || typeof value.id !== 'string' || !value.id || value.id.length > 256 || (expectedID && value.id !== expectedID) || value.monitorID !== monitorID || typeof value.state !== 'string' || typeof value.held !== 'boolean' || typeof value.executorFenced !== 'boolean') return invalid();
  const action: Action = { id: value.id, monitorID, state: value.state, held: value.held, executorFenced: value.executorFenced };
  if (value.failure !== undefined) action.failure = failureView(value.failure);
  if (value.cause !== undefined) action.cause = failureView(value.cause);
  if (value.agentReceipt !== undefined) action.agentReceipt = agentReceiptView(value.agentReceipt);
  if (value.reviewNotice !== undefined) action.reviewNotice = reviewNoticeView(value.reviewNotice);
  if (value.recoveryFailure !== undefined) action.recoveryFailure = recoveryFailureView(value.recoveryFailure);
  if (value.recoverySelection !== undefined) action.recoverySelection = recoverySelectionView(value.recoverySelection);
  if (value.resolution !== undefined) action.resolution = resolutionNoticeView(value.resolution);
  if (value.executionBudget !== undefined && value.executionBudget !== '') {
    if (typeof value.executionBudget !== 'string' || value.executionBudget.length > 128 || !/^\+?(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h)(?:(?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))*$/.test(value.executionBudget) || !/[1-9]/.test(value.executionBudget)) return invalid();
    action.executionBudget = value.executionBudget;
  }
  if (value.terminationReason !== undefined && value.terminationReason !== '') {
    if (typeof value.terminationReason !== 'string' || value.terminationReason.length > 128 || !/^[a-z][a-z0-9_]*$/.test(value.terminationReason)) return invalid();
    action.terminationReason = value.terminationReason;
  }
  for (const key of ['incarnationUID', 'incidentID', 'kind', 'executionID', 'executionRevision', 'reason', 'receiptID', 'reviewRevision', 'outcome', 'color', 'deliveryID', 'endpointID', 'notificationEpisode'] as const) {
    if (typeof value[key] === 'string') action[key] = value[key];
  }
  for (const key of ['createdAt', 'updatedAt', 'queuedAt', 'notBefore', 'deadline', 'startedAt', 'finishedAt'] as const) {
    if (value[key] === undefined) continue;
    if (typeof value[key] !== 'string' || value[key].length > 64 || !Number.isFinite(Date.parse(value[key])) || value[key].startsWith('0001-')) return invalid();
    action[key] = value[key];
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
