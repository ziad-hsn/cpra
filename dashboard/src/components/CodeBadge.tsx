import { CODE_LABELS, type CpraCode } from '../theme/tokens';
import { Icon } from './Icon';

export function CodeBadge({ code, label }: { code: CpraCode; label?: string }) {
  const text = label ?? CODE_LABELS[code];
  return (
    <span className={'code-badge ' + code} aria-label={text}>
      {code === 'blue' ? <Icon name="person" size={13} /> : code === 'cyan' ? <Icon name="clock" size={13} /> : <span className="status-dot" aria-hidden="true" />}
      {text}
    </span>
  );
}
