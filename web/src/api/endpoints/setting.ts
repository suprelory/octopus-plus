import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient, authenticatedFetch } from '../client';
import { logger } from '@/lib/logger';
import { V2_UI } from '@/lib/v2-ui';

/**
 * Setting 数据
 */
export interface Setting {
    key: string;
    value: string;
}

export const SettingKey = {
    ProxyURL: 'proxy_url',
    TrustedProxies: 'trusted_proxies',
    StatsSaveInterval: 'stats_save_interval',
    ModelInfoUpdateInterval: 'model_info_update_interval',
    SyncLLMInterval: 'sync_llm_interval',
    SiteSyncInterval: 'site_sync_interval',
    RelayLogKeepEnabled: 'relay_log_keep_enabled',
    RelayLogKeepPeriod: 'relay_log_keep_period',
    RelayLogContentEnabled: 'relay_log_content_enabled',
    RelayLogContentKeepPeriod: 'relay_log_content_keep_period',
    RelayLogContentMaxMB: 'relay_log_content_max_mb',
    CORSAllowOrigins: 'cors_allow_origins',
    CircuitBreakerThreshold: 'circuit_breaker_threshold',
    CircuitBreakerCooldown: 'circuit_breaker_cooldown',
    CircuitBreakerMaxCooldown: 'circuit_breaker_max_cooldown',
    ChannelAffinityEnabled: 'channel_affinity_enabled',
    ChannelAffinityTTLSeconds: 'channel_affinity_ttl_seconds',
    EmptyResponseDetectionEnabled: 'empty_response_detection_enabled',
    RelayMaxChannelAttempts: 'relay_max_channel_attempts',
    RelayMaxTotalAttempts: 'relay_max_total_attempts',
    RelayFailoverTimeoutSeconds: 'relay_failover_timeout_seconds',
    ResponsesWSEnabled: 'responses_ws_enabled',
    ResponsesWSDefaultMode: 'responses_ws_default_mode',
    SSEHeartbeatInterval: 'sse_heartbeat_interval',
    SSEPreStreamHeartbeatDelay: 'sse_pre_stream_heartbeat_delay',
    NonStreamKeepAliveInterval: 'nonstream_keepalive_interval',
    ProjectedChannelAutoGroupEnabled: 'projected_channel_auto_group_enabled',
    ApiBaseUrl: 'api_base_url',
    WebDAVURL: 'webdav_url',
    WebDAVUsername: 'webdav_username',
    WebDAVPassword: 'webdav_password',
    WebDAVBackupPath: 'webdav_backup_path',
    WebDAVBackupInterval: 'webdav_backup_interval',
    WebDAVRetentionCount: 'webdav_retention_count',
    WebDAVIncludeStats: 'webdav_include_stats',
    NotificationChannels: 'notification_channels',
    CheckinNotifyEnabled: 'checkin_notify_enabled',
    CheckinNotifySuccessEnabled: 'checkin_notify_success_enabled',
    CheckinNotifyManualEnabled: 'checkin_notify_manual_enabled',
    CheckinNotifyWebhookURL: 'checkin_notify_webhook_url',
    CheckinNotifyCooldownSeconds: 'checkin_notify_cooldown_seconds',
    CheckinLowBalanceThreshold: 'checkin_low_balance_threshold',
    TurnstileSolverEnabled: 'turnstile_solver_enabled',
    TurnstileCloudflareSolverEnabled: 'turnstile_cloudflare_solver_enabled',
    TurnstileCloudflareSolverAPIKey: 'turnstile_cloudflare_solver_api_key',
    TurnstileSolverQEnabled: 'turnstile_solverq_enabled',
    TurnstileSolverQAPIKey: 'turnstile_solverq_api_key',
} as const;

/**
 * 获取 Setting 列表 Hook
 * 
 * @example
 * const { data: settings, isLoading, error } = useSettingList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * settings?.forEach(setting => console.log(setting.key, setting.value));
 */
export function useSettingList() {
    return useQuery({
        queryKey: ['settings', 'list'],
        queryFn: async () => {
            return apiClient.get<Setting[]>('/api/v1/setting/list');
        },
        refetchInterval: 30000,
    });
}

export function useSettingValue(key: string, defaultValue = '') {
    const { data: settings, ...query } = useSettingList();
    return {
        ...query,
        value: settings?.find((setting) => setting.key === key)?.value ?? defaultValue,
    };
}

/**
 * 设置 Setting Hook
 * 
 * @example
 * const setSetting = useSetSetting();
 * 
 * setSetting.mutate({
 *   key: 'theme',
 *   value: 'dark',
 * });
 */
export function useSetSetting() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: Setting) => {
            return apiClient.post<Setting>('/api/v1/setting/set', data);
        },
        onSuccess: (data) => {
            logger.log('Setting 设置成功:', data.key);
            queryClient.invalidateQueries({ queryKey: ['settings', 'list'] });
        },
        onError: (error) => {
            logger.error('Setting 设置失败:', error);
        },
    });
}

export type NotificationChannel = 'webhook' | 'bark' | 'serverchan' | 'telegram' | 'smtp';

export interface NotificationTemplate {
    title?: string;
    body?: string;
    format?: '' | 'text' | 'markdown';
}

