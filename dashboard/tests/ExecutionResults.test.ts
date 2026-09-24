// @vitest-environment node
import { expect, it, vi } from 'vitest';
vi.hoisted(() => { vi.stubGlobal('window', { location: { origin: 'https://cpra.example' } }); });
import { collectionExecutionPage, collectionMutable, collectionOperation, collectionTerminal } from '../src/api/collectionOperations';
import { operationApplied, operationView } from '../src/api/operations';
import { collectionIdentityFormat } from '../src/api/collectionInventory';
import type { Operation } from '../src/api/generated';
const identity = { identityFormat: collectionIdentityFormat, contentDigest: 'a'.repeat(64), itemCount: 1 };
const counts = { processed: 1, accepted: 1, unchanged: 0, conflicts: 0, dependencyBlocked: 0, unattempted: 0, childPending: 0, childApplied: 0, childFailed: 1, childSuperseded: 0, childInvalidated: 0 };
function fixture(): Operation {
  return { ...identity, id: 'original-parent', state: 'partial', committed: 1, applied: 0,
    executionResult: { state: 'ready', counts: { ...counts }, summary: { ...counts, resultID: '11111111-1111-4111-8111-111111111111', uploadID: '22222222-2222-4222-8222-222222222222', planID: '33333333-3333-4333-8333-333333333333', planDigest: 'b'.repeat(64), outcome: 'partial', itemCount: 1, bytes: 500, digest: 'c'.repeat(64), finalizedAt: '2026-09-23T12:00:00Z', expiresAt: '2026-10-23T12:00:00Z' } },
    items: [{ id: 'Monitor/service', kind: 'Monitor', inputOrdinal: 1, planOrdinal: 1, source: 'source.00000000000000000001', sourceDocument: 2, sourceItem: 3, originalUID: 'original-uid', uid: 'original-uid', oldVersion: 'old', newVersion: 'new', generation: 2, catalogDecision: 'accepted', outcome: 'accepted', committedIndex: 11, decidedAt: '2026-09-23T12:00:00Z', committed: true, applied: false, childDisposition: { operationID: 'original-child', state: 'failed', outcome: 'projection_failed', updatedAt: '2026-09-23T12:00:00Z' } }],
  };
}
it('retains catalog decisions, child dispositions, coordinates and explicit false', () => {
  const raw = fixture();
  for (const decoded of [collectionOperation(raw, identity), operationView(raw)]) {
    expect(decoded).toEqual(raw); expect(decoded.items![0]).toMatchObject({ catalogDecision: 'accepted', committed: true, applied: false, childDisposition: { outcome: 'projection_failed' } }); expect(operationApplied(decoded)).toBe(false);
  }
});
it('keeps parent cancellation separate from pending or expired result availability', () => {
  for (const state of ['pending', 'expired']) {
    const raw = { ...fixture(), state: 'canceled', items: undefined, executionResult: { state, counts: { ...counts } } };
    const decoded = collectionOperation(raw, identity); expect(collectionTerminal(decoded)).toBe(true); expect(collectionMutable(decoded)).toBe(false);
    expect(decoded.executionResult!.state).toBe(state); expect(decoded.executionResult!.summary).toBeUndefined();
  }
});
it('retains bounded unknown observations without enabling changes', () => {
  const raw = fixture(); raw.executionResult!.state = 'future-availability'; raw.executionResult!.summary!.outcome = 'future-parent'; raw.items![0].catalogDecision = 'future-decision'; raw.items![0].outcome = 'future-outcome'; raw.items![0].childDisposition!.outcome = 'future-child-outcome';
  const decoded = collectionOperation(raw, identity); expect(decoded.executionResult).toEqual(raw.executionResult); expect(decoded.items).toEqual(raw.items); expect(collectionMutable(decoded)).toBe(false); expect(operationApplied(decoded)).toBe(false);
});
it('strips noncontract fields from every execution metadata layer', () => {
  const raw = fixture(); Object.assign(raw, { identityKey: 'private', sourceFingerprint: 'private' }); Object.assign(raw.executionResult!, { private: 'private' }); Object.assign(raw.executionResult!.summary!, { resource: { value: 'private' } }); Object.assign(raw.items![0], { message: 'private', resource: { spec: 'private' }, sourcePath: '/private' }); Object.assign(raw.items![0].childDisposition!, { error: 'private' });
  expect(JSON.stringify(collectionOperation(raw, identity))).not.toContain('private'); expect(JSON.stringify(operationView(raw))).not.toContain('private');
});
it.each(['summary', 'counts', 'decision', 'child', 'source', 'ordinal', 'applied', 'null'] as const)('rejects malformed execution %s observations', field => {
  const raw = fixture();
  switch (field) {
    case 'summary': raw.executionResult!.summary = undefined; break;
    case 'counts': raw.executionResult!.counts!.accepted = 0; break;
    case 'decision': raw.items![0].catalogDecision = 'unchanged'; raw.items![0].committed = false; break;
    case 'child': raw.items![0].childDisposition = { operationID: 'child', state: 'pending' }; raw.items![0].applied = undefined; break;
    case 'source': raw.items![0].source = '/private/file.yml'; break;
    case 'ordinal': raw.items![0].inputOrdinal = 0; break;
    case 'applied': raw.items![0].applied = true; break;
    case 'null': Object.assign(raw.items![0], { applied: null }); break;
  }
  expect(() => collectionOperation(raw, identity)).toThrow(); expect(() => operationView(raw)).toThrow();
});
it('represents unchanged with no mutation or child application observation', () => {
  const raw = fixture(); const unchanged = { ...counts, accepted: 0, unchanged: 1, childFailed: 0 }; raw.state = 'completed'; raw.committed = 0; raw.executionResult!.counts = unchanged; Object.assign(raw.executionResult!.summary!, unchanged, { outcome: 'completed' }); Object.assign(raw.items![0], { catalogDecision: 'unchanged', outcome: 'unchanged', committed: false, applied: undefined, childDisposition: undefined, newVersion: 'old' });
  const result = collectionOperation(raw, identity); expect(result.items![0].applied).toBeUndefined(); expect(result.items![0].childDisposition).toBeUndefined(); expect(result.items![0].committed).toBe(false);
});
it('represents an unattempted suffix without fabricating a decision or child', () => {
  const raw = fixture(); const stopped = { ...counts, processed: 0, accepted: 0, childFailed: 0, unattempted: 1 }; raw.state = 'canceled'; raw.committed = 0; raw.executionResult!.counts = stopped; Object.assign(raw.executionResult!.summary!, stopped, { outcome: 'canceled' }); Object.assign(raw.items![0], { catalogDecision: 'unattempted', outcome: 'unattempted', committed: false, applied: undefined, childDisposition: undefined, uid: undefined, newVersion: undefined, generation: undefined, committedIndex: undefined, decidedAt: undefined });
  const result = collectionOperation(raw, identity); expect(result.items![0].committed).toBe(false); expect(result.items![0].decidedAt).toBeUndefined();
});
it('keeps input order distinct from plan order and requires a complete final page', () => {
  const raw = fixture(); raw.itemCount = 2; raw.nextCursor = 'next'; raw.items![0].planOrdinal = 2; const doubled = { ...counts, processed: 2, accepted: 2, childFailed: 2 }; raw.executionResult!.counts = doubled; Object.assign(raw.executionResult!.summary!, doubled, { itemCount: 2 });
  const decoded = collectionOperation(raw, { ...identity, itemCount: 2 }); expect(decoded.items![0].inputOrdinal).toBe(1); expect(decoded.items![0].planOrdinal).toBe(2); raw.nextCursor = undefined; expect(() => collectionOperation(raw, { ...identity, itemCount: 2 })).toThrow();
});
it('allows list observations with a sealed summary and no embedded item page', () => {
  const raw = fixture(); raw.items = undefined; expect(operationView(raw).executionResult).toEqual(raw.executionResult);
});

it('pins the original sealed summary and input sequence across cursor pages', () => {
  const raw = fixture(); raw.itemCount = 2; raw.nextCursor = 'next'; raw.items![0].planOrdinal = 2;
  const doubled = { ...counts, processed: 2, accepted: 2, childFailed: 2 }; raw.executionResult!.counts = doubled; Object.assign(raw.executionResult!.summary!, doubled, { itemCount: 2 });
  const expected = { ...identity, itemCount: 2 }; const first = collectionExecutionPage(raw, expected, raw.id);
  const second = structuredClone(raw); second.nextCursor = undefined; second.items![0].id = 'Monitor/second'; second.items![0].inputOrdinal = 2; second.items![0].planOrdinal = 1;
  expect(collectionExecutionPage(second, expected, raw.id, 'next', first).items![0].inputOrdinal).toBe(2);
  second.executionResult!.summary!.bytes++;
  expect(() => collectionExecutionPage(second, expected, raw.id, 'next', first)).toThrow();
  expect(() => collectionExecutionPage(raw, expected, raw.id, 'next', first)).toThrow();
});
