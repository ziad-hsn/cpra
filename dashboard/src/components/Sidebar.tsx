import { NavLink } from 'react-router-dom';
import { useState } from 'react';
import { Icon, type IconName } from './Icon';

const NAV_ITEMS: { to: string; label: string; icon: IconName; end?: boolean }[] = [
  { to: '/', label: 'Overview', icon: 'overview', end: true },
  { to: '/monitors', label: 'Monitors', icon: 'monitors' },
  { to: '/alerts', label: 'Alerts', icon: 'alerts' },
  { to: '/system', label: 'System Health', icon: 'system' },
  { to: '/settings', label: 'Settings', icon: 'settings' },
];

export function Sidebar() {
  const [collapsed, setCollapsed] = useState(false);
  return (
    <aside className={`sidebar${collapsed ? ' collapsed' : ''}`} aria-label="Primary navigation">
      <div className="sidebar-head">
        <div className="brand">
          <span className="brand-mark"><Icon name="pulse" size={18} strokeWidth={2.2} /></span>
          <span className="brand-text">
            <span className="name">CPRA</span>
            <span className="tag">Monitoring</span>
          </span>
        </div>
        <button
          className="sidebar-collapse"
          onClick={() => setCollapsed((c) => !c)}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        >
          <Icon name="chevron-left" size={14} />
        </button>
      </div>

      <nav className="sidebar-nav">
        <div className="nav-section-label">Workspace</div>
        {NAV_ITEMS.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.end}
            className={({ isActive }) => `nav-item${isActive ? ' active' : ''}`}
            title={item.label}
          >
            <Icon name={item.icon} size={18} className="nav-icon" />
            <span className="nav-label">{item.label}</span>
          </NavLink>
        ))}
      </nav>

      <div className="sidebar-foot">
        <div className="sidebar-foot-row">
          <span className="sidebar-foot-label">Monitoring dashboard</span>
        </div>
      </div>
    </aside>
  );
}
