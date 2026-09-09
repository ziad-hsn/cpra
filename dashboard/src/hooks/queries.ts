import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import type { MonitorsFilters } from '../api/types';

export function useOverview() {
  return useQuery({
    queryKey: ['overview'],
    queryFn: api.getOverview,
    refetchInterval: 1000,
    staleTime: 800,
  });
}

export function useMonitors(
  params: MonitorsFilters & { page?: number; size?: number; sort?: string },
) {
  return useQuery({
    queryKey: ['monitors', params],
    queryFn: () => api.getMonitors(params),
    refetchInterval: 5000,
    staleTime: 3000,
    placeholderData: (prev) => prev,
  });
}

export function useMonitor(id: number | string | undefined) {
  return useQuery({
    queryKey: ['monitor', id],
    queryFn: () => api.getMonitor(id!),
    enabled: id !== undefined && id !== '',
    refetchInterval: 5000,
  });
}

export function useIncidents() {
  return useQuery({
    queryKey: ['incidents'],
    queryFn: api.getIncidents,
    refetchInterval: 5000,
  });
}

export function useSystems() {
  return useQuery({
    queryKey: ['systems'],
    queryFn: api.getSystems,
    refetchInterval: 5000,
  });
}

export function useQueues() {
  return useQuery({
    queryKey: ['queues'],
    queryFn: api.getQueues,
    refetchInterval: 5000,
  });
}

export function usePools() {
  return useQuery({
    queryKey: ['pools'],
    queryFn: api.getPools,
    refetchInterval: 5000,
  });
}

export function useConfig() {
  return useQuery({
    queryKey: ['config'],
    queryFn: api.getConfig,
    refetchInterval: 30000,
  });
}

export function useHealth() {
  return useQuery({
    queryKey: ['health'],
    queryFn: api.getHealth,
    refetchInterval: 10000,
  });
}
