import { afterEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import { MonitorActions } from '../src/components/MonitorActions';
import { operationContracts } from '../src/api/generated';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

async function actions(options: { reader?: boolean; unfenced?: boolean; conflict?: boolean; lost?: boolean; foreign?: boolean; paged?: boolean; reviewedConflict?: boolean } = {}) {
  const action: Record<string, unknown> = { id: 'execution-1', monitorID: 'checkout', incarnationUID: 'original-monitor-uid', state: 'unknown', kind: 'intervention', reviewRevision: 'observed-1', held: true, executorFenced: !options.unfenced,
    ...(options.reviewedConflict ? { review: { revision: 'review-old', resolution: 'rejected', actor: 'alice', reviewedAt: '2026-09-14T00:00:00Z', reason: 'Provider records checked', note: '<script>not executable</script>', evidenceRefs: ['javascript:alert(1)'], conflict: true } } : {}) };
  const writes: { method: string; path: string; match: string | null; body: Record<string, unknown> }[] = [];
  const permissions = Object.keys(operationContracts).filter(name => !options.reader || name.startsWith('Get') || name.startsWith('List'));
  const reply = (body: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(body), { status, headers });
  const reads: string[] = [];
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (url.pathname === '/api/v2/discovery') return reply({ apiVersions: ['cpra.io/v2'], resources: [], patchTypes: ['application/merge-patch+json'], drivers: { check: ['tcp'], recovery: ['docker'], notification: [] } });
    if (url.pathname === '/api/v2/self') return reply({ principalId: 'alice', role: options.reader ? 'reader' : 'operator', permissions });
    if (method === 'GET') {
      reads.push(url.pathname + url.search);
      if (url.pathname === '/api/v2/actions') return reply({ items: [{ ...action, ...(options.foreign ? { monitorID: 'another-monitor' } : {}), ...(url.searchParams.has('cursor') ? { id: 'execution-2' } : {}) }], ...(options.paged && !url.searchParams.has('cursor') ? { nextCursor: 'frozen-next-page' } : {}) });
      if (url.pathname === '/api/v2/actions/execution-1') return reply(action);
      if (url.pathname === '/api/v2/operations/review-operation') return reply({ id: 'review-operation', state: 'committed', contentDigest: '0'.repeat(64), committed: 1, applied: 0, validated: true });
      return reply({}, 404);
    }
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    writes.push({ method, path: url.pathname, match: new Headers(init?.headers).get('If-Match'), body });
    if (options.conflict) return reply({ code: 'conflict' }, 412);
    if (options.lost) return new Response(new ReadableStream({ start(controller) { controller.error(new Error('private interrupted response')); } }), { status: 200, headers: { 'X-Operation-ID': 'review-operation' } });
    action.review = { revision: 'review-operation', actor: 'alice', reviewedAt: '2026-09-14T00:00:00Z', resolution: body.resolution, reason: body.reason, note: body.note, evidenceRefs: body.evidenceRefs };
    action.held = body.resolution === 'inconclusive';
    return reply(action, 200, { 'X-Operation-ID': 'review-operation' });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('test-token');
  render(<MemoryRouter><SessionBoundary session={session}><MonitorActions monitorID="checkout" /></SessionBoundary></MemoryRouter>);
  await screen.findByRole('heading', { name: 'Action outcomes' });
  await waitFor(() => expect(reads.some(path => path.startsWith('/api/v2/actions?'))).toBe(true));
  return { action, writes, session, reads, fetcher };
}

async function reviewDialog() {
  fireEvent.click(await screen.findByRole('button', { name: 'Review action execution-1' }));
  return screen.findByRole('dialog', { name: 'Review unknown action' });
}

it('reviews the freshly fetched frozen observation without replacing provider facts or trusting an actor input', async () => {
  const { action, writes, reads } = await actions();
  action.reviewRevision = 'observed-before-dialog';
  const dialog = await reviewDialog();
  expect(reads).toContain('/api/v2/actions/execution-1');
  action.reviewRevision = 'observed-after-dialog';
  fireEvent.change(within(dialog).getByLabelText('Conclusion'), { target: { value: 'accepted' } });
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Operator checked provider receipt' } });
  fireEvent.change(within(dialog).getByLabelText(/^Evidence references/), { target: { value: 'provider-record-123\noperator-ticket-44' } });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Record review' }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]).toEqual({ method: 'POST', path: '/api/v2/actions/execution-1/review', match: '"observed-before-dialog"', body: { revision: 'observed-before-dialog', resolution: 'accepted', reason: 'Operator checked provider receipt', evidenceRefs: ['provider-record-123', 'operator-ticket-44'] } });
  expect(await screen.findByText(/Operator review: accepted · alice/)).toBeInTheDocument();
  expect(screen.getByText(/Provider outcome: unknown/)).toHaveTextContent('No investigation hold.');
  expect(writes[0].body).not.toHaveProperty('actor');
  expect(screen.queryByRole('button', { name: /replay|retry action|check now/i })).not.toBeInTheDocument();
});

it('permits only an inconclusive review while the original executor is unfenced', async () => {
  const { writes } = await actions({ unfenced: true });
  const dialog = await reviewDialog();
  expect(within(dialog).getByRole('option', { name: /^Accepted/ })).toBeDisabled();
  expect(within(dialog).getByRole('option', { name: /^Rejected/ })).toBeDisabled();
  const submit = within(dialog).getByRole('button', { name: 'Record review' });
  expect(submit).toBeDisabled();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Still investigating the remote execution' } });
  fireEvent.click(submit);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body.resolution).toBe('inconclusive');
  expect(await screen.findByText(/Provider outcome: unknown/)).toHaveTextContent('Held for investigation.');
});

