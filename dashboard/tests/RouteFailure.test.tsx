import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { onlineManager } from '@tanstack/react-query';
import { RouteFailure } from '../src/App';
import { StatusChip } from '../src/components/StatusChip';

afterEach(() => { cleanup(); vi.restoreAllMocks(); onlineManager.setOnline(true); });

it('catches a route rendering failure without showing its payload or stack', async () => {
  vi.spyOn(console, 'error').mockImplementation(() => {});
  const expectedFailure = (event: ErrorEvent) => { if (event.message.includes('private-response-marker')) event.preventDefault(); };
  window.addEventListener('error', expectedFailure);
  function Broken(): never { throw new Error('private-response-marker\nstack-marker'); }
  const router = createMemoryRouter([{path: '*', element: <Broken />, errorElement: <RouteFailure />}]);
  render(<RouterProvider router={router} />);
  expect(await screen.findByRole('heading', {name: 'This page could not be loaded'})).toBeInTheDocument();
  expect(document.body).not.toHaveTextContent('private-response-marker');
  expect(document.body).not.toHaveTextContent('stack-marker');
  expect(screen.getByRole('button', {name: 'Reload dashboard'})).toBeEnabled();
  expect(screen.getByRole('link', {name: 'Go to overview'})).toHaveAttribute('href', '/');
  expect(screen.getByText(/Check the original operation/)).toBeInTheDocument();
  window.removeEventListener('error', expectedFailure);
});

it('explains an offline route failure without submitting or repeating work', () => {
  onlineManager.setOnline(false);
  render(<RouteFailure />);
  expect(screen.getByRole('heading', {name: 'You are offline'})).toBeInTheDocument();
  expect(screen.getByText(/Current monitor health is unavailable/)).toBeInTheDocument();
});

it('keeps individual monitor statuses readable without a live announcement per row', () => {
  render(<><StatusChip status="up" /><StatusChip status="down" /></>);
  expect(screen.queryByRole('status')).not.toBeInTheDocument();
  expect(document.body).toHaveTextContent('OPERATIONAL');
});
