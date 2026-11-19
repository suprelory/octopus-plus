import { expect, type Page } from '@playwright/test';
import type {
    CheckinAccount,
    CheckinBatch,
    CheckinCredential,
    CheckinHistoryFilter,
    CheckinLog,
    CheckinNotification,
    CheckinSite,
    CheckinStats,
} from '../../src/api/endpoints/checkin-types';
import { mockApp } from './fixtures';

export const longID = '9007199254740993';
export const nextID = '9007199254740995';
const timestamp = '2026-11-20T09:00:00Z';
export function checkinSite(overrides: Partial<CheckinSite> = {}): CheckinSite {
    return {
        id: 1,
        name: 'Independent site',
        platform: 'new-api',
        base_url: 'https://checkin.example',
        enabled: true,
        archived: false,
        proxy_mode: 'direct',
        custom_header: [],
        checkin_mode: 'auto',
        checkin_http_enabled: false,
        checkin_http_method: 'POST',
        checkin_http_path: '',
        checkin_http_body: '',
        checkin_http_headers: [],
        checkin_reward_extractor: '',
        checkin_timezone: 'Asia/Shanghai',
        checkin_window_start: '00:00',
        checkin_window_end: '23:59',
        verification_status: 'supported',
        is_pinned: false,
        sort_order: 0,
        tags: ['Daily'],
        capability: {
            enabled: true,
            can_verify: true,
            default_enabled: true,
            source: 'platform_default',
            support: 'supported',
        },
        ...overrides,
    };
}
export function checkinCredential(overrides: Partial<CheckinCredential> = {}): CheckinCredential {
    return {
        id: 1,
        name: 'Independent credential',
        type: 'password',
        platform: 'new-api',
        auth_base_url: 'https://checkin.example',
        proxy_mode: 'direct',
        username: 'demo',
        token_expires_at: 0,
        credential_version: 7,
        identity_revision: 1,
        configured: true,
        session_state: 'idle',
        has_password: true,
        has_access_token: false,
        has_refresh_token: false,
        has_api_key: false,
        has_cookie: false,
        has_auth_headers: true,
        reference_count: 1,
        primary_reference_count: 1,
        balance_reference_count: 0,
        ...overrides,
    };
}
export function checkinAccount(overrides: Partial<CheckinAccount> = {}): CheckinAccount {
    return {
        id: 1,
        site_id: 1,
        name: 'Independent account',
        credential_id: 1,
        auth_mode: 'platform',
        proxy_mode: 'inherit',
        enabled: true,
        auto_checkin: true,
        random_checkin: false,
        checkin_random_window_minutes: 120,
        balance_config: { mode: 'primary' },
        balance: 0,
        balance_used: 0,
        today_income: 0,
        last_checkin_status: '',
        last_checkin_message: '',
        checkin_failure_count: 0,
        ...overrides,
    };
}
export function checkinBatch(overrides: Partial<CheckinBatch> = {}): CheckinBatch {
    return {
        id: longID,
        status: 'running',
        trigger: 'manual',
        total: 1,
        attempted: 0,
        success: 0,
        pending: 0,
        failed: 0,
        skipped: 0,
        canceled: false,
        duration_ms: 0,
        started_at: timestamp,
        updated_at: timestamp,
        current_site_name: 'Independent site',
        current_account_name: 'Independent account',
        ...overrides,
    };
}
export function checkinLog(overrides: Partial<CheckinLog> = {}): CheckinLog {
    return {
        id: nextID,
        site_id: 1,
        account_id: 1,
        site_name: 'Independent site',
        account_name: 'Independent account',
        platform: 'new-api',
        source: 'manual',
        status: 'success',
        reason: 'checked_in',
        message: 'Check-in complete',
        reward: '0',
        duration_ms: 50,
        started_at: timestamp,
        finished_at: timestamp,
        ...overrides,
    };
}
export function notification(overrides: Partial<CheckinNotification> = {}): CheckinNotification {
    return {
        id: longID,
        completion_id: `${longID}:success`,
        destination: 'a'.repeat(64),
        event: 'site_checkin_success',
        threshold: 0,
        state: 'uncertain',
        attempts: 1,
        started_at: Date.parse(timestamp),
        finished_at: Date.parse(timestamp),
        ...overrides,
    };
}
export type CheckinMutation = { path: string; method: string; body: Record<string, unknown> | null };
type Response = { data?: unknown; status?: number; errorCode?: string; message?: string };

