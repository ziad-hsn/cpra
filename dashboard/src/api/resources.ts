import type { Credential, Metadata, Monitor, NotificationEndpoint, NotificationGroup, Recipient } from './generated';
import { notificationFields, checkFields, recoveryFields, monitorShapes, operationContracts } from './generated';
import { DashboardSession, ManagementError, type APIResponse } from './session';
import { operationID, operationView } from './operations';
import { object } from './value';
export { object } from './value';

export const resourceKinds = {
  monitors: { kind: 'Monitor', singular: 'Monitor', title: 'Monitor configurations', path: '/api/v2/monitors', route: '/monitor-configurations', operations: 'Monitor', listOperation: 'ListMonitors', description: 'Manage saved check, recovery and notification settings. Saved configuration and controller observations have separate versions; a saved change may still be awaiting application.' },
  recipients: { kind: 'Recipient', singular: 'Recipient', title: 'Recipients', path: '/api/v2/recipients', route: '/recipients', operations: 'Recipient', listOperation: 'ListRecipients', description: 'Notification contacts collect a person’s delivery endpoints. Contacts are separate from dashboard access.' },
  endpoints: { kind: 'NotificationEndpoint', singular: 'Notification endpoint', title: 'Notification endpoints', path: '/api/v2/notification-endpoints', route: '/notification-endpoints', operations: 'NotificationEndpoint', listOperation: 'ListNotificationEndpoints', description: 'Configure destinations using built-in notification drivers. A monitor’s Code selects the delivery type; its recipients and groups supply matching destinations.' },
  groups: { kind: 'NotificationGroup', singular: 'Notification group', title: 'Notification groups', path: '/api/v2/notification-groups', route: '/notification-groups', operations: 'NotificationGroup', listOperation: 'ListNotificationGroups', description: 'Groups contain recipients, direct endpoints, or both. Groups cannot contain other groups. Legacy direct-endpoint groups keep their existing delivery behavior.' },
  credentials: { kind: 'Credential', singular: 'Secret', title: 'Secrets', path: '/api/v2/credentials', route: '/secrets', operations: 'Credential', listOperation: 'ListCredentials', description: 'Secret values are write-only. Protected values are encrypted before durable admission; provider credentials are separate from dashboard bearer tokens.' },
} as const;

export type ResourceType = keyof typeof resourceKinds;
export type ManagedResource = Monitor | Recipient | NotificationEndpoint | NotificationGroup | Credential;
export type ResourceView = { resource: ManagedResource; unsupported?: string };
export type ResourcePage = { items: ResourceView[]; nextCursor?: string; snapshot?: string };
export type FieldDescriptor = { readonly key: string; readonly label: string; readonly type: string; readonly protected: boolean };

export type DriverCategory = 'check' | 'recovery' | 'notification';
const driverFields = { check: checkFields, recovery: recoveryFields, notification: notificationFields };
export function fieldsFor(driver: string, category: DriverCategory = 'notification'): readonly FieldDescriptor[] | undefined {
  const fields: Record<string, readonly FieldDescriptor[]> = driverFields[category];
  return Object.prototype.hasOwnProperty.call(fields, driver) ? fields[driver] : undefined;
}

export function protectedHTTPURL(value: unknown): boolean {
  if (typeof value !== 'string') return true;
  if (!value) return false;
  try {
    const url = new URL(value);
    return !!(url.username || url.password || url.search || url.hash);
  } catch { return true; }
}

