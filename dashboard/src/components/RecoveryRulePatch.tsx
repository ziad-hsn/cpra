import { useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { Monitor } from '../api/generated';
import { mutationReceipt, object } from '../api/resources';
import { useDashboardSession } from '../auth/SessionBoundary';
import { useManagementMutation } from '../hooks/mutations';
import { DirtyDraftGuard } from './DirtyDraftGuard';
import { FieldValidation } from './FieldValidation';
import { ProblemDetails } from './ProblemDetails';
import { MutationReceipt, OperationLink } from './OperationReceipt';
import { RecoveryRules, RecoveryRuleChanges } from './RecoveryRules';

/** Edit only an understood recovery subtree. Unknown check fields are neither
 * cached by this form nor sent back to the server. */
export function RecoveryRulePatch({ monitor, onClose }: { monitor: Monitor; onClose: () => void }) {
  const session = useDashboardSession();
  const cache = useQueryClient();
  const [original] = useState(() => structuredClone(monitor));
  const [draft, setDraft] = useState<Record<string, unknown>>(() => {
    const current: Record<string, unknown> = object(monitor.spec.recovery) ? structuredClone(monitor.spec.recovery) : {};
    if (Array.isArray(current.rules)) return current;
    const next = { ...current }; delete next.driver;
    return { ...next, taxonomyVersion: '1', rules: [{ id: '', match: { codes: [] }, none: true }] };
  });
  const [dirty, setDirty] = useState(false);
  const [complete, setComplete] = useState(false);
  const mutation = useManagementMutation<unknown>(mutationReceipt);
  const errors = useRef<HTMLDivElement>(null);
  useEffect(() => { if (mutation.error) errors.current?.focus(); }, [mutation.error]);
  async function save() {
    if (mutation.pending || mutation.error?.reason === 'unconfirmed' || !original.metadata.uid || !original.metadata.resourceVersion) return;
    const epoch = session.getSnapshot().epoch;
    try {
      // Explicit null removes a former single driver. This patch has exactly one
      // mutable subtree; If-Match remains the originally observed version.
      await mutation.execute(`/api/v2/monitors/${encodeURIComponent(original.metadata.id)}`, { method: 'PATCH', operation: 'PatchMonitor', resourceVersion: original.metadata.resourceVersion, body: { spec: { recovery: { ...draft, driver: null } } } });
      if (session.getSnapshot().epoch !== epoch) return;
      setDirty(false); setComplete(true);
      await cache.invalidateQueries({ queryKey: ['management', 'monitors'] });
    } catch { errors.current?.focus(); }
  }
  const failure = mutation.error?.reason === 'unconfirmed'
    ? 'The outcome is unconfirmed. Do not submit again; inspect the original resource or operation.'
    : mutation.error?.status === 409 || mutation.error?.status === 412
      ? 'The resource changed. Your draft still uses its original version. Close this editor and compare the current configuration before deciding.'
      : mutation.error?.status === 404
        ? 'The original resource no longer exists. This draft cannot recreate it.'
        : 'The request did not complete. Review the highlighted fields and server availability.';
  return <FieldValidation error={mutation.error}><section className="card" aria-label="Edit intervention rules only">
    <DirtyDraftGuard dirty={dirty} />
    <h3>Edit intervention rules</h3>
    <p>This saves only the recovery configuration. The server validates rules against the check’s exact contract before applying the change.</p>
    <p>Configuration version: <span className="mono">{original.metadata.resourceVersion}</span>. Existing attempt limits and cooldowns remain in the proposed configuration.</p>
    {mutation.error && <div ref={errors} tabIndex={-1} role="alert"><p>{failure}</p><ProblemDetails error={mutation.error} />
      <OperationLink id={mutation.error.response?.operationID || mutation.error.problem?.operationID} source="error" label="Reconcile original operation" />
    </div>}
    {mutation.response && <MutationReceipt response={mutation.response} />}
    {!complete && <form onSubmit={event => { event.preventDefault(); void save(); }}>
      <RecoveryRules recovery={draft} checkDriver={original.spec.check.driver.type} readOnly={mutation.pending} onChange={next => { if (!mutation.pending) { setDraft(next); setDirty(true); } }} />
      <RecoveryRuleChanges before={original.spec.recovery} after={draft} />
      <button type="submit" className="btn primary" disabled={mutation.pending || mutation.error?.reason === 'unconfirmed'}>Save intervention rules</button>
    </form>}
    <button type="button" className="btn ghost" disabled={mutation.pending} onClick={() => { if (!dirty || window.confirm('Discard the unsaved intervention rules?')) { setDirty(false); onClose(); } }}>Close rule editor</button>
  </section></FieldValidation>;
}
