import type { NotificationCondition } from '../api/generated';
import { formatDateTime, timeAgo } from '../lib/format';

const kindLabel: Record<string, string> = { opening: 'Opening notice', resolution: 'Earlier outage closure', migration: 'Upgrade review', review_opening: 'Recovery outcome review notice', review_closure: 'Recovery review closure' };
const reasonLabel: Record<string, string> = {
  suppressed: 'Suppressed by current control policy', disabled: 'Checks disabled', snoozed: 'Monitor snoozed',
  ignored: 'Monitor ignored', maintenance: 'Maintenance active', dismissed: 'Incident dismissed',
  configuration_changed: 'The original notification configuration changed', incarnation_changed: 'The original monitor was replaced',
  health_recovered: 'Health recovery was verified', action_reviewed: 'The action received a conclusive review',
};

/** Conditions and execution attempts have independent lifetimes. This view must
 * not turn local recovery or a delivery acceptance into provider resolution. */
export function MonitorNotificationConditions({ conditions, truncated = false, observedAt, sequence, headingLevel = 3 }: { conditions?: NotificationCondition[]; truncated?: boolean; observedAt?: string; sequence?: string; headingLevel?: 2 | 3 | 4 }) {
  const sampled = formatDateTime(observedAt ?? '');
  const progress = typeof sequence === 'string' && /^(0|[1-9]\d{0,19})$/.test(sequence) ? sequence : undefined;
  if (!conditions?.length && !truncated && sampled === '—') return null;
  const Heading = headingLevel === 2 ? 'h2' : headingLevel === 4 ? 'h4' : 'h3';
  return <section aria-label="Notification conditions" className="stack-12">
    <Heading>Notification conditions</Heading>
    <p className="muted">These conditions can remain pending or held without a new execution attempt. Review the original action for its provider outcome.</p>
    <p className="muted">{sampled === '—' ? 'Retained condition sample unavailable; only the controller summary is shown.' : `Conditions sampled ${sampled}${progress ? ` · Storage sequence ${progress}` : ''}. This sequence is not a write revision or pagination cursor.`}</p>
    {!conditions?.length && !truncated && sampled !== '—' && <p>No current notification conditions were reported in this bounded read.</p>}
    {(conditions ?? []).slice(0, 100).map(condition => <article key={condition.conditionID} className="stack-4" style={{ overflowWrap: 'anywhere' }}>
      <p><strong>{kindLabel[condition.kind] ?? 'Notification condition'}</strong> · {condition.color} · {condition.state}</p>
      {condition.kind === 'review_closure' && <p>This closes the review notice only. The original recovery outcome and its hold remain subject to action review; provider resolution is not inferred from this condition.</p>}
      <p className="muted mono">{condition.conditionID}</p>
      {condition.pendingAt && <p>Pending since {formatDateTime(condition.pendingAt)} ({timeAgo(condition.pendingAt)}).</p>}
      {condition.recoveredAt && <p>Earlier outage recovered at {formatDateTime(condition.recoveredAt)}. This does not describe the current outage or prove provider resolution.</p>}
      {(condition.migrationReason || condition.reason) && <p>Reason: {reasonLabel[condition.migrationReason || condition.reason || ''] ?? (condition.migrationReason || condition.reason)?.replaceAll('_', ' ')}.</p>}
      {condition.originalActionID && <p>Original action: <span className="mono">{condition.originalActionID}</span></p>}
    </article>)}
    {(truncated || (conditions?.length ?? 0) > 100) && <p className="muted">Additional retained conditions may be omitted or unavailable. Inspect monitor history and action records.</p>}
  </section>;
}
