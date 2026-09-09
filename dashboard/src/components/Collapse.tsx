import { useState, type ReactNode } from 'react';
import { Icon } from './Icon';

interface CollapseProps {
  title: string;
  subtitle?: string;
  defaultOpen?: boolean;
  badge?: ReactNode;
  children: ReactNode;
}

export function Collapse({ title, subtitle, defaultOpen = false, badge, children }: CollapseProps) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className='card' style={{ padding: 0, overflow: 'hidden' }}>
      <button
        className='collapse-head'
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        style={{
          width: '100%', display: 'flex', alignItems: 'center', gap: 12,
          padding: '16px 18px', background: 'transparent', border: 'none', cursor: 'pointer',
          color: 'inherit', textAlign: 'left',
        }}
      >
        <span className={'collapse-chev' + (open ? ' open' : '')}><Icon name='chevron-down' size={16} /></span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className='card-title'>{title}</div>
          {subtitle && <div className='card-sub'>{subtitle}</div>}
        </div>
        {badge}
      </button>
      {open && <div style={{ padding: '0 18px 18px' }}>{children}</div>}
    </div>
  );
}