export interface NotificationConfig {
    templates?: Partial<Record<NotificationChannel, NotificationTemplate>>;
    webhook_url?: string;
    bark_url?: string;
    serverchan_key?: string;
    telegram_bot_token?: string;
    telegram_chat_id?: string;
    smtp_host?: string;
    smtp_port?: number;
    smtp_user?: string;
    smtp_password?: string;
    smtp_from?: string;
    smtp_to?: string;
    smtp_tls?: '' | 'starttls' | 'tls' | 'none';
}

export function useTestNotificationChannel() {
    return useMutation({
        mutationFn: (data: { channel: NotificationChannel; config: NotificationConfig }) =>
            apiClient.post<{ channel: NotificationChannel; success: boolean }>('/api/v1/setting/notification/test', data),
    });
}

/**
 * 数据库导入/导出
 */
export interface DBImportResult {
    rows_affected: Record<string, number>;
    already_imported?: boolean;
    issues?: Array<{ code: string; severity: string; entity?: string; id?: number }>;
}

export interface DBExportOptions {
    include_logs?: boolean;
    include_stats?: boolean;
    format?: 'json' | 'zip';
}

type ApiResponse<T> = {
    code?: number;
    message?: string;
    data?: T;
};

function isRecord(value: unknown): value is Record<string, unknown> {
    return typeof value === 'object' && value !== null;
}

function getMessageField(value: unknown): string | undefined {
    if (!isRecord(value)) return undefined;
    const msg = value.message;
    return typeof msg === 'string' ? msg : undefined;
}

function getDataField<T>(value: unknown): T | undefined {
    if (!isRecord(value)) return undefined;
    return (value as ApiResponse<T>).data;
}

function parseFilename(contentDisposition: string | null): string | null {
    if (!contentDisposition) return null;
    // e.g. attachment; filename="octopus-export-20250101120000.json"
    const match = contentDisposition.match(/filename="([^"]+)"/i);
    return match?.[1] ?? null;
}

function exportFallbackFilename(format: 'json' | 'zip' = 'json') {
    const d = new Date();
    const pad = (n: number) => String(n).padStart(2, '0');
    const ts = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
    return `octopus-export-${ts}.${format}`;
}

async function downloadBlob(blob: Blob, filename: string) {
    const url = URL.createObjectURL(blob);
    try {
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
    } finally {
        URL.revokeObjectURL(url);
    }
}

/**
 * 导出数据库（下载 JSON 文件）
 */
export function useExportDB() {
    return useMutation({
        mutationFn: async (options: DBExportOptions = {}) => {
            const format: 'json' | 'zip' = options.format ?? 'json';
            const params = new URLSearchParams();
            params.set('include_logs', String(!!options.include_logs));
            params.set('include_stats', String(!!options.include_stats));
            params.set('format', format);
            if (V2_UI) params.set('version', '2');

            const { blob, filename } = await authenticatedFetch(`/api/v1/setting/export?${params.toString()}`, {
                method: 'GET',
            }, async (res) => {
                if (!res.ok) {
                    const text = await res.text();
                    throw new Error(text || res.statusText);
                }
                return {
                    blob: await res.blob(),
                    filename: parseFilename(res.headers.get('content-disposition')) || exportFallbackFilename(format),
                };
            });
            await downloadBlob(blob, filename);
            return { filename };
        },
        onError: (error) => {
            logger.error('导出数据库失败:', error);
        },
    });
}

/**
 * 导入数据库（上传 JSON 或 ZIP 备份，增量导入）
 */
export function useImportDB() {
    return useMutation({
        mutationFn: async (file: File) => {
            const form = new FormData();
            form.append('file', file);

            return authenticatedFetch(`/api/v1/setting/import${V2_UI ? '?version=2' : ''}`, {
                method: 'POST',
                body: form,
            }, async (res) => {
                const contentType = res.headers.get('content-type') || '';
                const isJson = contentType.includes('application/json');
                const data = isJson ? await res.json() : await res.text();
                if (!res.ok) {
                    const message = getMessageField(data) ?? (typeof data === 'string' ? data : res.statusText);
                    throw new Error(message);
                }
                const nested = getDataField<DBImportResult>(data);
                return nested ?? (data as DBImportResult);
            });
        },
        onError: (error) => {
            logger.error('导入数据库失败:', error);
        },
    });
}

/**
 * WebDAV Backup
 */
export interface WebDAVBackupInfo {
    name: string;
    size: number;
    modified_at: string;
}

export function useTestWebDAV() {
    return useMutation({
        mutationFn: async () => {
            return apiClient.post<{ ok: boolean }>('/api/v1/webdav-backup/test', {});
        },
    });
}

export function useTriggerWebDAVBackup() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async () => {
            return apiClient.post<{ message: string }>(`/api/v1/webdav-backup/trigger${V2_UI ? '?version=2' : ''}`, {});
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['webdav-backups'] });
        },
    });
}

export function useWebDAVBackupList(enabled = true) {
    return useQuery({
        queryKey: ['webdav-backups'],
        queryFn: async () => {
            return apiClient.get<WebDAVBackupInfo[]>('/api/v1/webdav-backup/list');
        },
        enabled,
        refetchInterval: 30000,
    });
}

export function useRestoreWebDAVBackup() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: async (filename: string) => {
            return apiClient.post<DBImportResult>(`/api/v1/webdav-backup/restore${V2_UI ? '?version=2' : ''}`, { filename });
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['settings', 'list'] });
        },
    });
}
