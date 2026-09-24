import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { DashboardSession } from '../src/api/session';
import { MetricsView } from '../src/components/MetricsView';

const text = (body: string) => new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
async function fixture(permitted = true) {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response('{}', { status: 401 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ principalId: 'reader', role: 'reader', permissions: permitted ? ['GetMetrics'] : [] })));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('private-metrics-token');
  render(<SessionBoundary session={session}><MetricsView /></SessionBoundary>);
  return { session, fetcher };
}
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

it('loads only on explicit request and clears the bounded text on hide', async () => {
  const { fetcher } = await fixture();
  expect(fetcher).toHaveBeenCalledTimes(2);
  expect(screen.queryByRole('link')).not.toBeInTheDocument();
  expect(screen.getByText(/1 MiB display limit/)).toBeInTheDocument();
  fetcher.mockResolvedValueOnce(text('cpra_private_snapshot 42\n'));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  expect(await screen.findByRole('region', { name: 'Prometheus metrics output' })).toHaveTextContent('cpra_private_snapshot 42');
  vi.useFakeTimers();
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); window.dispatchEvent(new Event('focus')); });
  expect(fetcher).toHaveBeenCalledTimes(3);
  fireEvent.click(screen.getByRole('button', { name: 'Hide metrics' }));
  expect(screen.queryByText(/cpra_private_snapshot/)).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'View metrics' })).toBeInTheDocument();
});

it('uses text rendering without interpreting returned label markup as HTML', async () => {
  const { fetcher } = await fixture();
  fetcher.mockResolvedValueOnce(text('cpra_label{value="<img src=x onerror=alert(1)>"} 1\n'));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  const output = await screen.findByRole('region', { name: 'Prometheus metrics output' });
  expect(output.textContent).toContain('<img src=x onerror=alert(1)>');
  expect(output.querySelector('img')).toBeNull();
});

it('offers no metrics action without the exact permission', async () => {
  const { fetcher } = await fixture(false);
  expect(screen.queryByRole('button', { name: 'View metrics' })).not.toBeInTheDocument();
  expect(screen.getByText(/Metrics access is not available/)).toBeInTheDocument();
  expect(fetcher).toHaveBeenCalledTimes(2);
});

it('cancels an in-flight stream and ignores its abandoned result', async () => {
  const { fetcher } = await fixture();
  const cancel = vi.fn();
  fetcher.mockResolvedValueOnce(new Response(new ReadableStream({ cancel }), { headers: { 'Content-Type': 'text/plain' } }));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  await act(async () => { await Promise.resolve(); });
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  await act(async () => { await Promise.resolve(); });
  expect(cancel).toHaveBeenCalledOnce();
  expect(screen.queryByRole('region', { name: 'Prometheus metrics output' })).not.toBeInTheDocument();
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'View metrics' })).toBeEnabled();
});

it('clears displayed data on sign-out and never persists it or the token', async () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { session, fetcher } = await fixture();
  fetcher.mockResolvedValueOnce(text('old_identity_private_metric 1\n'));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  await screen.findByRole('region', { name: 'Prometheus metrics output' });
  act(() => session.signOut());
  expect(screen.queryByText(/old_identity_private_metric/)).not.toBeInTheDocument();
  expect(screen.getByRole('heading', { name: 'Sign in to CPRa' })).toBeInTheDocument();
  expect(storage).not.toHaveBeenCalled();
});

it('shows a sanitized rejection and retries only when explicitly requested', async () => {
  const { fetcher } = await fixture();
  fetcher.mockResolvedValueOnce(new Response('private-server-error', { status: 503 }));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Metrics request rejected (HTTP 503).');
  expect(screen.queryByText('private-server-error')).not.toBeInTheDocument();
  expect(fetcher).toHaveBeenCalledTimes(3);
  fetcher.mockResolvedValueOnce(text('new_metric 1\n'));
  fireEvent.click(screen.getByRole('button', { name: 'View metrics' }));
  expect(await screen.findByRole('region', { name: 'Prometheus metrics output' })).toHaveTextContent('new_metric 1');
  expect(fetcher).toHaveBeenCalledTimes(4);
});
