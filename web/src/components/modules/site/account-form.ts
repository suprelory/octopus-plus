import { Site as SiteRecord, SiteAccount, SiteCredentialType, SitePlatform } from '@/api/endpoints/site';
import type { ProxyMode } from '@/api/endpoints/proxy-pool';
import type { Dispatch, SetStateAction } from 'react';

export type SiteAccountFormState = {
    site_id: number;
    name: string;
    credential_type: SiteCredentialType;
    username: string;
    password: string;
    access_token: string;
    api_key: string;
    refresh_token: string;
    cookie: string;
    linked_account_id: number | null;
    token_expires_at: string;
    credential_version: number;
    platform_user_id: string;
    proxy_mode: ProxyMode;
    proxy_config_id: number | null;
    enabled: boolean;
    auto_sync: boolean;
    auto_checkin: boolean;
    random_checkin: boolean;
    checkin_random_window_minutes: number;
};

export function defaultCredentialType(): SiteCredentialType {
    return SiteCredentialType.AccessToken;
}

export function credentialOptions(platform: SitePlatform) {
    switch (platform) {
        case SitePlatform.Sub2API:
            return [SiteCredentialType.AccessToken, SiteCredentialType.APIKey];
        case SitePlatform.API:
            return [SiteCredentialType.AccessToken, SiteCredentialType.APIKey];
        default:
            return [
                SiteCredentialType.AccessToken,
                SiteCredentialType.UsernamePassword,
                SiteCredentialType.APIKey,
            ];
    }
}

export function createEmptyAccountForm(site: SiteRecord): SiteAccountFormState {
    return {
        site_id: site.id,
        name: '',
        credential_type: site.kind === 'checkin'
            ? (site.checkin_http_enabled && !site.checkin_use_subscription_token ? SiteCredentialType.Cookie : SiteCredentialType.LinkedAccount)
            : defaultCredentialType(),
        username: '',
        password: '',
        access_token: '',
        api_key: '',
        refresh_token: '',
        cookie: '',
        linked_account_id: null,
        token_expires_at: '',
        credential_version: 0,
        platform_user_id: '',
        proxy_mode: 'inherit',
        proxy_config_id: null,
        enabled: true,
        auto_sync: site.kind !== 'checkin',
        auto_checkin: site.kind === 'checkin',
        random_checkin: false,
        checkin_random_window_minutes: 120,
    };
}

export function createAccountForm(account: SiteAccount): SiteAccountFormState {
    return {
        site_id: account.site_id,
        name: account.name,
        credential_type: account.credential_type,
        username: account.username,
        password: account.password,
        access_token: account.access_token,
        api_key: account.api_key,
        refresh_token: account.refresh_token ?? '',
        cookie: account.cookie ?? '',
        linked_account_id: account.linked_account_id ?? null,
        token_expires_at:
            account.token_expires_at > 0 ? String(account.token_expires_at) : '',
        credential_version: account.credential_version ?? 0,
        platform_user_id: account.platform_user_id
            ? String(account.platform_user_id)
            : '',
        proxy_mode: account.proxy_mode ?? 'inherit',
        proxy_config_id: account.proxy_config_id ?? null,
        enabled: account.enabled,
        auto_sync: account.auto_sync,
        auto_checkin: account.auto_checkin,
        random_checkin: account.random_checkin,
        checkin_random_window_minutes: account.checkin_random_window_minutes,
    };
}

export function parseTokenExpiresAtInput(value: string) {
    const trimmed = value.trim();
    if (!trimmed) {
        return 0;
    }
    if (/^\d+$/.test(trimmed)) {
        const parsed = Number(trimmed);
        if (!Number.isFinite(parsed) || parsed <= 0) {
            throw new Error('token_expires_at 必须是正整数时间戳');
        }
        return parsed < 1_000_000_000_000 ? Math.trunc(parsed * 1000) : Math.trunc(parsed);
    }
    const parsed = Date.parse(trimmed);
    if (!Number.isFinite(parsed) || parsed <= 0) {
        throw new Error('token_expires_at 必须是时间戳或可解析时间');
    }
    return Math.trunc(parsed);
}

export function getErrorMessage(error: unknown) {
    if (error instanceof Error) return error.message;
    if (typeof error === 'object' && error !== null && 'message' in error) {
        const message = (error as { message?: unknown }).message;
        if (typeof message === 'string') return message;
    }
    return '操作失败';
}

// Compare with the form's opening snapshot, not a query result that may have
// refreshed in the background while the user was editing.
export function createAccountUpdatePayload(
    payload: Partial<SiteAccount> & { id: number },
    form: SiteAccountFormState,
    initial: SiteAccountFormState,
) {
    const update = { ...payload };
    const typeChanged = form.credential_type !== initial.credential_type;
    let credentialsChanged = false;
    const fields = [
        'credential_type', 'username', 'password', 'access_token', 'api_key',
        'refresh_token', 'cookie', 'token_expires_at', 'platform_user_id',
        'proxy_mode', 'proxy_config_id',
    ] as const;
    for (const field of fields) {
        const current = form[field];
        const previous = initial[field];
        const changed = (typeof current === 'string' ? current.trim() : current) !==
            (typeof previous === 'string' ? previous.trim() : previous);
        if (!changed && !(typeChanged && field !== 'proxy_mode' && field !== 'proxy_config_id')) {
            delete update[field];
        } else {
            credentialsChanged = true;
        }
    }
    if (credentialsChanged) update.credential_version = initial.credential_version;
    else delete update.credential_version;
    return update;
}

export type AccountFormFieldsProps = {
    accountForm: SiteAccountFormState;
    setAccountForm: Dispatch<SetStateAction<SiteAccountFormState | null>>;
};
