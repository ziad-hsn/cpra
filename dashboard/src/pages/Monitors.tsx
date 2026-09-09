import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useMonitors, useOverview } from '../hooks/queries';
import { DataTable } from '../components/DataTable';
import { FilterBar, type FilterValues } from '../components/FilterBar';
import { ErrorState } from '../components/ErrorState';
import { EmptyState } from '../components/EmptyState';
import { LoadingSkeleton } from '../components/LoadingSkeleton';
import { Icon } from '../components/Icon';
import { formatNumber } from '../lib/format';

const PAGE_SIZES = [25, 50, 100, 200];

export default function Monitors() {
  const navigate = useNavigate();
  const { data: overview } = useOverview();
  const [filters, setFilters] = useState<FilterValues>({ status: '', type: '', code: '', q: '' });
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(50);

  const { data, isLoading, isError, refetch } = useMonitors({
    status: filters.status || undefined,
    type: filters.type || undefined,
    code: filters.code || undefined,
    q: filters.q || undefined,
    page,
    size,
  });

  const monitors = data?.monitors ?? [];
  const total = data?.total ?? 0;
  const startIdx = total > 0 ? (page - 1) * size + 1 : 0;
  const endIdx = Math.min(page * size, total);

  const handleFilterChange = (f: FilterValues) => {
    setFilters(f);
    setPage(1);
  };

  const handleSizeChange = (newSize: number) => {
    setSize(newSize);
    setPage(1);
  };

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Monitor Fleet</h1>
          <div className="lead">Browse active monitors in the current snapshot</div>
        </div>
      </div>

      <FilterBar filters={filters} onChange={handleFilterChange} pulseTypes={Object.keys(overview?.by_pulse_type ?? {}).sort()} />

      {overview?.index_capped ? (
        <div className="card">
          <EmptyState title="Monitor details unavailable" message="This fleet exceeds the snapshot index limit. Aggregate counts are available on the overview page." />
        </div>
      ) : isError ? (
        <ErrorState message="Failed to load monitors" onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="card">
          <LoadingSkeleton lines={8} />
        </div>
      ) : monitors.length === 0 ? (
        <div className="card">
          <EmptyState title="No monitors found" message={total === 0 ? 'No monitors match your filters.' : undefined} />
        </div>
      ) : (
        <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
          <div style={{ height: 'calc(100vh - 280px)', minHeight: 300 }}>
            <DataTable data={monitors} onRowClick={(id) => navigate(`/monitors/${id}`)} rowHeight={36} />
          </div>
        </div>
      )}

      {/* Pagination footer */}
      {!isLoading && !isError && !overview?.index_capped && <div className="pager">
        <span className="info">
          Showing {formatNumber(startIdx)}–{formatNumber(endIdx)} of {formatNumber(total)}
        </span>
        <label htmlFor="size-select" style={{ fontSize: 12 }}>Page size:</label>
        <select
          id="size-select"
          className="input"
          value={size}
          onChange={(e) => handleSizeChange(Number(e.target.value))}
          aria-label="Results per page"
        >
          {PAGE_SIZES.map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <button className="btn" onClick={() => setPage((p) => Math.max(1, p - 1))} disabled={page <= 1} aria-label="Previous page">
          <Icon name="chevron-left" size={14} /> Prev
        </button>
        <span style={{ fontSize: 12, color: 'var(--text-muted)' }}>Page {page}</span>
        <button className="btn" onClick={() => setPage((p) => p + 1)} disabled={endIdx >= total} aria-label="Next page">
          Next <Icon name="chevron-right" size={14} />
        </button>
      </div>}
    </div>
  );
}
