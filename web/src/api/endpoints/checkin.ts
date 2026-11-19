import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';
import type {
    CheckinAccount,
    CheckinAccountCreate,
    CheckinAccountInput,
    CheckinBalance,
    CheckinBatch,
    CheckinBatchSelection,
    CheckinCredential,
    CheckinCredentialInput,
    CheckinCredentialPatch,
    CheckinDefaults,
    CheckinHistoryFilter,
    CheckinLog,
    CheckinNotification,
    CheckinPage,
    CheckinResult,
    CheckinSite,
    CheckinSiteInput,
    CheckinStats,
    CheckinStatusRule,
} from './checkin-types';

const root = '/api/v1/checkin';
export const checkinKeys = { all: ['checkin'] as const, sites: ['checkin', 'sites'] as const };
export const isActiveBatch = (job: CheckinBatch) => job.status === 'queued' || job.status === 'running';

// Mutation inputs can contain secrets. Do not retain them after the form unmounts
// or retry an execution/notification write implicitly.
export function useCheckinMutation<T, V>(mutationFn: (variables: V) => Promise<T>) {
    const client = useQueryClient();
    return useMutation({
        mutationFn,
        retry: false,
        gcTime: 0,
        onSettled: async () => {
            await client.invalidateQueries({ queryKey: checkinKeys.all });
            void client.invalidateQueries({ queryKey: ['proxy-pool'] });
        },
    });
}
export function useCheckinSites() {
    return useQuery({
        queryKey: checkinKeys.sites,
        queryFn: ({ signal }) => apiClient.get<CheckinSite[]>(`${root}/sites`, { include_archived: true }, { signal }),
    });
}
export function useCheckinAccounts() {
    return useQuery({
        queryKey: [...checkinKeys.all, 'accounts'],
        queryFn: ({ signal }) => apiClient.get<CheckinAccount[]>(`${root}/accounts`, undefined, { signal }),
        refetchInterval: 15000,
    });
}
export function useCheckinCredentials() {
    return useQuery({
        queryKey: [...checkinKeys.all, 'credentials'],
        queryFn: ({ signal }) => apiClient.get<CheckinCredential[]>(`${root}/credentials`, undefined, { signal }),
    });
}
export function useCheckinCapabilities() {
    return useQuery({
        queryKey: [...checkinKeys.all, 'capabilities'],
        queryFn: ({ signal }) => apiClient.get<CheckinDefaults>(`${root}/capabilities`, undefined, { signal }),
        staleTime: Infinity,
    });
}
function pageQuery<T>(kind: 'logs' | 'batches' | 'notifications', filters: CheckinHistoryFilter = {}) {
    return {
        queryKey: [...checkinKeys.all, kind, filters],
        initialPageParam: undefined as string | undefined,
        queryFn: ({ pageParam, signal }: { pageParam: string | undefined; signal: AbortSignal }) =>
            apiClient.get<CheckinPage<T>>(
                `${root}/${kind}`,
                { ...filters, limit: 50, ...(pageParam ? { before_id: pageParam } : {}) },
                { signal },
            ),
        getNextPageParam: (last: CheckinPage<T>) => last.next_before_id || undefined,
    };
}
export function useCheckinLogs(filters: CheckinHistoryFilter) {
    return useInfiniteQuery(pageQuery<CheckinLog>('logs', filters));
}
export function useCheckinStats(filters: CheckinHistoryFilter, timezone: string) {
    return useQuery({
        queryKey: [...checkinKeys.all, 'stats', filters, timezone],
        queryFn: ({ signal }) => apiClient.get<CheckinStats>(`${root}/stats`, { ...filters, timezone }, { signal }),
    });
}
export function useCheckinBatches() {
    return useInfiniteQuery({
        ...pageQuery<CheckinBatch>('batches'),
        refetchInterval: (query) =>
            query.state.data?.pages.some((page) => page.items.some(isActiveBatch)) ? 2000 : false,
    });
}
export function useCheckinBatch(id: string | null) {
    return useQuery({
        queryKey: [...checkinKeys.all, 'batch', id],
        enabled: id !== null,
        queryFn: ({ signal }) => apiClient.get<CheckinBatch>(`${root}/batches/${id}`, undefined, { signal }),
        refetchInterval: (query) => (query.state.data && isActiveBatch(query.state.data) ? 1500 : false),
    });
}
export function useCheckinNotifications() {
    return useInfiniteQuery({
        ...pageQuery<CheckinNotification>('notifications'),
        refetchInterval: (query) =>
            query.state.data?.pages.some((page) =>
                page.items.some((item) => item.state === 'queued' || item.state === 'sending'),
            )
                ? 2000
                : false,
    });
}
export const checkinAPI = {
    createSite: (body: CheckinSiteInput) => apiClient.post<CheckinSite>(`${root}/sites`, body),
    patchSite: (id: number, body: Partial<CheckinSiteInput>) =>
        apiClient.patch<CheckinSite>(`${root}/sites/${id}`, body),
    deleteSite: (id: number) => apiClient.delete<{ id: number }>(`${root}/sites/${id}`),
    archiveSite: (id: number, archived: boolean) =>
        apiClient.post<CheckinSite>(`${root}/sites/${id}/${archived ? 'archive' : 'restore'}`),
    createAccount: (body: CheckinAccountCreate) => apiClient.post<CheckinAccount>(`${root}/accounts`, body),
    patchAccount: (id: number, body: Partial<CheckinAccountInput>) =>
        apiClient.patch<CheckinAccount>(`${root}/accounts/${id}`, body),
    deleteAccount: (id: number) => apiClient.delete<{ id: number }>(`${root}/accounts/${id}`),
    createCredential: (body: CheckinCredentialInput) => apiClient.post<CheckinCredential>(`${root}/credentials`, body),
    patchCredential: (id: number, body: CheckinCredentialPatch) =>
        apiClient.patch<CheckinCredential>(`${root}/credentials/${id}`, body),
    deleteCredential: (id: number) => apiClient.delete<{ id: number }>(`${root}/credentials/${id}`),
    execute: (id: number) => apiClient.post<CheckinResult>(`${root}/accounts/${id}/execute`),
    confirm: (id: number) => apiClient.post<CheckinResult>(`${root}/accounts/${id}/confirm`),
    balance: (id: number) => apiClient.post<CheckinBalance>(`${root}/accounts/${id}/balance`),
    startBatch: (body: CheckinBatchSelection) => apiClient.post<CheckinBatch>(`${root}/batches`, body),
    retryNotification: (id: string) =>
        apiClient.post<{ id: string; state: 'queued' }>(`${root}/notifications/${id}/retry`),
    testReward: (body: { code: string; response: unknown; status_response?: unknown }) =>
        apiClient.post<{ reward: string; found: boolean }>(`${root}/reward/test`, body),
    testStatus: (body: { config: CheckinStatusRule; http_status: number; response: unknown; date: string }) =>
        apiClient.post<{ state: string }>(`${root}/status/test`, body),
};