/** A safe, editable projection; unsupported inputs are never silently replaced. */
export function driverView(source: Record<string, unknown>, category: DriverCategory): { value: Record<string, unknown>; unsupported?: string } {
  const driver = typeof source.type === 'string' ? source.type : 'unsupported';
  const fields = fieldsFor(driver, category);
  const config: Record<string, unknown> = {};
  const refs: Record<string, string> = {};
  let unsupported: string | undefined;
  if (!fields || !object(source.config)) unsupported = 'This driver configuration is not supported by this dashboard. It can still be observed.';
  else for (const [key, value] of Object.entries(source.config)) {
    const field = fields.find(candidate => candidate.key === key);
    if (!field) { unsupported = 'This driver has fields that this dashboard cannot edit safely.'; continue; }
    if (field.protected || (category === 'check' && driver === 'http' && key === 'url' && protectedHTTPURL(value))) {
      unsupported = 'This configuration contains older inline credentials or a protected URL. Migrate them to secret references before editing.'; continue;
    }
    const valid = field.type === 'string-list' ? Array.isArray(value) && value.every(item => typeof item === 'string')
      : field.type === 'number-list' ? Array.isArray(value) && value.every(item => typeof item === 'number' && Number.isSafeInteger(item))
        : field.type === 'number' ? typeof value === 'number' && Number.isSafeInteger(value)
          : field.type === 'duration' ? typeof value === 'string'
            : typeof value === field.type;
    if (!valid) { unsupported = 'This driver contains values with an unsupported format.'; continue; }
    config[key] = structuredClone(value);
  }
  if (source.credentialRefs !== undefined) {
    if (!object(source.credentialRefs)) unsupported = 'This driver contains unsupported secret references.';
    else for (const [key, ref] of Object.entries(source.credentialRefs)) {
      if (!fields?.some(field => field.key === key) || typeof ref !== 'string') { unsupported = 'This driver has secret slots that this dashboard cannot edit safely.'; continue; }
      refs[key] = ref;
      if (Object.prototype.hasOwnProperty.call(config, key)) unsupported = 'A driver field contains both a value and a secret reference. Resolve this conflict before editing.';
    }
  }
  if (Object.keys(source).some(key => !['type', 'config', 'credentialRefs'].includes(key))) unsupported = 'This driver has fields that this dashboard cannot edit safely.';
  return { value: { type: driver, config, ...(source.credentialRefs !== undefined ? { credentialRefs: refs } : {}) }, unsupported };
}

type Shape = { $ref?: string; type?: string; properties?: Record<string, Shape>; additionalProperties?: Shape | boolean; items?: Shape; required?: readonly string[] };
const shapes: Record<string, Shape> = monitorShapes;

function monitorProjection(input: unknown, name: string): { value: Record<string, unknown>; unsupported?: string } {
  let unsupported: string | undefined;
  const invalid = () => { unsupported ??= 'This monitor contains fields or values that this dashboard cannot edit safely. Supported observations remain available.'; };
  const project = (value: unknown, shape: Shape, path: string[]): unknown => {
    if (shape.$ref) {
      const ref = shape.$ref.slice('#/components/schemas/'.length);
      if (ref === 'DriverConfig') {
        if (!object(value)) { invalid(); return {}; }
        const result = driverView(value, path[0] === 'check' ? 'check' : path[0] === 'recovery' ? 'recovery' : 'notification');
        unsupported = result.unsupported ?? unsupported;
        return result.value;
      }
      if (!shapes[ref]) { invalid(); return undefined; }
      return project(value, shapes[ref], path);
    }
    if (shape.type === 'object') {
      if (!object(value)) { invalid(); return {}; }
      const result: Record<string, unknown> = {};
      for (const required of shape.required ?? []) if (!Object.prototype.hasOwnProperty.call(value, required)) invalid();
      for (const [key, child] of Object.entries(value)) {
        const property = Object.prototype.hasOwnProperty.call(shape.properties ?? {}, key) ? shape.properties![key] : typeof shape.additionalProperties === 'object' ? shape.additionalProperties : undefined;
        if (!property) { invalid(); continue; }
        // JSON object keys are data, including '__proto__'; never assign them
        // through an object's inherited setter while projecting server input.
        Object.defineProperty(result, key, { value: project(child, property, [...path, key]), enumerable: true, writable: true, configurable: true });
      }
      return result;
    }
    if (shape.type === 'array') {
      if (!Array.isArray(value) || !shape.items) { invalid(); return []; }
      return value.map((item, index) => project(item, shape.items!, [...path, String(index)]));
    }
    if (shape.type === 'integer' || shape.type === 'number') {
      if (typeof value === 'number' && (shape.type === 'integer' ? Number.isSafeInteger(value) : Number.isFinite(value))) return value;
    } else if (typeof value === shape.type) return value;
    invalid(); return undefined;
  };
  const value = project(input, shapes[name], []) as Record<string, unknown>;
  return { value, unsupported };
}

