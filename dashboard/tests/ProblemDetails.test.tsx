import { afterEach, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { ManagementError } from '../src/api/session';
import { ProblemDetails } from '../src/components/ProblemDetails';

afterEach(cleanup);
const request = 'c47948b9-084b-4109-a862-0312e7a77102';
const operation = '964cbcb8-9bc9-43de-bd64-0c1169898c6d';
const secret = 'credential-value-that-must-not-reach-output';

it('shows safe correlation, phase and field codes without raw server details', () => {
  const error = new ManagementError('http', 'Request rejected.', 422, { code: 'invalidResource', requestID: request, operationID: operation,
    title: secret, detail: secret, errors: [{ field: 'spec.check.driver.config', reason: 'invalidResource', message: secret }] });
  const { container } = render(<ProblemDetails error={error} phase="validation" />);
  expect(screen.getByText(request)).toBeInTheDocument();
  expect(screen.getByText(operation)).toBeInTheDocument();
  expect(screen.getByText('validation')).toBeInTheDocument();
  expect(screen.getByText('invalidResource')).toBeInTheDocument();
  expect(screen.getByText(/spec.check.driver.config: validation failed/)).toBeInTheDocument();
  expect(container.textContent).not.toContain(secret);
});

it('bounds field output and rejects hostile codes, identities, paths and phases', () => {
  const error = new ManagementError('http', 'Request rejected.', 999999, { code: secret, requestID: secret, operationID: secret, errors:
    Array.from({ length: 12 }, () => ({ field: `/private/${secret}`, reason: secret, message: secret })) });
  const { container } = render(<ProblemDetails error={error} phase={secret} />);
  expect(screen.getAllByText('Resource field: validation failed.')).toHaveLength(10);
  expect(screen.getByText('Additional field errors omitted.')).toBeInTheDocument();
  expect(container.textContent).not.toContain(secret);
  expect(container.textContent).not.toContain('/private/');
  expect(container.textContent).not.toContain('999999');
});
