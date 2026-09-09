import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { MemoryRouter } from 'react-router-dom';
import { AppShell } from '../src/App';

const overviewResponse = {
  generated: new Date().toISOString(),
  total: 1048576,
  disabled: 10,
  by_status: { up: 1000000, down: 5, verifying: 20, incident: 3, disabled: 10 },
  by_pulse_type: { http: 900000, tcp: 100000, icmp: 48576 },
  by_code: { red: 5, yellow: 20, green: 1000000, cyan: 3, gray: 10 },
  up_percent: 95.3,
  index_capped: false,
};

const monitorsResponse = {
  generated: new Date().toISOString(),
  page: 1,
  size: 50,
  total: 2,
  filters: {},
  monitors: [
    {
      id: 1,
      name: 'example.com',
      pulse_type: 'http',
      status: 'up' as const,
      incident: false,
      pending_code: '',
      consecutive_failures: 0,
      last_check: new Date().toISOString(),
      last_success: new Date().toISOString(),
      next_check: new Date().toISOString(),
      active_codes: [],
    },
    {
      id: 2,
      name: 'db.internal',
      pulse_type: 'tcp',
      status: 'down' as const,
      incident: true,
      pending_code: 'red',
      consecutive_failures: 3,
      last_check: new Date().toISOString(),
      last_success: new Date().toISOString(),
      next_check: new Date().toISOString(),
      active_codes: ['red'],
    },
  ],
};

const incidentsResponse = {
  generated: new Date().toISOString(),
  count: 1,
  incidents: [monitorsResponse.monitors[1]],
};

const emptyAggregate = {
  start_time: new Date().toISOString(),
  min_update_duration: 0,
  total_entities_processed: 0,
  total_batches_created: 0,
  total_duration: 0,
  max_update_duration: 0,
  system_count: 0,
  avg_update_duration: 0,
  avg_entities_per_update: 0,
  avg_batches_per_update: 0,
  entities_per_second: 0,
  updates_per_second: 0,
  total_updates: 0,
};

const systemsResponse = { systems: {}, aggregate: emptyAggregate };
const emptyQueue = {
  name: '', last_enqueue: '', last_dequeue: '', avg_queue_time: 0, dequeued: 0, dropped: 0,
  max_queue_time: 0, queue_depth: 0, max_job_latency: 0, avg_job_latency: 0,
  enqueue_rate: 0, dequeue_rate: 0, enqueued: 0, capacity: 0, sample_window: 0,
};
const emptyPool = {
  name: '', last_scale_time: '', min_workers: 0, max_workers: 0, current_capacity: 0,
  running_workers: 0, waiting_tasks: 0, target_workers: 0, tasks_submitted: 0,
  tasks_completed: 0, scaling_events: 0, pending_results: 0,
};

const server = setupServer(
  http.get('/api/v1/overview', () => HttpResponse.json(overviewResponse)),
  http.get('/api/v1/monitors', () => HttpResponse.json(monitorsResponse)),
  http.get('/api/v1/monitors/:id', () => HttpResponse.json(monitorsResponse.monitors[0])),
  http.get('/api/v1/incidents', () => HttpResponse.json(incidentsResponse)),
  http.get('/api/v1/systems', () => HttpResponse.json(systemsResponse)),
  http.get('/api/v1/queues', () =>
    HttpResponse.json({ pulse: emptyQueue, intervention: emptyQueue, code: emptyQueue }),
  ),
  http.get('/api/v1/pools', () =>
    HttpResponse.json({ pulse: emptyPool, intervention: emptyPool, code: emptyPool }),
  ),
  http.get('/api/v1/config', () =>
    HttpResponse.json({
      queue_capacity: 10000,
      batch_size: 512,
      alert_cooldown: 300000000000,
      recovery_bypass: true,
      use_adaptive_queue: false,
      queue_type: 'workiva',
    }),
  ),
  http.get('/api/v1/healthz', () => HttpResponse.json({ status: 'ok' })),
);

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

function renderApp(initialRoute = '/') {
  return render(
    <MemoryRouter initialEntries={[initialRoute]}>
      <AppShell />
    </MemoryRouter>,
  );
}

describe('App', () => {
  it('does not report an unprobed fleet as operational', async () => {
    server.use(http.get('/api/v1/overview', () => HttpResponse.json({
      ...overviewResponse, total: 2, disabled: 0, by_status: { unknown: 2 }, up_percent: 0,
    })));
    renderApp('/');
    await waitFor(() => {
      expect(screen.getByRole('banner', { name: 'Global fleet status' })).toHaveTextContent('Awaiting first check results');
    });
    expect(screen.queryByText('All systems operational')).not.toBeInTheDocument();
  });

  it('renders the layout shell with CPRA branding', () => {
    renderApp('/');
    expect(screen.getByText('CPRA')).toBeInTheDocument();
  });

  it('renders the overview page and loads fleet KPIs from mocked API', async () => {
    renderApp('/');
    await waitFor(
      () => {
        expect(screen.getAllByText('1,048,576').length).toBeGreaterThan(0);
      },
      { timeout: 5000 },
    );
  });

  it('renders nav items', () => {
    renderApp('/');
    expect(screen.getByText('Overview')).toBeInTheDocument();
    expect(screen.getByText('Monitors')).toBeInTheDocument();
    expect(screen.getByText('Alerts')).toBeInTheDocument();
  });
});