/** Keep write-only values out of query caches even if an incompatible server echoes them. */
export function resourceView(value: unknown, type: ResourceType): ResourceView {
  if (!object(value) || value.kind !== resourceKinds[type].kind || !object(value.metadata) || typeof value.metadata.id !== 'string' || !object(value.spec)) {
    throw new ManagementError('invalid', 'The server returned an unsupported resource.');
  }
  const source = value.spec;
  const spec: Record<string, unknown> = {};
  let unsupported: string | undefined;
  if (value.apiVersion !== 'cpra.io/v2' || Object.keys(value.metadata).some(key => !['id', 'name', 'uid', 'resourceVersion', 'generation', 'labels'].includes(key))) {
    unsupported = 'This resource uses a version or metadata fields that this dashboard cannot edit safely.';
  }
  if (type === 'credentials') {
    if (typeof source.description === 'string') spec.description = source.description;
    if (Object.keys(source).some(key => !['description', 'value'].includes(key))) unsupported = 'This secret has fields that this dashboard cannot edit safely.';
  } else if (type === 'recipients' || type === 'groups') {
    for (const key of type === 'groups' ? ['endpointRefs', 'recipientRefs'] : ['endpointRefs']) {
      if (source[key] === undefined) continue;
      if (!Array.isArray(source[key]) || !(source[key] as unknown[]).every(item => typeof item === 'string')) unsupported = 'This resource uses an unsupported reference format.';
      else spec[key] = [...source[key]];
    }
    const allowed = type === 'groups' ? ['endpointRefs', 'recipientRefs'] : ['endpointRefs'];
    if (Object.keys(source).some(key => !allowed.includes(key))) unsupported = 'This resource has fields that this dashboard cannot edit safely.';
  } else if (type === 'monitors') {
    const parsed = monitorProjection(source, 'MonitorSpec');
    Object.assign(spec, parsed.value);
    unsupported = parsed.unsupported ?? unsupported;
  } else {
    const parsed = driverView(source, 'notification');
    Object.assign(spec, parsed.value);
    unsupported = parsed.unsupported ?? unsupported;
  }
  const metadata: Metadata = { id: value.metadata.id };
  for (const key of ['name', 'uid', 'resourceVersion'] as const) if (typeof value.metadata[key] === 'string') metadata[key] = value.metadata[key];
  if (typeof value.metadata.generation === 'number') metadata.generation = value.metadata.generation;
  if (value.metadata.labels !== undefined && (!object(value.metadata.labels) || !Object.values(value.metadata.labels).every(label => typeof label === 'string'))) unsupported = 'This resource has unsupported label values.';
  if (object(value.metadata.labels) && Object.values(value.metadata.labels).every(label => typeof label === 'string')) metadata.labels = { ...value.metadata.labels } as Record<string, string>;
  const status: Record<string, unknown> = {};
  if (type === 'monitors' && object(value.status)) Object.assign(status, monitorProjection(value.status, 'MonitorStatus').value);
  if (object(value.status)) for (const key of ['available', 'applied', 'observedGeneration'] as const) {
    if (typeof value.status[key] === 'boolean' || typeof value.status[key] === 'number') status[key] = value.status[key];
  }
  return { resource: { apiVersion: typeof value.apiVersion === 'string' ? value.apiVersion : 'cpra.io/v2', kind: value.kind, metadata, spec, status } as ManagedResource, unsupported };
}

export async function listResources(session: DashboardSession, type: ResourceType, cursor = '', signal?: AbortSignal): Promise<ResourcePage> {
  const params = new URLSearchParams({ limit: '100' });
  if (cursor) params.set('cursor', cursor);
  const result = await session.get<unknown>(`${resourceKinds[type].path}?${params}`, { signal });
  if (!object(result.data) || !Array.isArray(result.data.items) || result.data.items.length > 500) throw new ManagementError('invalid', 'The server returned an unsupported resource page.');
  return { items: result.data.items.map(item => resourceView(item, type)),
    nextCursor: typeof result.data.nextCursor === 'string' ? result.data.nextCursor : undefined,
    snapshot: typeof result.data.snapshot === 'string' ? result.data.snapshot : undefined };
}

export async function getResource(session: DashboardSession, type: ResourceType, id: string, signal?: AbortSignal): Promise<ResourceView> {
  const result = await session.get<unknown>(`${resourceKinds[type].path}/${encodeURIComponent(id)}`, { signal });
  return resourceView(result.data, type);
}

export function operationFor(type: ResourceType, action: 'Create' | 'Replace' | 'Patch' | 'Delete' | 'Get'): keyof typeof operationContracts {
  const operation = `${action}${resourceKinds[type].operations}`;
  if (!(operation in operationContracts)) throw new Error('Resource operation missing from generated contract');
  return operation as keyof typeof operationContracts;
}

export function mutationReceipt(response: APIResponse<unknown>): APIResponse<unknown> {
  // Feedback needs receipt/progress identity, never the potentially secret-bearing payload.
  const value = response.data;
  let operation;
  if (object(value) && typeof value.contentDigest === 'string' && typeof value.state === 'string') {
    try { operation = operationView(value, operationID(response.operationID) ? response.operationID : undefined); }
    catch { /* Keep the header so an unsupported body can still be reconciled. */ }
  }
  return { ...response, operationID: operationID(response.operationID) ? response.operationID : operation?.id ?? '', data: { operation } };
}
