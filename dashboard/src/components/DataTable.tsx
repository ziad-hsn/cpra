import { useCallback, useRef } from 'react';
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from '@tanstack/react-table';
import { useVirtualizer } from '@tanstack/react-virtual';
import type { MonitorSummary } from '../api/types';
import { MonitorStatusLabels } from './MonitorStatusLabels';
import { formatMeasuredLatency, formatTime, formatUptime, uptimeColor } from '../lib/format';
import { CodeBadge } from './CodeBadge';
import type { CpraCode } from '../theme/tokens';

interface DataTableProps {
  data: MonitorSummary[];
  onRowClick: (id: number) => void;
  rowHeight?: number;
}

// Shared column template — header and every row MUST use the same grid
// so columns align perfectly across the virtualized, absolutely-positioned rows.
const COLS = 'minmax(220px, 2.6fr) 208px 128px 96px 144px 120px minmax(220px, 1.7fr)';
const MIN_TABLE_WIDTH = 1152;

export function DataTable({ data, onRowClick, rowHeight = 80 }: DataTableProps) {
  const columns: ColumnDef<MonitorSummary>[] = [
    {
      header: 'Monitor',
      accessorKey: 'name',
      cell: (info) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 1, minWidth: 0 }}>
          <span className='mono' style={{ fontWeight: 600, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{String(info.getValue() ?? '')}</span>
          {info.row.original.target && (
            <span className='muted mono' style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{info.row.original.target}</span>
          )}
        </div>
      ),
    },
    {
      header: 'Status',
      id: 'status',
      cell: (info) => <MonitorStatusLabels monitor={info.row.original} compact />,
    },
    {
      header: 'Health samples',
      id: 'uptime',
      cell: (info) => {
        const m = info.row.original;
        if (m.uptime === undefined || m.status === 'unknown') return <span className='muted'>—</span>;
        return <span style={{ fontWeight: 600, fontVariantNumeric: 'tabular-nums', color: uptimeColor(m.uptime) }}>{formatUptime(m.uptime)}</span>;
      },
    },
    {
      header: 'Latency',
      id: 'latency',
      cell: (info) => {
        const monitor = info.row.original;
        return <span className='mono' style={{ fontVariantNumeric: 'tabular-nums', color: 'var(--text-secondary)' }}>{formatMeasuredLatency(monitor.latency_available, monitor.latency_ms)}</span>;
      },
    },
    {
      header: 'Last check',
      accessorKey: 'last_check',
      cell: (info) => <span className='muted mono'>{formatTime(String(info.getValue()))}</span>,
    },
    {
      header: 'Pending notices',
      id: 'pending_notifications',
      cell: (info) => typeof info.row.original.pending_notifications === 'number'
        ? <span>{info.row.original.pending_notifications}</span> : <span className='muted'>Unavailable</span>,
    },
    {
      header: 'Configured notifications',
      id: 'active_codes',
      cell: (info) => {
        const codes = info.row.original.active_codes ?? [];
        if (codes.length === 0) return <span className='muted'>—</span>;
        return (
          <span style={{ display: 'inline-flex', gap: 5, flexWrap: 'wrap' }}>
            {codes.map((c) => <CodeBadge key={c} code={c as CpraCode} />)}
          </span>
        );
      },
    },
  ];

  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() });
  const parentRef = useRef<HTMLDivElement>(null);
  const { rows } = table.getRowModel();

  const getItemKey = useCallback((index: number) => `${data[index].monitor_id ?? data[index].id}:${data[index].incarnation_uid ?? ''}`, [data]);
  const virtualizer = useVirtualizer({
    count: rows.length,
    getItemKey,
    estimateSize: () => rowHeight,
    getScrollElement: () => parentRef.current,
    overscan: 16,
  });

  const totalHeight = virtualizer.getTotalSize();
  const items = virtualizer.getVirtualItems();

  return (
    <div ref={parentRef} style={{ height: '100%', overflow: 'auto', position: 'relative' }} role='table' aria-label='Monitors table' aria-rowcount={rows.length + 1} aria-colcount={columns.length} tabIndex={0}>
      {/* Sticky header — a real grid row, not a <table>, so it aligns exactly */}
      <div className='vtable-head' role='row' aria-rowindex={1} style={{ display: 'grid', gridTemplateColumns: COLS, minWidth: MIN_TABLE_WIDTH, position: 'sticky', top: 0, zIndex: 3 }}>
        {table.getFlatHeaders().map((h) => (
          <div key={h.id} className='vtable-th' role='columnheader'>
            {flexRender(h.column.columnDef.header, h.getContext())}
          </div>
        ))}
      </div>
      <div style={{ height: totalHeight, minWidth: MIN_TABLE_WIDTH, position: 'relative' }}>
        {items.map((virtualRow) => {
          const row = rows[virtualRow.index];
          return (
            <div
              key={getItemKey(virtualRow.index)}
              ref={virtualizer.measureElement}
              data-index={virtualRow.index}
              className='vtable-row'
              style={{
                position: 'absolute',
                top: 0, left: 0, width: '100%',
                minHeight: 48,
                transform: 'translateY(' + virtualRow.start + 'px)',
                display: 'grid',
                gridTemplateColumns: COLS,
                alignItems: 'center',
              }}
              onClick={() => onRowClick(row.original.id)}
              onKeyDown={(e) => { if (e.key === 'Enter') onRowClick(row.original.id); }}
              tabIndex={0}
              role='row'
              aria-rowindex={virtualRow.index + 2}
            >
              {row.getVisibleCells().map((cell, i) => (
                <div key={cell.id} role='cell' className={'vtable-td' + (i === row.getVisibleCells().length - 1 ? ' last' : '')}>
                  {flexRender(cell.column.columnDef.cell, cell.getContext())}
                </div>
              ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}
