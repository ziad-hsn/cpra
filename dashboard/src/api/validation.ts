import { validationMessages, validationFieldNames } from './generated';

const fieldPath = /^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*|\[[0-9]{1,10}\])*$/;

export function safeValidationField(field?: string): string {
  return field && field.length <= 160 && fieldPath.test(field) && field.replace(/\[[0-9]+\]/g, '').split('.').every(part => validationFieldNames.has(part.replace(/_/g, '').toLowerCase())) ? field : '';
}

export function validationMessage(reason?: string): string {
  return reason && Object.hasOwn(validationMessages, reason) ? validationMessages[reason] : '';
}

/** Render only the local rule catalog and schema property paths. */
export function validationDiagnostic(value: { field?: string; reason?: string }, source?: SourceCoordinates): string {
  if (!value.reason) return '';
  const message = validationMessage(value.reason) || 'Unrecognized validation rule';
  const field = safeValidationField(validationPoint(source, value.field)?.field) || safeValidationField(value.field);
  return field ? `${field}: ${message}` : message;
}

export interface SourcePoint { readonly line?: number; readonly column?: number; readonly field?: string }
export interface SourceCoordinates extends SourcePoint { readonly fields?: Readonly<Record<string, SourcePoint>> }
const coordinate = (value: unknown): number | undefined => typeof value === 'number' && Number.isSafeInteger(value) && value > 0 && value <= 100_000_000 ? value : undefined;
const point = (value: unknown): SourcePoint => {
  const input = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const field = typeof input.field === 'string' ? safeValidationField(input.field) : '';
  const safeField = field && !/(^|\.)(headers|labels|credentialrefs|parameters|parameterschema|resultschema|status)(\.|\[)/.test(field.replace(/_/g, '').toLowerCase()) ? field : undefined;
  return { line: coordinate(input.line), column: coordinate(input.column), field: safeField };
};

/** Local parser coordinates only. Never include these in an operation identity,
 * upload, URL or persisted browser state. Free-form map descendants are omitted. */
export function sourceCoordinates(value: unknown): SourceCoordinates {
  const input = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const fields: Record<string, SourcePoint> = Object.create(null);
  if (input.fields && typeof input.fields === 'object' && !Array.isArray(input.fields)) {
    for (const [key, value] of Object.entries(input.fields).slice(0, 1024)) {
      if (!safeValidationField(key)) continue;
      const path = key.replace(/_/g, '').toLowerCase();
      if (/(^|\.)(headers|labels|credentialrefs|parameters|parameterschema|resultschema|status)(\.|\[)/.test(path)) continue;
      const location = point(value);
      if (location.line && location.column) fields[path] = location;
    }
  }
  return { ...point(value), fields };
}

function validationPoint(source: SourceCoordinates | undefined, field?: string): SourcePoint | undefined {
  if (!source) return undefined;
  const relative = field?.replace(/^items\[\d+\]\.resource\.?/, '');
  let path = safeValidationField(relative).replace(/_/g, '').toLowerCase();
  while (path) {
    const candidate = source.fields?.[path];
    if (candidate?.line && candidate.column) return candidate;
    const end = Math.max(path.lastIndexOf('.'), path.lastIndexOf('['));
    path = end < 0 ? '' : path.slice(0, end);
  }
  return source;
}

export function validationSourcePosition(source: SourceCoordinates | undefined, field?: string): string {
  const location = validationPoint(source, field);
  if (!location) return '';
  return location.line ? `, line ${location.line}, column ${location.column ?? 'unavailable'}` : '';
}
