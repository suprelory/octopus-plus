import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../client";
import { logger } from "@/lib/logger";
import { CustomHeader } from './site-types';
import { invalidateSiteQueries } from './site-query';

export type SiteCheckinBatchJob = {
  id: string;
  status: 'queued' | 'running' | 'completed' | 'completed_with_errors' | 'canceled' | 'failed' | 'interrupted';
  trigger: string;
  site_ids?: number[];
  total: number;
  attempted: number;
  success: number;
  partial: number;
  pending?: number;
  failed: number;
  skipped: number;
  warnings: number;
  canceled: boolean;
  cancel_reason?: string;
  current_site_id?: number;
  current_site_name?: string;
  current_account_id?: number;
  current_account_name?: string;
  error_message?: string;
  duration_ms: number;
  started_at: string;
  updated_at: string;
  finished_at?: string;
};

export function useSyncAllSites() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => apiClient.post<null>("/api/v1/site/sync-all", {}),
    onSuccess: () => invalidateSiteQueries(queryClient),
    onError: (error) => logger.error("站点批量同步失败:", error),
  });
}

export function useCheckinAllSites() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      apiClient.post<SiteCheckinBatchJob>("/api/v1/site/checkin-all", {}),
    onSuccess: (job) => {
      queryClient.setQueryData(["sites", "checkin-batch", "latest"], job);
      void queryClient.invalidateQueries({ queryKey: ["sites", "checkin-batch", "latest"] });
      invalidateSiteQueries(queryClient);
    },
    onError: (error) => logger.error("站点批量签到失败:", error),
  });
}

export function useCheckinSelectedSites() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (ids: number[]) =>
      apiClient.post<SiteCheckinBatchJob>("/api/v1/site/batch", { ids, action: "checkin" }),
    onSuccess: (job) => {
      queryClient.setQueryData(["sites", "checkin-batch", "latest"], job);
      void queryClient.invalidateQueries({ queryKey: ["sites", "checkin-batch", "latest"] });
      invalidateSiteQueries(queryClient);
    },
    onError: (error) => logger.error("所选站点签到失败:", error),
  });
}

export function useLatestSiteCheckinBatch() {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: ["sites", "checkin-batch", "latest"],
    queryFn: async () => {
      const response = await apiClient.get<SiteCheckinBatchJob | { code?: number }>("/api/v1/site/checkin-batches/latest");
      const job = response && typeof response === 'object' && 'status' in response
        ? response as SiteCheckinBatchJob
        : null;
      const previous = queryClient.getQueryData<SiteCheckinBatchJob | null>(["sites", "checkin-batch", "latest"]);
      if (job && job.status !== 'queued' && job.status !== 'running'
          && (previous?.id !== job.id || previous?.status !== job.status)) {
        invalidateSiteQueries(queryClient);
      }
      return job;
    },
    refetchInterval: 5000,
  });
}

export function useSiteLastSyncTime() {
  return useQuery({
    queryKey: ["sites", "last-sync-time"],
    queryFn: async () => apiClient.get<string>("/api/v1/site/last-sync-time"),
    refetchInterval: 30000,
  });
}

export function useSiteLastCheckinTime() {
  return useQuery({
    queryKey: ["sites", "last-checkin-time"],
    queryFn: async () =>
      apiClient.get<string>("/api/v1/site/last-checkin-time"),
    refetchInterval: 30000,
  });
}

export function useSiteBatchAction() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (data: { ids: number[]; action: string }) =>
      apiClient.post<{
        success_ids: number[];
        failed_items: Array<{ id: number; message: string }>;
      }>("/api/v1/site/batch", data),
    onSuccess: () => invalidateSiteQueries(queryClient),
    onError: (error) => logger.error("批量操作失败:", error),
  });
}

export function useSiteBatchEdit() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (data: {
      ids: number[];
      add_tags: string[];
      remove_tags: string[];
      upserts: CustomHeader[];
      delete_keys: string[];
    }) =>
      apiClient.post<{
        success_ids: number[];
        failed_items: Array<{ id: number; message: string }>;
      }>("/api/v1/site/batch/edit", data),
    onSuccess: () => invalidateSiteQueries(queryClient),
    onError: (error) => logger.error("批量编辑失败:", error),
  });
}
