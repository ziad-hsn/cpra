import type { ResolutionNotice } from '../api/generated';
import { formatDateTime } from '../lib/format';

export function NotificationResolution({ resolution }: { resolution?: ResolutionNotice }) {
  if (!resolution) return null;
  return <div className="stack-4">
    <p>Earlier outage recovered at {formatDateTime(resolution.recoveredAt)}. Health at this notification: {resolution.health}.</p>
    <p className="muted">Closing condition {resolution.conditionID}. Local health recovery and delivery acceptance do not prove that the provider resolved its alert.</p>
  </div>;
}

export function terminationLabel(reason: string): string {
  const labels: Record<string, string> = {
    attempt_limit: 'Attempt limit reached', execution_deadline: 'Execution deadline reached', permanent_rejection: 'Permanent rejection',
    recovered_before_start: 'Recovered before execution started', configuration_changed: 'Configuration changed', suppressed: 'Suppressed by control policy',
  };
  return labels[reason] ?? reason.replaceAll('_', ' ');
}
