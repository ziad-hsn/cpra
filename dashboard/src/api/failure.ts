import type { FailureDiagnostic } from './generated';
import { object } from './value';
import { ManagementError } from './session';

/** Retain only bounded public fields, without arbitrary provider payloads. */
export function failureView(value: unknown): FailureDiagnostic {
  if (!object(value) || typeof value.code !== 'string' || !/^[a-z][a-z0-9_]{0,63}$/.test(value.code) ||
    typeof value.message !== 'string' || !value.message || new TextEncoder().encode(value.message).byteLength > 256 || /[\p{Cc}\p{Cf}]/u.test(value.message)) {
    throw new ManagementError('invalid', 'The server returned an unsupported failure diagnostic.');
  }
  return { code: value.code, message: value.message };
}
