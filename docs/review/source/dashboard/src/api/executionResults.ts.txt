import type { ApplyResult, ExecutionChildDisposition, ExecutionResultAvailability, ExecutionResultCounts, ExecutionResultSummary } from './generated';
import { ManagementError } from './session';
import { object } from './value';

const invalid = () => new ManagementError('invalid', 'The server returned an invalid execution result observation.');
const integer = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
const token = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(v);
const identifier = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_.:-]{1,256}$/.test(v) && v !== '.' && v !== '..';
const digest = (v: unknown): v is string => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const time = (v: unknown): v is string => typeof v === 'string' && v.length <= 64 && !v.startsWith('0001-') && /^\d{4}-\d\d-\d\dT/.test(v) && Number.isFinite(Date.parse(v));
const countNames = ['processed', 'accepted', 'unchanged', 'conflicts', 'dependencyBlocked', 'unattempted', 'childPending', 'childApplied', 'childFailed', 'childSuperseded', 'childInvalidated'] as const;
function counts(raw: unknown, total: number, terminal = false): ExecutionResultCounts {
  if (!object(raw)) throw invalid();
  const result = {} as ExecutionResultCounts;
  for (const name of countNames) { if (!integer(raw[name]) || raw[name] > total) throw invalid(); result[name] = raw[name]; }
  if (result.accepted + result.unchanged + result.conflicts + result.dependencyBlocked !== result.processed || result.childPending + result.childApplied + result.childFailed + result.childSuperseded + result.childInvalidated !== result.accepted || result.processed + result.unattempted > total || terminal && (result.childPending !== 0 || result.processed + result.unattempted !== total)) throw invalid();
  return result;
}
function summary(raw: unknown, total: number): ExecutionResultSummary {
  if (!object(raw) || !identifier(raw.resultID) || !identifier(raw.uploadID) || !identifier(raw.planID) || !digest(raw.planDigest) || !digest(raw.digest) || !token(raw.outcome) || raw.itemCount !== total || !integer(raw.bytes) || !time(raw.finalizedAt) || !time(raw.expiresAt) || Date.parse(raw.expiresAt) <= Date.parse(raw.finalizedAt)) throw invalid();
  return { ...counts(raw, total, true), resultID: raw.resultID, uploadID: raw.uploadID, planID: raw.planID, planDigest: raw.planDigest, digest: raw.digest, outcome: raw.outcome, itemCount: total, bytes: raw.bytes, finalizedAt: raw.finalizedAt, expiresAt: raw.expiresAt };
}
function child(raw: unknown): ExecutionChildDisposition {
  if (!object(raw) || !identifier(raw.operationID) || !token(raw.state)) throw invalid();
  const result: ExecutionChildDisposition = { operationID: raw.operationID, state: raw.state };
  if (raw.outcome !== undefined) { if (!token(raw.outcome)) throw invalid(); result.outcome = raw.outcome; }
  if (raw.updatedAt !== undefined) { if (!time(raw.updatedAt)) throw invalid(); result.updatedAt = raw.updatedAt; }
  if (raw.invalidatedByRestore !== undefined) { if (!identifier(raw.invalidatedByRestore)) throw invalid(); result.invalidatedByRestore = raw.invalidatedByRestore; }
  if (raw.state === 'pending' && (result.outcome !== undefined || result.updatedAt !== undefined || result.invalidatedByRestore !== undefined)) throw invalid();
  const outcomes: Record<string, string> = { completed: 'applied', failed: 'projection_failed', partial: 'superseded' };
  if (outcomes[raw.state] && (!result.outcome || Object.values(outcomes).includes(result.outcome) && result.outcome !== outcomes[raw.state] || !result.updatedAt || result.invalidatedByRestore !== undefined && raw.state !== 'partial')) throw invalid();
  return result;
}
function item(raw: unknown, total: number, ready: boolean): ApplyResult {
  if (!object(raw) || !token(raw.kind) || typeof raw.id !== 'string' || !raw.id.startsWith(`${raw.kind}/`) || !identifier(raw.id.slice(raw.kind.length + 1)) || !token(raw.outcome) || !token(raw.catalogDecision) || !integer(raw.inputOrdinal) || raw.inputOrdinal < 1 || raw.inputOrdinal > total || !integer(raw.planOrdinal) || raw.planOrdinal < 1 || raw.planOrdinal > total || typeof raw.source !== 'string' || !/^source\.\d{20}$/.test(raw.source) || Number(raw.source.slice(7)) < 1 || Number(raw.source.slice(7)) > 1_000_000 || !integer(raw.sourceDocument) || raw.sourceDocument < 1 || raw.sourceDocument > 10_000_000 || !integer(raw.sourceItem) || raw.sourceItem < 1 || raw.sourceItem > 10_000_000) throw invalid();
  const result: ApplyResult = { id: raw.id, kind: raw.kind, outcome: raw.outcome, catalogDecision: raw.catalogDecision, inputOrdinal: raw.inputOrdinal, planOrdinal: raw.planOrdinal, source: raw.source, sourceDocument: raw.sourceDocument, sourceItem: raw.sourceItem };
  for (const key of ['oldVersion', 'newVersion', 'originalUID', 'uid'] as const) if (raw[key] !== undefined) { if (!identifier(raw[key])) throw invalid(); result[key] = raw[key]; }
  for (const key of ['committed', 'applied'] as const) if (raw[key] !== undefined) { if (typeof raw[key] !== 'boolean') throw invalid(); result[key] = raw[key]; }
  for (const key of ['generation', 'committedIndex'] as const) if (raw[key] !== undefined) { if (!integer(raw[key]) || raw[key] < 1) throw invalid(); result[key] = raw[key]; }
  if (raw.decidedAt !== undefined) { if (!time(raw.decidedAt)) throw invalid(); result.decidedAt = raw.decidedAt; }
  if (raw.childDisposition !== undefined) result.childDisposition = child(raw.childDisposition);
  const disposition = result.childDisposition;
  if (!disposition && result.applied !== undefined || disposition?.state === 'pending' && (ready || result.applied !== undefined)) throw invalid();
  if (disposition && ['completed', 'failed', 'partial'].includes(disposition.state) && (result.applied === undefined || ['applied', 'projection_failed', 'superseded'].includes(disposition.outcome ?? '') && result.applied !== (disposition.state === 'completed'))) throw invalid();
  switch (result.catalogDecision) {
    case 'accepted':
      if (result.committed !== true || !disposition || !result.uid || !result.newVersion || !result.generation || !result.decidedAt || !result.committedIndex) throw invalid();
      break;
    case 'unchanged': case 'conflict': case 'dependencyBlocked': case 'unattempted':
      if (result.committed !== false || disposition || result.applied !== undefined) throw invalid();
      if (result.catalogDecision === 'unattempted') { if (result.decidedAt || result.committedIndex || result.uid || result.newVersion || result.generation) throw invalid(); }
      else if (!result.decidedAt || !result.committedIndex) throw invalid();
      if (result.catalogDecision === 'unchanged' && (!result.uid || !result.newVersion || result.newVersion !== result.oldVersion || !result.generation)) throw invalid();
  }
  return result;
}

