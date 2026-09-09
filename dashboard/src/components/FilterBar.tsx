import type { ChangeEvent } from 'react';

export interface FilterValues {
  status: string;
  type: string;
  code: string;
  q: string;
}

export function FilterBar({ filters, onChange, pulseTypes = [] }: { filters: FilterValues; onChange: (f: FilterValues) => void; pulseTypes?: string[] }) {
  const update = (key: keyof FilterValues) => (e: ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    onChange({ ...filters, [key]: e.target.value });
  };
  return (
    <div
      className="card"
      style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 12 }}
      role="search"
      aria-label="Monitor filters"
    >
      <input
        className="input"
        type="search"
        placeholder="Search by name…"
        value={filters.q}
        onChange={update('q')}
        aria-label="Search monitors"
        style={{ minWidth: 180 }}
      />
      <select className="input" value={filters.status} onChange={update('status')} aria-label="Filter by status">
        <option value="">All statuses</option>
        <option value="unknown">Unknown</option>
        <option value="up">Operational</option>
        <option value="down">Down</option>
	    <option value="degraded">Degraded</option>
        <option value="verifying">Verifying</option>
        <option value="incident">Incident</option>
      </select>
      <select className="input" value={filters.type} onChange={update('type')} aria-label="Filter by pulse type">
        <option value="">All types</option>
        {pulseTypes.map((type) => <option key={type} value={type}>{type.toUpperCase()}</option>)}
      </select>
      <select className="input" value={filters.code} onChange={update('code')} aria-label="Filter by code">
        <option value="">All codes</option>
        <option value="red">Red</option>
        <option value="yellow">Yellow</option>
        <option value="green">Green</option>
        <option value="cyan">Cyan</option>
        <option value="gray">Gray</option>
      </select>
    </div>
  );
}
