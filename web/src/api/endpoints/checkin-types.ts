// Independent v2 API contract. Long identifiers stay strings end to end.
export const checkinPlatforms = [
    'new-api',
    'one-api',
    'one-hub',
    'done-hub',
    'anyrouter',
    'agentrouter',
    'sub2api',
    'api',
] as const;
export type CheckinPlatform = (typeof checkinPlatforms)[number];
export type CheckinStatus = 'success' | 'failed' | 'skipped' | 'pending';
export type CheckinSource = 'manual' | 'scheduled' | 'batch' | 'import' | 'auto';
export type CheckinHeader = { header_key: string; header_value: string };
export type CheckinNetwork = { proxy_mode: 'direct' | 'system' | 'pool' | 'inherit'; proxy_config_id?: number | null };
export type CheckinCapability = {
    enabled: boolean;
    can_verify: boolean;
    default_enabled: boolean;
    source: string;
    support: 'unknown' | 'supported' | 'unsupported';
    verified_at?: string;
};
export type CheckinDefaults = Record<CheckinPlatform, { enabled: boolean; has_builtin: boolean }>;

export interface CheckinStatusRule {
    method: 'GET' | 'POST';
    path: string;
    body: string;
    headers: CheckinHeader[];
    skip_precheck: boolean;
    checked_path: string;
    checked_value: unknown;
    unchecked_value: unknown;
    date_path: string;
    code: string;
    initial_delay_ms: number;
    poll_interval_ms: number;
    max_attempts: number;
    timeout_seconds: number;
    confirm_timeout_minutes: number;
    reward_source: 'submit' | 'status';
}
export interface CheckinSiteInput extends CheckinNetwork {
    name: string;
    platform: CheckinPlatform;
    base_url: string;
    enabled: boolean;
    custom_header: CheckinHeader[];
    external_checkin_url: string | null;
    checkin_mode: 'auto' | 'enabled' | 'disabled';
    checkin_http_enabled: boolean;
    checkin_http_method: 'GET' | 'POST';
    checkin_http_path: string;
    checkin_http_body: string;
    checkin_http_headers: CheckinHeader[];
    checkin_status: CheckinStatusRule | null;
    checkin_reward_extractor: string;
    checkin_timezone: string;
    checkin_window_start: string;
    checkin_window_end: string;
    is_pinned: boolean;
    sort_order: number;
    tags: string[];
}
export interface CheckinSite extends Omit<CheckinSiteInput, 'checkin_status' | 'external_checkin_url'> {
    id: number;
    archived: boolean;
    archived_at?: string;
    verification_status: string;
    verified_at?: string;
    capability: CheckinCapability;
    checkin_status?: CheckinStatusRule | null;
    external_checkin_url?: string | null;
}
export type CredentialType = 'password' | 'access_token' | 'api_key' | 'cookie';
export type CredentialSecret = 'password' | 'access_token' | 'refresh_token' | 'api_key' | 'cookie';
export interface CheckinCredentialInput extends CheckinNetwork {
    name: string;
    type: CredentialType;
    platform: CheckinPlatform;
    auth_base_url: string;
    username?: string;
    platform_user_id?: number | null;
    token_expires_at?: number;
    auth_headers?: CheckinHeader[] | null;
    password?: string;
    access_token?: string;
    refresh_token?: string;
    api_key?: string;
    cookie?: string;
}
export interface CheckinCredential extends CheckinNetwork {
    id: number;
    name: string;
    type: CredentialType;
    platform: CheckinPlatform;
    auth_base_url: string;
    username: string;
    platform_user_id?: number;
    token_expires_at: number;
    credential_version: number;
    identity_revision: number;
    configured: boolean;
    session_state: 'idle' | 'exchanging' | 'needs_credentials' | 'cooldown';
    session_retry_at?: number;
    has_password: boolean;
    has_access_token: boolean;
    has_refresh_token: boolean;
    has_api_key: boolean;
    has_cookie: boolean;
    has_auth_headers: boolean;
    reference_count: number;
    primary_reference_count: number;
    balance_reference_count: number;
}
export type CheckinCredentialPatch = Partial<CheckinCredentialInput> & { expected_version: number };
export interface CheckinBalanceConfig extends Partial<CheckinNetwork> {
    mode: 'disabled' | 'primary' | 'custom';
    platform?: CheckinPlatform;
    base_url?: string;
    credential_id?: number | null;
    headers?: CheckinHeader[];
}
export interface CheckinAccountInput extends CheckinNetwork {
    name: string;
    credential_id: number;
    auth_mode: 'platform' | 'cookie' | 'bearer';
    enabled: boolean;
    auto_checkin: boolean;
    random_checkin: boolean;
    checkin_random_window_minutes: number;
    balance_config: CheckinBalanceConfig | null;
}
export type CheckinAccountCreate = Omit<CheckinAccountInput, 'credential_id'> & { site_id: number } & (
        { credential_id: number; credential?: never } | { credential: CheckinCredentialInput; credential_id?: never }
    );
