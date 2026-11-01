import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { onlineManager } from '@tanstack/react-query';
import { RouteFailure } from '../src/App';
import { StatusChip } from '../src/components/StatusChip';
import { PageBoundary } from '../src/components/PageBoundary';

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

it('retains a successful operation receipt during healthy route navigation', () => {
  function SavedOperation() {
    const [receipt, setReceipt] = useState('');
    return <><button onClick={() => setReceipt('operation-123')}>Save fixture</button><output>{receipt}</output></>;
  }
  const view = render(<PageBoundary resetKey="/secrets" fallback={<p>Unavailable</p>}><SavedOperation /></PageBoundary>);
  fireEvent.click(screen.getByRole('button', {name: 'Save fixture'}));
  view.rerender(<PageBoundary resetKey="/secrets/created" fallback={<p>Unavailable</p>}><SavedOperation /></PageBoundary>);
  expect(screen.getByRole('status')).toHaveTextContent('operation-123');
});

it('allows navigation away from a failed page without rendering its error', () => {
  vi.spyOn(console, 'error').mockImplementation(() => {});
  const expectedFailure = (event: ErrorEvent) => { if (event.message.includes('private-route-marker')) event.preventDefault(); };
  window.addEventListener('error', expectedFailure);
  function Broken(): never { throw new Error('private-route-marker'); }
  const view = render(<PageBoundary resetKey="/failed" fallback={<p>Page unavailable</p>}><Broken /></PageBoundary>);
  expect(screen.getByText('Page unavailable')).toBeInTheDocument();
  expect(document.body).not.toHaveTextContent('private-route-marker');
  view.rerender(<PageBoundary resetKey="/next" fallback={<p>Page unavailable</p>}><p>Next page</p></PageBoundary>);
  expect(screen.getByText('Next page')).toBeInTheDocument();
  expect(screen.queryByText('Page unavailable')).not.toBeInTheDocument();
  window.removeEventListener('error', expectedFailure);
});
