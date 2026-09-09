import { CODE_LABELS, type CpraCode } from '../theme/tokens';

export function CodeBadge({ code, label }: { code: CpraCode; label?: string }) {
  const text = label ?? CODE_LABELS[code];
  return (
    <span className={'code-badge ' + code} role="status" aria-label={text}>
      <span className="status-dot" />
      {text}
    </span>
  );
}
