import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { DataTable } from '../src/components/DataTable';
import MonitorDetail from '../src/pages/MonitorDetail';
import { api } from '../src/api/client';
import type { MonitorSummary } from '../src/api/types';

const base: MonitorSummary = { id: 7, name: 'Payments', pulse_type: 'http', status: 'up', incident: false,
  pending_code: '', consecutive_failures: 0, last_check: '', last_success: '', next_check: '', active_codes: [] };

beforeEach(() => {
  // JSDOM has no layout. Supply the viewport size, retaining the real virtualizer.
  vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(400);
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(1100);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it.each([
  [true, 0, '0ms'], [true, 0.5, '<1ms'], [true, 12, '12ms'],
  [true, undefined, 'Unavailable'], [true, NaN, 'Unavailable'], [true, Infinity, 'Unavailable'],
  [true, -1, 'Unavailable'], [false, 0, 'Unavailable'], [false, 12, 'Unavailable'], [undefined, 12, 'Unavailable'],
] as const)('fleet reports availability=%s, latency=%s as %s', async (available, latency, expected) => {
  render(<DataTable data={[{ ...base, latency_available: available, latency_ms: latency }]} onRowClick={() => undefined} />);
  expect(await screen.findByRole('cell', { name: expected })).toBeVisible();
});

it('exposes the virtual table structure and supports activating a visible row with Enter', async () => {
  const open = vi.fn();
  render(<DataTable data={[base]} onRowClick={open} />);
  const table = screen.getByRole('table', { name: 'Monitors table' });
  expect(table).toHaveAttribute('aria-rowcount', '2');
  expect(within(table).getAllByRole('columnheader')).toHaveLength(7);
  const row = (await screen.findByRole('cell', { name: 'Payments' })).closest('[role="row"]')!;
  expect(row).toHaveAttribute('aria-rowindex', '2');
  expect(within(row as HTMLElement).getAllByRole('cell')).toHaveLength(7);
  fireEvent.keyDown(row, { key: 'Enter' });
  expect(open).toHaveBeenCalledExactlyOnceWith(7);
});

it.each([[0, '0ms'], [undefined, 'Unavailable']] as const)('numeric detail preserves latency %s', async (latency, expected) => {
  vi.spyOn(api, 'getMonitor').mockResolvedValue({ ...base, latency_available: true, latency_ms: latency });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  try {
    render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/monitors/7']}>
      <Routes><Route path="/monitors/:id" element={<MonitorDetail />} /></Routes>
    </MemoryRouter></QueryClientProvider>);
    const label = await screen.findByText('Latency');
    expect(label.parentElement).toHaveTextContent(`Latency${expected}`);
  } finally { client.clear(); }
});
