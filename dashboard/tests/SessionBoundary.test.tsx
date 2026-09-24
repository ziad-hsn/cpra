import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { onlineManager, useQuery, useQueryClient } from '@tanstack/react-query';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary, useDashboardSession } from '../src/auth/SessionBoundary';
import { useManagementMutation } from '../src/hooks/mutations';

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
afterEach(() => { cleanup(); onlineManager.setOnline(true); vi.restoreAllMocks(); });

it('keeps the data view unmounted before sign-in and clears the token input immediately on submit', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, 401))
    .mockResolvedValueOnce(json({ principalId: 'alice', role: 'reader', permissions: ['GetMonitor'] }));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  render(<SessionBoundary session={session}><p>Protected observations</p></SessionBoundary>);
  expect(await screen.findByRole('heading', { name: 'Sign in to CPRa' })).toBeInTheDocument();
  expect(screen.queryByText('Protected observations')).not.toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Bearer token'), { target: { value: 'opaque-token' } });
  fireEvent.submit(screen.getByRole('form', { name: 'Dashboard sign-in' }));
  await waitFor(() => expect(screen.getByText('Protected observations')).toBeInTheDocument());
  expect(screen.getByText('alice · Read-only access')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }));
  expect(screen.queryByText('Protected observations')).not.toBeInTheDocument();
  expect(screen.getByLabelText('Bearer token')).toHaveValue('');
});

it('uses an empty query cache for a replacement identity and drops late old data', async () => {
  let resolveOld!: (response: Response) => void;
  const fetcher = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(json({}, 401))
    .mockResolvedValueOnce(json({ principalId: 'alice', role: 'reader', permissions: ['GetMonitor'] }))
    .mockImplementationOnce(() => new Promise<Response>(resolve => { resolveOld = resolve; }))
    .mockResolvedValueOnce(json({ principalId: 'bob', role: 'reader', permissions: ['GetMonitor'] }))
    .mockResolvedValueOnce(json({ owner: 'bob' }));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  const clients: ReturnType<typeof useQueryClient>[] = [];
  function Observations() {
    const current = useDashboardSession();
    const client = useQueryClient();
    if (!clients.includes(client)) clients.push(client);
    const result = useQuery({ queryKey: ['resource'], queryFn: ({ signal }) => current.get<{ owner: string }>('/api/v2/monitors/first', { signal }), retry: false });
    return <p>{result.data?.data.owner ?? 'Waiting for observations'}</p>;
  }
  render(<SessionBoundary session={session}><Observations /></SessionBoundary>);
  await screen.findByRole('heading', { name: 'Sign in to CPRa' });
  await act(async () => { await session.signIn('alice-token'); });
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
  await act(async () => { session.signOut(); await session.signIn('bob-token'); });
  expect(await screen.findByText('bob')).toBeInTheDocument();
  await act(async () => { resolveOld(json({ owner: 'alice-private-data' })); });
  expect(screen.queryByText('alice-private-data')).not.toBeInTheDocument();
  expect(clients).toHaveLength(2);
  expect(clients[0].getQueryCache().getAll()).toHaveLength(0);
  expect(JSON.stringify(clients[1].getQueryData(['resource']))).not.toContain('alice');
  expect(clients[1].getDefaultOptions().mutations).toMatchObject({ retry: false, networkMode: 'always', gcTime: 0 });
});

it('preserves the observation view when the server has only v1', async () => {
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, 404)) });
  render(<SessionBoundary session={session}><p>Legacy observations</p></SessionBoundary>);
  expect(await screen.findByText('Legacy observations')).toBeInTheDocument();
  expect(screen.getByText(/Read-only server/)).toBeInTheDocument();
  expect(screen.queryByLabelText('Bearer token')).not.toBeInTheDocument();
});

it('does not queue an offline mutation or retain its secret variables in the query cache', async () => {
  const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(json({}, 401))
    .mockResolvedValueOnce(json({ principalId: 'alice', role: 'operator', permissions: ['CreateCredential'] }))
    .mockRejectedValueOnce(new TypeError('offline'));
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  let queryClient!: ReturnType<typeof useQueryClient>;
  function Editor() {
    queryClient = useQueryClient();
    const mutation = useManagementMutation();
    return <><button onClick={() => void mutation.execute('/api/v2/credentials', {
      method: 'POST', operation: 'CreateCredential', body: { value: 'test-secret-never-cached' },
    }).catch(() => undefined)}>Save credential</button><p>{mutation.error?.reason}</p></>;
  }
  render(<SessionBoundary session={session}><Editor /></SessionBoundary>);
  await screen.findByRole('heading', { name: 'Sign in to CPRa' });
  await act(async () => { await session.signIn('alice-token'); });
  onlineManager.setOnline(false);
  fireEvent.click(screen.getByRole('button', { name: 'Save credential' }));
  expect(await screen.findByText('unconfirmed')).toBeInTheDocument();
  expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
  await act(async () => { onlineManager.setOnline(true); });
  expect(fetcher).toHaveBeenCalledTimes(3);
});