it('keeps a conflicted request on the original review version without retrying it', async () => {
  const { writes } = await actions({ conflict: true });
  const dialog = await reviewDialog();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Reviewed receipt' } });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Record review' }));
  expect(await screen.findByRole('alert')).toHaveTextContent(/changed|conflict/i);
  expect(writes).toHaveLength(1);
  expect(writes[0].match).toBe('"observed-1"');
  expect(screen.getByRole('dialog')).toBeInTheDocument();
});

it('retains the original operation and prevents re-submission after a lost review response', async () => {
  const { writes } = await actions({ lost: true });
  const dialog = await reviewDialog();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Reviewed receipt' } });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Record review' }));
  expect(await screen.findByRole('link', { name: 'Inspect submitted review' })).toHaveAttribute('href', '/operations/review-operation');
  expect(within(dialog).getByRole('button', { name: 'Record review' })).toBeDisabled();
  expect(writes).toHaveLength(1);
});

it('shows conflicting late evidence and renders notes and evidence references as inert text', async () => {
  const { writes } = await actions({ reviewedConflict: true });
  expect(await screen.findByRole('alert')).toHaveTextContent('Later provider evidence conflicts');
  expect(screen.getByText(/<script>not executable<\/script>/)).toBeInTheDocument();
  expect(screen.getByText(/javascript:alert\(1\)/)).toBeInTheDocument();
  expect(document.querySelector('script, a[href^="javascript:"]')).toBeNull();
  expect(writes).toHaveLength(0);
});

it('rejects a foreign monitor page and makes no fallback read or mutation', async () => {
  const { writes, reads } = await actions({ foreign: true });
  expect(await screen.findByRole('alert')).toHaveTextContent('Action outcomes are unavailable');
  expect(screen.queryByRole('button', { name: /^Review action/ })).not.toBeInTheDocument();
  expect(reads.some(path => path.startsWith('/api/v1'))).toBe(false);
  expect(writes).toHaveLength(0);
});

it('keeps reader observations available without review controls', async () => {
  const { writes } = await actions({ reader: true });
  expect(await screen.findByText(/Provider outcome: unknown/)).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^Review action/ })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('reads only the requested action page with the monitor filter and frozen cursor', async () => {
  const { reads, writes } = await actions({ paged: true });
  const next = await screen.findByRole('button', { name: 'Next actions' });
  expect(reads.filter(path => path.startsWith('/api/v2/actions?'))).toHaveLength(1);
  fireEvent.click(next);
  expect(await screen.findByRole('button', { name: 'Review action execution-2' })).toBeInTheDocument();
  expect(reads).toContain('/api/v2/actions?monitorID=checkout&limit=100&cursor=frozen-next-page');
  expect(screen.queryByRole('button', { name: 'Review action execution-1' })).not.toBeInTheDocument();
  expect(writes).toHaveLength(0);
});

it('bounds review text and unique evidence references before sending', async () => {
  const { writes } = await actions();
  const dialog = await reviewDialog();
  const reason = within(dialog).getByLabelText('Reason');
  const refs = within(dialog).getByLabelText(/^Evidence references/);
  const submit = within(dialog).getByRole('button', { name: 'Record review' });
  fireEvent.change(reason, { target: { value: 'م'.repeat(2049) } });
  expect(submit).toBeDisabled();
  fireEvent.change(reason, { target: { value: 'Provider evidence checked' } });
  for (const evidence of ['duplicate\nduplicate', Array.from({ length: 9 }, (_, i) => `ref-${i}`).join('\n'), 'م'.repeat(1025), 'tab\treference']) {
    fireEvent.change(refs, { target: { value: evidence } });
    expect(submit).toBeDisabled();
  }
  expect(writes).toHaveLength(0);
});

it('discards a review draft on sign-out', async () => {
  const { writes, session } = await actions();
  const dialog = await reviewDialog();
  fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'Private draft in this tab' } });
  act(() => session.signOut());
  expect(await screen.findByRole('form', { name: 'Dashboard sign-in' })).toBeInTheDocument();
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(document.body.textContent).not.toContain('Private draft in this tab');
  expect(writes).toHaveLength(0);
});
