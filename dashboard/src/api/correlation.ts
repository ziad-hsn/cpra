const uuid = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
const identity = new RegExp(`^(?:${uuid}|op\\.${uuid}\\.[0-9]{20})$`);

// Error metadata may contain arbitrary text. It is not an observed operation.
export function correlationID(value: unknown): value is string {
  if (typeof value !== 'string' || !identity.test(value) || value.includes('00000000-0000-0000-0000-000000000000')) return false;
  return !value.startsWith('op.') || BigInt(value.slice(40)) > 0n && BigInt(value.slice(40)) <= 18446744073709551615n;
}