export interface CheckinPending {
    log_id: string;
    batch_job_id?: string;
    source: CheckinSource;
    date: string;
    started_at: string;
    deadline: string;
    submitted: boolean;
    finished: boolean;
    status: CheckinStatus;
    reason: string;
}
export interface CheckinAccount extends CheckinAccountInput {
    id: number;
    site_id: number;
    balance: number;
    balance_used: number;
    today_income: number;
    balance_updated_at?: string;
    next_auto_checkin_at?: string;
    last_checkin_at?: string;
    last_checkin_success_at?: string;
    last_checkin_status: CheckinStatus | '';
    last_checkin_message: string;
    checkin_failure_count: number;
    checkin_pending?: CheckinPending;
}
export interface CheckinResult {
    status: CheckinStatus;
    reason: string;
    message: string;
    reward?: string;
    log_id: string;
    site_id: number;
    account_id: number;
    date: string;
    submitted: boolean;
    replayed: boolean;
}
export interface CheckinBalance {
    account_id: number;
    balance: number;
    balance_used: number | null;
    today_income: number | null;
}
export interface CheckinLog {
    id: string;
    site_id: number;
    account_id: number;
    site_name: string;
    account_name: string;
    platform: CheckinPlatform;
    source: CheckinSource;
    status: CheckinStatus;
    reason: string;
    message: string;
    reward: string;
    batch_job_id?: string;
    duration_ms: number;
    started_at: string;
    finished_at: string;
}
export interface CheckinStats {
    today_reward: number;
    recent_7_days_reward: number;
    recent_30_days_reward: number;
    total_reward: number;
    total_count: number;
    success_count: number;
    failed_count: number;
    skipped_count: number;
    pending_count: number;
    invalid_reward_count: number;
    unknown_reward_count: number;
    timezone: string;
    by_site: {
        site_id: number;
        site_name: string;
        reward: number;
        total_count: number;
        success_count: number;
        failed_count: number;
        skipped_count: number;
        pending_count: number;
    }[];
}
export type CheckinBatchSelection = { scope: 'all'; site_ids?: never } | { scope: 'selected'; site_ids: number[] };
export interface CheckinBatch {
    id: string;
    status: 'queued' | 'running' | 'completed' | 'completed_with_errors' | 'canceled' | 'failed' | 'interrupted';
    trigger: string;
    site_ids?: number[];
    total: number;
    attempted: number;
    success: number;
    pending: number;
    failed: number;
    skipped: number;
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
}
export interface CheckinNotification {
    id: string;
    completion_id: string;
    destination: string;
    event: string;
    threshold: number;
    state: 'queued' | 'sending' | 'delivered' | 'suppressed' | 'canceled' | 'uncertain';
    attempts: number;
    started_at: number;
    finished_at: number;
}
export type CheckinPage<T> = { items: T[]; next_before_id?: string };
export type CheckinHistoryFilter = {
    site_id?: number;
    account_id?: number;
    status?: CheckinStatus;
    source?: CheckinSource;
    batch_id?: string;
    before_id?: string;
    from?: string;
    until?: string;
};
