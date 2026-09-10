import type {
  DurableStateResponse, HistoryResponse, SLOResponse,
  ConfigResponse,
  HealthResponse,
  IncidentsResponse,
  MonitorSummary,
  MonitorsFilters,
  MonitorsResponse,
  OverviewResponse,
  PoolsResponse,
  QueuesResponse,
  SystemsResponse,
} from './types';

/** Base URL for API requests. In dev, the Vite proxy forwards /api → localhost:8060. */
export const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function request<T>(path: string): Promise<T> {
  const url = `${API_BASE}${path}`;
  const res = await fetch(url, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(msg, res.status);
  }
  return res.json() as Promise<T>;
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '' && v !== null) {
      parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
    }
  }
  return parts.length > 0 ? `?${parts.join('&')}` : '';
}

export const api = {
  getState: (monitorID?: string) => request<DurableStateResponse>("/api/v1/state" + buildQuery({monitor_id: monitorID})),
  getHistory: (monitorID: string, cursor = "") => request<HistoryResponse>("/api/v1/history" + buildQuery({monitor_id: monitorID, cursor, limit: 100})),
  getSLO: () => request<SLOResponse>("/api/v1/slo"),
  getOverview: () => request<OverviewResponse>('/api/v1/overview'),

  getMonitors: (params: MonitorsFilters & { page?: number; size?: number; sort?: string } = {}) =>
    request<MonitorsResponse>(
      '/api/v1/monitors' +
        buildQuery({
          status: params.status,
          type: params.type,
          code: params.code,
          q: params.q,
          page: params.page,
          size: params.size,
          sort: params.sort,
        }),
    ),

  getMonitor: (id: number | string) => request<MonitorSummary>(`/api/v1/monitors/${id}`),

  getIncidents: () => request<IncidentsResponse>('/api/v1/incidents'),

  getSystems: () => request<SystemsResponse>('/api/v1/systems'),

  getQueues: () => request<QueuesResponse>('/api/v1/queues'),

  getPools: () => request<PoolsResponse>('/api/v1/pools'),

  getConfig: () => request<ConfigResponse>('/api/v1/config'),

  getHealth: () => request<HealthResponse>('/api/v1/healthz'),
};
