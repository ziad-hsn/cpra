import { describe, expect, it } from 'vitest';
import { sourceCoordinates, validationDiagnostic, validationSourcePosition } from '../src/api/validation';

describe('original source diagnostics', () => {
  it('uses the original YAML spelling and location without changing the wire field', () => {
    const source = sourceCoordinates({ line: 2, column: 1, fields: { 'spec.check.driver.config.caFile': { line: 12, column: 5, field: 'pulse_check.config.ca_file' } } });
    const problem = { field: 'items[0].resource.spec.check.driver.config.caFile', reason: 'skipVerificationCAConflict' };
    expect(validationDiagnostic(problem, source)).toMatch(/^pulse_check\.config\.ca_file:/);
    expect(validationSourcePosition(source, problem.field)).toBe(', line 12, column 5');
    expect(problem.field).toContain('caFile');
  });
  it('rejects values and free-form descendants in coordinate payloads', () => {
    const source = sourceCoordinates({ fields: {
      'spec.check.interval': { line: 3, column: 2, field: 'spec.check.driver.config.headers.url' },
      'spec.check.driver.config.headers.url': { line: 4, column: 2, field: 'private-value' },
    } });
    expect(JSON.stringify(source)).not.toContain('headers');
    expect(JSON.stringify(source)).not.toContain('private-value');
    expect(validationDiagnostic({ field: 'spec.check.interval', reason: 'positiveDuration' }, source)).toMatch(/^spec.check.interval:/);
  });
});
