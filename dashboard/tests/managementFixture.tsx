import { vi } from 'vitest';
import { render } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { DashboardSession } from '../src/api/session';
import { SessionBoundary } from '../src/auth/SessionBoundary';
import OperationDetail from '../src/pages/OperationDetail';
import ManagementResources from '../src/pages/ManagementResources';
import { resourceKinds, type ResourceType } from '../src/api/resources';
import { notificationFields, checkFields, recoveryFields, operationContracts, type Operation } from '../src/api/generated';

export type Resource = { apiVersion: string; kind: string; metadata: { id: string; name: string; uid: string; resourceVersion: string }; spec: Record<string, unknown>; status?: Record<string, unknown> };
const response = (data: unknown, status = 200, headers: HeadersInit = {}) => new Response(JSON.stringify(data), { status, headers });
export function resource(type: ResourceType, id: string, spec: Record<string, unknown>): Resource {
  return { apiVersion: 'cpra.io/v2', kind: resourceKinds[type].kind, metadata: { id, name: id, uid: `uid-${id}`, resourceVersion: 'rv-1' }, spec };
}

export async function fixture(type: ResourceType, id?: string, options: { reader?: boolean; initial?: Resource[]; failWrite?: boolean; permissions?: string[]; drivers?: Record<string, string[]>; summaries?: boolean; lostResponseBody?: boolean } = {}) {
  const records = new Map<string, Resource>((options.initial ?? []).map(item => [`${item.kind}:${item.metadata.id}`, item]));
  const operations = new Map<string, Operation>();
  const writes: { method: string; path: string; body: Record<string, unknown> | undefined; match: string | null }[] = [];
  const permissions = options.permissions ?? Object.keys(operationContracts).filter(operation => !options.reader || operation.startsWith('Get') || operation.startsWith('List'));
  const fetcher = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    const headers = new Headers(init?.headers);
    if (url.pathname === '/api/v2/self') return headers.has('Authorization') ? response({ principalId: 'alice', role: options.reader ? 'reader' : 'operator', permissions }) : response({}, 401);
    if (url.pathname.startsWith('/api/v2/operations/')) { const operation = operations.get(decodeURIComponent(url.pathname.slice('/api/v2/operations/'.length))); return operation ? response(operation) : response({}, 404); }
    if (url.pathname === '/api/v2/discovery') return response({ apiVersions: ['cpra.io/v2'], drivers: options.drivers ?? { notification: Object.keys(notificationFields), check: Object.keys(checkFields), recovery: Object.keys(recoveryFields) }, resources: [], patchTypes: ['application/merge-patch+json'] });
    const definition = Object.values(resourceKinds).find(value => url.pathname === value.path || url.pathname.startsWith(`${value.path}/`));
    if (!definition) return response({}, 404);
    const itemID = decodeURIComponent(url.pathname.slice(definition.path.length + 1));
    const key = `${definition.kind}:${itemID}`;
    if (method === 'GET') return itemID ? records.has(key) ? response(records.get(key)) : response({}, 404)
      : response({ items: [...records.values()].filter(item => item.kind === definition.kind).map(item => options.summaries ? { ...item, spec: {} } : item), snapshot: 'page-snapshot' });
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) as Record<string, unknown> : undefined;
    writes.push({ method, path: url.pathname, body, match: headers.get('If-Match') });
    if (options.failWrite) throw new TypeError('lost response');
    const makeReceipt = (id: string, oldVersion?: string): Operation => {
      const operation: Operation = { id: `operation-${writes.length}`, state: 'committed', contentDigest: '0'.repeat(64), committed: 1, applied: 0, validated: true,
        items: [{ id, outcome: 'committed', committed: true, applied: false, oldVersion, newVersion: 'rv-next' }] };
      operations.set(operation.id, operation); return operation;
    };
    const writeResponse = (value: unknown, status: number, headers: HeadersInit) => options.lostResponseBody ? new Response(new ReadableStream({ start(controller) { controller.error(new Error('private interrupted response')); } }), { status, headers }) : response(value, status, headers);

    if (method === 'POST') {
      if (headers.get('If-None-Match') !== '*') return response({ code: 'preconditionRequired' }, 428);
      const incoming = body as unknown as Resource;
      const created = { ...incoming, metadata: { ...incoming.metadata, uid: 'new-uid', resourceVersion: 'rv-new' }, status: { available: true, applied: true } };
      records.set(`${definition.kind}:${created.metadata.id}`, created);
      // Deliberately echo the request in this fixture: browser feedback/cache must still strip the value.
      const operation = makeReceipt(created.metadata.id);
      return writeResponse(created, 201, { ETag: '"rv-new"', 'X-Resource-Version': 'rv-new', 'X-Operation-ID': operation.id });
    }
    const current = records.get(key);
    if (!current) return response({}, 404);
    if (headers.get('If-Match') !== `"${current.metadata.resourceVersion}"`) return response({ code: 'conflict', errors: [{ field: 'metadata.resourceVersion', message: 'changed' }] }, 412);
    if (method === 'DELETE') {
      if (itemID === 'referenced') return response({ code: 'referenced', errors: [{ field: 'metadata.id', message: 'used by a monitor' }] }, 409);
      records.delete(key); const operation = makeReceipt(itemID, current.metadata.resourceVersion);
      return writeResponse(operation, 200, { 'X-Operation-ID': operation.id });
    }
    if (method === 'PATCH' && body?.metadata && Object.keys(body.metadata as object).some(key => !['name', 'labels'].includes(key))) return response({ code: 'validationFailed' }, 422);
    const next = method === 'PATCH' ? { ...current, ...body, metadata: { ...current.metadata, ...body?.metadata as object }, spec: { ...current.spec, ...body?.spec as object } } : body as unknown as Resource;
    next.metadata.resourceVersion = 'rv-next';
    records.set(key, next);
    const operation = makeReceipt(itemID, current.metadata.resourceVersion);
    return writeResponse(next, 200, { ETag: '"rv-next"', 'X-Resource-Version': 'rv-next', 'X-Operation-ID': operation.id });
  });
  const session = new DashboardSession({ origin: 'https://cpra.example', fetch: fetcher });
  await session.discover(); await session.signIn('test-token');
  const route = resourceKinds[type].route;
  const router = createMemoryRouter([{ path: `${route}/:id?`, element: <SessionBoundary session={session}><ManagementResources type={type} /></SessionBoundary> }, { path: '/operations/:id?', element: <SessionBoundary session={session}><OperationDetail /></SessionBoundary> }, { path: '/elsewhere', element: <p>Elsewhere</p> }], { initialEntries: [id === 'new' ? `${route}?create=1` : `${route}${id ? `/${id}` : ''}`] });
  render(<RouterProvider router={router} />);
  return { session, fetcher, records, writes, router, operations };
}