export async function installCheckinAPI(
    page: Page,
    options: {
        sites?: CheckinSite[];
        accounts?: CheckinAccount[];
        credentials?: CheckinCredential[];
        logs?: CheckinLog[];
        notifications?: CheckinNotification[];
        locale?: 'en' | 'zh_hans' | 'zh_hant';
        mutate?: (request: CheckinMutation) => Response | undefined;
    } = {},
) {
    const legacy = await mockApp(page, 'checkin');
    if (options.locale)
        await page.addInitScript(
            (locale) => localStorage.setItem('octopus-settings', JSON.stringify({ state: { locale }, version: 0 })),
            options.locale,
        );
    const state = {
        sites: options.sites ?? [checkinSite()],
        accounts: options.accounts ?? [checkinAccount()],
        credentials: options.credentials ?? [checkinCredential()],
        logs: options.logs ?? [],
        notifications: options.notifications ?? [],
        batches: [] as CheckinBatch[],
        reads: [] as string[],
        writes: [] as CheckinMutation[],
        legacyRequests: [] as string[],
        batchPolls: 0,
        pageErrors: legacy.pageErrors,
        unexpected: [] as string[],
        filters: [] as CheckinHistoryFilter[],
    };
    page.on('request', (request) => {
        if (/\/api\/v1\/(site(?:\/|-channel)|site-channel)/.test(request.url()))
            state.legacyRequests.push(request.url());
    });
    function references() {
        for (const credential of state.credentials) {
            credential.primary_reference_count = state.accounts.filter(
                (account) => account.credential_id === credential.id,
            ).length;
            credential.balance_reference_count = state.accounts.filter(
                (account) => account.balance_config?.credential_id === credential.id,
            ).length;
            credential.reference_count = credential.primary_reference_count + credential.balance_reference_count;
        }
    }
    function addCredential(body: Record<string, unknown>) {
        const credential = checkinCredential({
            id: Math.max(0, ...state.credentials.map((item) => item.id)) + 1,
            name: body.name as string,
            type: body.type as CheckinCredential['type'],
            platform: body.platform as CheckinCredential['platform'],
            auth_base_url: body.auth_base_url as string,
            username: (body.username as string) ?? '',
            proxy_mode: (body.proxy_mode as CheckinCredential['proxy_mode']) ?? 'direct',
            credential_version: 0,
            has_password: !!body.password,
            has_cookie: !!body.cookie,
            has_access_token: !!body.access_token,
            has_api_key: !!body.api_key,
            has_refresh_token: !!body.refresh_token,
            has_auth_headers: false,
            reference_count: 0,
        });
        state.credentials.push(credential);
        return credential;
    }
    await page.route('**/api/v1/checkin/**', async (route) => {
        const request = route.request(),
            url = new URL(request.url()),
            path = url.pathname.replace('/api/v1/checkin', ''),
            method = request.method();
        let result: Response | undefined;
        if (method === 'GET') {
            state.reads.push(url.pathname + url.search);
            references();
            const before = url.searchParams.get('before_id');
            const paged = <T extends { id: string }>(items: T[]) => {
                const rows = items.filter((item) => !before || BigInt(item.id) < BigInt(before));
                return { items: rows.slice(0, 1), ...(rows.length > 1 ? { next_before_id: rows[0].id } : {}) };
            };
            if (path === '/sites') result = { data: state.sites };
            if (path === '/accounts') result = { data: state.accounts };
            if (path === '/credentials') result = { data: state.credentials };
            if (path === '/capabilities')
                result = {
                    data: Object.fromEntries(
                        ['new-api', 'one-api', 'one-hub', 'done-hub', 'anyrouter', 'agentrouter', 'sub2api', 'api'].map(
                            (platform) => [
                                platform,
                                { enabled: true, has_builtin: !['api', 'sub2api'].includes(platform) },
                            ],
                        ),
                    ),
                };
            if (path === '/logs') {
                state.filters.push(Object.fromEntries(url.searchParams));
                result = { data: paged(state.logs) };
            }
            if (path === '/stats') {
                const stats: CheckinStats = {
                    today_reward: 0,
                    recent_7_days_reward: 0.5,
                    recent_30_days_reward: 0.5,
                    total_reward: 0.5,
                    total_count: state.logs.length,
                    success_count: state.logs.length,
                    failed_count: 0,
                    skipped_count: 0,
                    pending_count: 0,
                    invalid_reward_count: 0,
                    unknown_reward_count: 1,
                    timezone: url.searchParams.get('timezone') ?? 'Asia/Shanghai',
                    by_site: [],
                };
                result = { data: stats };
            }
            if (path === '/notifications') result = { data: paged(state.notifications) };
            if (path === '/batches') result = { data: paged(state.batches) };
            if (path.startsWith('/batches/')) {
                state.batchPolls++;
                const job = state.batches.find((item) => item.id === path.split('/')[2]);
                if (job && state.batchPolls >= 2)
                    Object.assign(job, { status: 'completed', attempted: 1, success: 1, finished_at: timestamp });
                result = { data: job };
            }
        } else {
            const body = request.postData() ? (request.postDataJSON() as Record<string, unknown>) : null;
            const mutation = { method, path, body };
            state.writes.push(mutation);
            result = options.mutate?.(mutation);
            if (!result && body && path === '/sites' && method === 'POST') {
                const site = checkinSite({ ...body, id: state.sites.length + 1 } as Partial<CheckinSite>);
                state.sites.push(site);
                result = { data: site };
            }
            if (!result && body && path === '/credentials' && method === 'POST') result = { data: addCredential(body) };
            if (!result && body && path === '/accounts' && method === 'POST') {
                const credential = body.credential
                    ? addCredential(body.credential as Record<string, unknown>)
                    : undefined;
                const fields = { ...body };
                delete fields.credential;
                const account = checkinAccount({
                    ...fields,
                    id: state.accounts.length + 1,
                    credential_id: credential?.id ?? body.credential_id,
                } as Partial<CheckinAccount>);
                state.accounts.push(account);
                result = { data: account };
            }
            const [, resource, id, operation] = path.split('/');
            const list =
                resource === 'sites'
                    ? state.sites
                    : resource === 'accounts'
                      ? state.accounts
                      : resource === 'credentials'
                        ? state.credentials
                        : [];
            const item = list.find((row) => row.id === Number(id));
            if (!result && method === 'PATCH' && item) {
                if (
                    resource === 'credentials' &&
                    body?.expected_version !== (item as CheckinCredential).credential_version
                )
                    result = { status: 409, errorCode: 'checkin.credential_conflict' };
                else {
                    const fields = { ...body };
                    if (resource === 'credentials') {
                        const credential = item as CheckinCredential;
                        for (const key of ['password', 'access_token', 'refresh_token', 'api_key', 'cookie'] as const) {
                            if (key in fields) credential[`has_${key}`] = !!fields[key];
                            delete fields[key];
                        }
                        if ('auth_headers' in fields)
                            credential.has_auth_headers = !!(fields.auth_headers as unknown[]).length;
                        delete fields.auth_headers;
                        delete fields.expected_version;
                        credential.credential_version++;
                    }
                    Object.assign(item, fields);
                    result = { data: item };
                }
            }
            if (!result && method === 'DELETE' && item) {
                list.splice(list.indexOf(item as never), 1);
                result = { data: { id: Number(id) } };
            }
            if (!result && resource === 'sites' && item && ['archive', 'restore'].includes(operation)) {
                (item as CheckinSite).archived = operation === 'archive';
                result = { data: item };
            }
            if (!result && resource === 'accounts' && item && operation === 'balance')
                result = { data: { account_id: Number(id), balance: 0, balance_used: 0, today_income: null } };
            if (!result && resource === 'accounts' && item && ['execute', 'confirm'].includes(operation)) {
                const account = item as CheckinAccount;
                const replayed = account.last_checkin_status === 'success';
                account.last_checkin_status = 'success';
                account.last_checkin_at = timestamp;
                if (account.checkin_pending) account.checkin_pending.finished = true;
                result = {
                    data: {
                        log_id: longID,
                        site_id: account.site_id,
                        account_id: account.id,
                        date: '2026-11-20',
                        status: 'success',
                        reason: 'checked_in',
                        message: 'Success',
                        reward: '0',
                        submitted: true,
                        replayed,
                    },
                };
            }
            if (!result && path === '/batches' && method === 'POST') {
                const job = checkinBatch(body?.scope === 'selected' ? { site_ids: body.site_ids as number[] } : {});
                state.batches.push(job);
                result = { data: job };
            }
            if (!result && resource === 'notifications' && operation === 'retry') {
                const delivery = state.notifications.find((row) => row.id === id);
                if (delivery) {
                    delivery.state = 'delivered';
                    delivery.attempts++;
                    result = { data: { id, state: 'queued' } };
                }
            }
            if (!result && path === '/reward/test') result = { data: { reward: '0', found: true } };
            if (!result && path === '/status/test') result = { data: { state: 'checked_in' } };
        }
        if (!result) state.unexpected.push(`${method} ${path}`);
        await route.fulfill({
            status: result?.status ?? (result ? 200 : 500),
            json: {
                code: result?.status ?? 200,
                data: result?.data,
                error_code: result?.errorCode,
                message: result?.message ?? 'fixture response',
            },
        });
    });
    return state;
}
export function expectIndependent(state: Awaited<ReturnType<typeof installCheckinAPI>>) {
    expect(state.legacyRequests).toEqual([]);
    expect(state.unexpected).toEqual([]);
    expect(state.pageErrors).toEqual([]);
}
