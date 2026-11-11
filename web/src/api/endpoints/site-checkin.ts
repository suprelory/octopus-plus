import { useMutation, useQuery } from '@tanstack/react-query';
import { apiClient } from '../client';
import type { SiteCheckinDefaults, SitePlatform } from './site-types';
import type { SiteCheckinStatusConfig } from './site-types';

export function useTestCheckinStatusRule() {
  return useMutation({
    mutationFn: (request: { config: SiteCheckinStatusConfig; http_status: number; date: string; response: unknown }) =>
      apiClient.post<{ state: 'checked_in' | 'not_checked_in' | 'unknown' }>('/api/v1/site/checkin-status/test', request),
  });
}

export function useTestCheckinRewardExtractor() {
  return useMutation({
    mutationFn: (request: { code: string; response: unknown; status_response?: unknown }) =>
      apiClient.post<{ reward: string; found: boolean }>('/api/v1/site/checkin-reward/test', request),
  });
}

export function useSiteCheckinDefaults() {
  return useQuery({
    queryKey: ['sites', 'checkin-capabilities'],
    queryFn: () => apiClient.get<Record<SitePlatform, SiteCheckinDefaults>>('/api/v1/site/checkin-capabilities'),
    staleTime: Infinity,
  });
}

export type SiteCheckinLog = {
  id: number;
  site_id: number;
  account_id: number;
  site_name: string;
  account_name: string;
  platform: string;
  source: string;
  status: 'success' | 'failed' | 'skipped' | 'pending';
  reason: string;
  message: string;
  reward: string;
  duration_ms: number;
  started_at: string;
  finished_at: string;
};

export type SiteCheckinLogPage = {
  items: SiteCheckinLog[];
  next_before_id?: number;
};

export type SiteCheckinSiteStats = {
  site_id: number;
  site_name: string;
  reward: number;
  total_count: number;
  success_count: number;
  failed_count: number;
  skipped_count: number;
  pending_count?: number;
};

export type SiteCheckinStats = {
  today_reward: number;
  recent_7_days_reward: number;
  recent_30_days_reward: number;
  total_reward: number;
  total_count: number;
  success_count: number;
  failed_count: number;
  skipped_count: number;
  pending_count?: number;
  invalid_reward_count: number;
  unknown_reward_count: number;
  timezone: string;
  by_site: SiteCheckinSiteStats[];
};

export function useSiteCheckinStats(filters: Record<string, string | number>) {
  return useQuery({
    queryKey: ['sites', 'checkin-stats', filters],
    queryFn: () => apiClient.get<SiteCheckinStats>('/api/v1/site/checkin-stats', filters),
    refetchInterval: 60000,
  });
}

export function useSiteCheckinLogs(filters: Record<string, string | number>, enabled = true) {
  return useQuery({
    queryKey: ['sites', 'checkin-logs', filters],
    queryFn: () => apiClient.get<SiteCheckinLogPage>('/api/v1/site/checkin-logs', filters),
    enabled,
    refetchInterval: enabled ? 15000 : false,
  });
}
