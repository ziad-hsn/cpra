import type { FailureDiagnostic } from '../api/generated';

export function FailureDetails({ diagnostic, label = 'Failure' }: { diagnostic?: FailureDiagnostic; label?: string }) {
  if (!diagnostic) return null;
  return <p style={{ overflowWrap: 'anywhere' }}>{label}: <span className="mono">{diagnostic.code}</span> · {diagnostic.message}</p>;
}
