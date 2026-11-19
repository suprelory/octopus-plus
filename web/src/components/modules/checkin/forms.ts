import type {
    CheckinAccount,
    CheckinAccountInput,
    CheckinCredential,
    CheckinCredentialInput,
    CheckinCredentialPatch,
    CheckinSite,
    CheckinSiteInput,
    CheckinStatusRule,
    CredentialSecret,
    CredentialType,
} from '@/api/endpoints/checkin-types';

export const credentialSecretFields: Record<CredentialType, CredentialSecret[]> = {
    password: ['password'],
    access_token: ['access_token', 'refresh_token'],
    api_key: ['api_key'],
    cookie: ['cookie'],
};
export function siteDraft(site?: CheckinSite): CheckinSiteInput {
    return {
        name: site?.name ?? '',
        platform: site?.platform ?? 'new-api',
        base_url: site?.base_url ?? '',
        enabled: site?.enabled ?? true,
        proxy_mode: site?.proxy_mode ?? 'direct',
        proxy_config_id: site?.proxy_config_id ?? null,
        custom_header: site?.custom_header ?? [],
        external_checkin_url: site?.external_checkin_url ?? null,
        checkin_mode: site?.checkin_mode ?? 'auto',
        checkin_http_enabled: site?.checkin_http_enabled ?? false,
        checkin_http_method: site?.checkin_http_method || 'POST',
        checkin_http_path: site?.checkin_http_path ?? '',
        checkin_http_body: site?.checkin_http_body ?? '',
        checkin_http_headers: site?.checkin_http_headers ?? [],
        checkin_status: site?.checkin_status ?? null,
        checkin_reward_extractor: site?.checkin_reward_extractor ?? '',
        checkin_timezone: site?.checkin_timezone ?? 'Asia/Shanghai',
        checkin_window_start: site?.checkin_window_start ?? '00:00',
        checkin_window_end: site?.checkin_window_end ?? '23:59',
        is_pinned: site?.is_pinned ?? false,
        sort_order: site?.sort_order ?? 0,
        tags: site?.tags ?? [],
    };
}
export function statusDraft(): CheckinStatusRule {
    return {
        method: 'GET',
        path: '',
        body: '',
        headers: [],
        skip_precheck: false,
        checked_path: '',
        checked_value: true,
        unchecked_value: false,
        date_path: '',
        code: '',
        initial_delay_ms: 1000,
        poll_interval_ms: 2000,
        max_attempts: 3,
        timeout_seconds: 10,
        confirm_timeout_minutes: 30,
        reward_source: 'status',
    };
}
export function accountDraft(site: CheckinSite, account?: CheckinAccount): CheckinAccountInput {
    return {
        name: account?.name ?? '',
        credential_id: account?.credential_id ?? 0,
        auth_mode: account?.auth_mode ?? (site.checkin_http_enabled ? 'cookie' : 'platform'),
        enabled: account?.enabled ?? true,
        auto_checkin: account?.auto_checkin ?? false,
        random_checkin: account?.random_checkin ?? false,
        checkin_random_window_minutes: account?.checkin_random_window_minutes ?? 120,
        proxy_mode: account?.proxy_mode ?? 'inherit',
        proxy_config_id: account?.proxy_config_id ?? null,
        balance_config: account?.balance_config ?? { mode: 'disabled' },
    };
}
export function credentialDraft(credential?: CheckinCredential): CheckinCredentialInput {
    return {
        name: credential?.name ?? '',
        type: credential?.type ?? 'password',
        platform: credential?.platform ?? 'new-api',
        auth_base_url: credential?.auth_base_url ?? '',
        username: credential?.username ?? '',
        proxy_mode: credential?.proxy_mode ?? 'direct',
        proxy_config_id: credential?.proxy_config_id ?? null,
        platform_user_id: credential?.platform_user_id ?? null,
        token_expires_at: credential?.token_expires_at ?? 0,
        auth_headers: [],
        password: '',
        access_token: '',
        refresh_token: '',
        api_key: '',
        cookie: '',
    };
}

// Omitted secrets stay omitted. Summaries and browser password placeholders must
// never be spread into API patches; keep the version from when the editor opened.
export function credentialPayload(
    draft: CheckinCredentialInput,
    original?: CheckinCredential,
    cleared: CredentialSecret[] = [],
    replaceHeaders = false,
): CheckinCredentialInput | CheckinCredentialPatch {
    const payload: CheckinCredentialInput | CheckinCredentialPatch = {
        name: draft.name.trim(),
        type: draft.type,
        platform: draft.platform,
        auth_base_url: draft.auth_base_url.trim(),
        username: draft.username,
        proxy_mode: draft.proxy_mode,
        proxy_config_id: draft.proxy_config_id,
        platform_user_id: draft.platform_user_id,
        token_expires_at: draft.token_expires_at,
        ...(original ? { expected_version: original.credential_version } : {}),
    };
    // Switching type must not submit values from fields that are now hidden.
    for (const key of credentialSecretFields[draft.type]) {
        if (cleared.includes(key)) payload[key] = '';
        else if (draft[key]) payload[key] = draft[key];
    }
    if (!original || replaceHeaders) payload.auth_headers = draft.auth_headers ?? [];
    return payload;
}
export function credentialMatches(
    site: CheckinSite,
    mode: CheckinAccountInput['auth_mode'],
    credential: CheckinCredential,
) {
    if (site.checkin_http_enabled)
        return mode === 'cookie'
            ? credential.type === 'cookie'
            : mode === 'bearer' && credential.type === 'access_token';
    if (
        mode !== 'platform' ||
        credential.platform !== site.platform ||
        credential.auth_base_url.replace(/\/+$/, '') !== site.base_url.replace(/\/+$/, '')
    )
        return false;
    if (site.platform === 'agentrouter') return credential.type === 'password';
    return (
        credential.type === 'password' ||
        credential.type === 'access_token' ||
        (site.platform === 'anyrouter' && credential.type === 'cookie')
    );
}
export function balanceCredentialMatches(platform: string, credential: CheckinCredential) {
    return (
        credential.platform === platform &&
        (platform === 'sub2api'
            ? credential.type === 'access_token' || credential.type === 'api_key'
            : platform !== 'api' &&
              (credential.type === 'password' || credential.type === 'access_token' || credential.type === 'cookie'))
    );
}
export function canConfirm(site: CheckinSite, account: CheckinAccount) {
    const pending = account.checkin_pending;
    return !!pending?.submitted && !pending.finished && !!(site.checkin_status || site.platform === 'agentrouter');
}
export function externalLink(value?: string | null) {
    if (!value) return undefined;
    try {
        const url = new URL(value);
        return ['http:', 'https:'].includes(url.protocol) ? url.href : undefined;
    } catch {
        return undefined;
    }
}