/** Returns allowlisted metadata only. Unknown observation tokens remain intact;
 * they never grant activation or establish a known terminal disposition. */
export function executionObservation(raw: unknown, total: unknown, rows?: unknown, cursor?: string): { executionResult: ExecutionResultAvailability; items?: ApplyResult[] } {
  if (!object(raw) || !token(raw.state) || !integer(total) || total < 1 || total > 10_000_000) throw invalid();
  const result: ExecutionResultAvailability = { state: raw.state };
  if (raw.counts !== undefined) result.counts = counts(raw.counts, total);
  if (raw.summary !== undefined) result.summary = summary(raw.summary, total);
  if (result.summary && result.counts && countNames.some(key => result.counts![key] !== result.summary![key])) throw invalid();
  if (result.state === 'ready' && !result.summary) throw invalid();
  if (rows !== undefined && (!Array.isArray(rows) || rows.length > 500)) throw invalid();
  const items = (rows as unknown[] | undefined)?.map(row => item(row, total, result.state === 'ready'));
  if (new Set(items?.map(row => row.id)).size !== (items?.length ?? 0)) throw invalid();
  if (items?.some((row, n) => n > 0 && row.inputOrdinal !== items[n - 1].inputOrdinal! + 1)) throw invalid();
  if (['pending', 'expired'].includes(result.state) && (items?.length || cursor)) throw invalid();
  if (result.state === 'ready' && items !== undefined && (!items.length || !!cursor !== (items.at(-1)!.inputOrdinal! < total))) throw invalid();
  return { executionResult: result, items };
}
