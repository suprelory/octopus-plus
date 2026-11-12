import { expect, type Page } from '@playwright/test';
import type { Site } from '../../src/api/endpoints/site';
import type { SiteChannelCard } from '../../src/api/endpoints/site-channel';
import type { Group } from '../../src/api/endpoints/group';
import type { Channel } from '../../src/api/endpoints/channel';
import type { StatsDaily, StatsHourly, StatsTotal } from '../../src/api/endpoints/stats';
import type { LLMInfo } from '../../src/api/endpoints/model';
import type { RelayLog } from '../../src/api/endpoints/log';
import type { APIKey, APIKeyStatsResponse } from '../../src/api/endpoints/apikey';
import type { Setting } from '../../src/api/endpoints/setting';
import type { NavItem } from '../../src/components/modules/navbar/nav-store';
import type { ApiErrorParams } from '../../src/api/types';
import { APP_VERSION } from '../../src/lib/info';

export const emptyStats = { input_token: 0, output_token: 0, input_cost: 0, output_cost: 0, wait_time: 0, request_success: 0, request_failed: 0 };

export function makeChannel(id = 1, name = 'OpenAI Primary', overrides: Partial<Channel> = {}): Channel {
    return {
        id, name, type: 0, enabled: true, base_urls: [{ url: 'https://api.example/v1', delay: 120 }],
        keys: [{ id, channel_id: id, enabled: true, channel_key: 'demo-key', status_code: 200, last_use_time_stamp: 0, total_cost: 0, remark: '' }],
        model: 'gpt-4.1,gpt-4.1-mini', custom_model: '', proxy_mode: 'direct', auto_sync: false,
        auto_group: 0, custom_header: [], ws_mode: 'inherit', passthrough_mode: 'auto', managed: false,
        stats: { ...emptyStats, channel_id: id, request_success: 1200 * id, request_failed: 5 * id, input_token: 250000 * id, output_token: 120000 * id, input_cost: 12.8 * id, output_cost: 8.6 * id },
        ...overrides,
    };
}

export function makeSite(id = 1, name = 'Alpha site'): Site {
    return {
        id, name, kind: 'relay', platform: 'new-api' as Site['platform'], base_url: `https://site-${id}.example`,
        enabled: true, proxy_mode: 'direct', checkin_timezone: 'Asia/Shanghai',
        checkin_mode: 'auto',
        checkin_capability: { enabled: true, can_verify: true, default_enabled: true, source: 'platform_default', support: 'unknown' },
        checkin_window_start: '00:00', checkin_window_end: '23:59', is_pinned: false,
        sort_order: 0, global_weight: 1, custom_header: [], route_base_urls: [], tags: [],
        archived: false, accounts: [],
    };
}

export function makeCheckinSite(id = 1, name = 'Alpha site'): Site {
    return { ...makeSite(id, name), kind: 'checkin' };
}

export function makeSiteChannelCard(): SiteChannelCard {
    return {
        site_id: 1, site_name: 'Alpha site', base_url: 'https://site-1.example',
        platform: 'new-api' as Site['platform'], enabled: true, account_count: 1,
        accounts: [{
            site_id: 1, account_id: 11, account_name: 'Primary account', enabled: true,
            auto_sync: false, group_count: 1, model_count: 2,
            route_summaries: [{ route_type: 'openai_chat', count: 1 }, { route_type: 'anthropic', count: 1 }],
            groups: [{
                group_key: 'default', group_name: 'Default group', projection_disabled: false,
                projection_suspended: false, model_sync_status: 'synced', model_sync_authoritative: true,
                model_sync_model_count: 2, model_sync_failure_count: 0, key_count: 1,
                enabled_key_count: 1, masked_pending_key_count: 0, has_keys: true,
                has_projected_channel: true, projected_channel_ids: [101, 102],
                projected_channels: [{
                    channel_id: 101, channel_name: 'Chat route', route_type: 'openai_chat',
                    auto_group: 0, effective_auto_group: 0, global_override: false,
                    param_override: '{"temperature":0.2}',
                }, {
                    channel_id: 102, channel_name: 'Messages route', route_type: 'anthropic',
                    auto_group: 1, effective_auto_group: 1, global_override: false,
                    param_override: '{"max_tokens":64}',
                }],
                source_keys: [], projected_keys: [],
                models: [{
                    model_name: 'chat-model', source: 'sync', route_type: 'openai_chat',
                    route_source: 'sync_inferred', manual_override: false, disabled: false, projected_channel_id: 101,
                }, {
                    model_name: 'messages-model', source: 'sync', route_type: 'anthropic',
                    route_source: 'sync_inferred', manual_override: false, disabled: false, projected_channel_id: 102,
                }],
            }],
        }],
    };
}

export type Mutation = { method: string; path: string; body: unknown };
type MockResponse = { status?: number; data?: unknown; message?: string; errorCode?: string; params?: ApiErrorParams };

export async function mockApp(page: Page, nav: NavItem, options: {
    sites?: Site[];
    siteChannels?: SiteChannelCard[];
    groups?: Group[];
    channels?: Channel[];
    statsDaily?: StatsDaily[];
    statsHourly?: StatsHourly[];
    statsTotal?: StatsTotal;
    models?: LLMInfo[];
    logs?: RelayLog[];
    settings?: Setting[];
    apiKeys?: APIKey[];
    auth?: 'admin' | 'apikey' | 'guest';
    credentials?: { token: string; expireAt: string };
    bootstrapRequired?: boolean;
    apiKeyStats?: APIKeyStatsResponse;
    mutate?: (request: Mutation) => MockResponse | Promise<MockResponse>;
} = {}) {
    const state = {
        sites: options.sites ?? [nav === 'checkin' ? makeCheckinSite() : makeSite()],
        siteChannels: options.siteChannels ?? [makeSiteChannelCard()],
        groups: options.groups ?? [],
        channels: options.channels ?? [],
        models: options.models ?? [],
        logs: options.logs ?? [],
        mutations: [] as Mutation[],
        unexpectedRequests: [] as string[],
        pageErrors: [] as string[],
    };
    page.on('pageerror', error => state.pageErrors.push(error.message));
    await page.addInitScript(({ activeItem, auth, credentials }) => {
        if (auth !== 'guest') localStorage.setItem('auth-storage', JSON.stringify({
            state: { token: credentials?.token ?? 'browser-test-token', expireAt: credentials?.expireAt ?? '2099-01-01T00:00:00Z', isAPIKeyAuth: auth === 'apikey' }, version: 0,
        }));
        localStorage.setItem('nav-storage', JSON.stringify({ state: { activeItem, prevItem: null, direction: 0 }, version: 0 }));
        localStorage.setItem('octopus-settings', JSON.stringify({ state: { locale: 'zh_hans' }, version: 0 }));
    }, { activeItem: nav, auth: options.auth ?? 'admin', credentials: options.credentials });
    await page.route('**/api/v1/**', async route => {
        const request = route.request();
        const path = new URL(request.url()).pathname;
        if (request.method() !== 'GET') {
            const mutation = { method: request.method(), path, body: request.postData() ? request.postDataJSON() as unknown : null };
            state.mutations.push(mutation);
            const response = await options.mutate?.(mutation);
            if (!response) state.unexpectedRequests.push(`${request.method()} ${path}`);
            await route.fulfill({
                status: response?.status ?? (response ? 200 : 500),
                json: {
                    code: response?.status ?? (response ? 200 : 500),
                    data: response?.data ?? null,
                    message: response?.message ?? 'Unexpected mutation',
                    error_code: response?.errorCode,
                    params: response?.params,
                },
            });
            return;
        }
        const data: Record<string, unknown> = {
            '/api/v1/user/status': null,
            '/api/v1/user/bootstrap': { required: options.bootstrapRequired ?? false },
            '/api/v1/site/list': state.sites,
            '/api/v1/site/checkin-capabilities': {
                'new-api': { enabled: true, has_builtin: true }, 'one-api': { enabled: true, has_builtin: true },
                'one-hub': { enabled: true, has_builtin: true }, 'anyrouter': { enabled: true, has_builtin: true },
                'agentrouter': { enabled: true, has_builtin: true },
                'done-hub': { enabled: false, has_builtin: true }, 'sub2api': { enabled: false, has_builtin: false },
                'api': { enabled: false, has_builtin: false },
            },
            '/api/v1/site-channel/list': state.siteChannels,
            '/api/v1/channel/list': state.channels,
            '/api/v1/stats/daily': options.statsDaily ?? [],
            '/api/v1/stats/hourly': options.statsHourly ?? [],
            '/api/v1/stats/total': options.statsTotal ?? { id: 1, ...emptyStats },
            '/api/v1/group/list': state.groups,
            '/api/v1/model/channel': [],
            '/api/v1/model/list': state.models,
            '/api/v1/setting/list': options.settings ?? [],
            '/api/v1/proxy-pool/list': [],
            '/api/v1/apikey/list': options.apiKeys ?? [],
            '/api/v1/apikey/login': null,
            '/api/v1/apikey/stats': options.apiKeyStats ?? null,
            '/api/v1/update': { tag_name: APP_VERSION, published_at: '', body: '', message: '' },
            '/api/v1/update/now-version': APP_VERSION,
            '/api/v1/model/last-update-time': '',
            '/api/v1/channel/last-sync-time': '',
            '/api/v1/site/last-sync-time': '',
            '/api/v1/site/last-checkin-time': '',
            '/api/v1/site/checkin-batches/latest': null,
            '/api/v1/site/checkin-stats': {
                today_reward: 0, recent_7_days_reward: 0, recent_30_days_reward: 0, total_reward: 0,
                total_count: 0, success_count: 0, failed_count: 0, skipped_count: 0,
                invalid_reward_count: 0, unknown_reward_count: 0, timezone: 'Asia/Shanghai', by_site: [],
            },
            '/api/v1/webdav-backup/list': [],
            '/api/v1/log/site-action-targets': {},
        };
        if (path === '/api/v1/log/list') {
            const keyword = new URL(request.url()).searchParams.get('keyword')?.toLowerCase();
            const logs = keyword ? state.logs.filter(log => log.request_model_name.toLowerCase().includes(keyword)) : state.logs;
            data[path] = { logs, total: logs.length, total_exact: true };
        }
        const detail = state.logs.find(log => path === `/api/v1/log/${log.id}`);
        if (detail) data[path] = { ...detail, request_content: '{"messages":[]}', response_content: '{"id":"response-demo"}' };
        if (!(path in data)) state.unexpectedRequests.push(`GET ${path}`);
        await route.fulfill({ status: path in data ? 200 : 500, json: { code: 200, data: data[path] ?? null } });
    });
    return state;
}

export async function openAdvancedSettings(page: Page) {
    await page.getByText('Alpha site', { exact: true }).click();
    const groupPicker = page.getByRole('combobox').filter({ hasText: '分组' });
    await groupPicker.click();
    await page.getByRole('option', { name: /Default group/ }).click();
    await page.getByRole('button', { name: '高级', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '站点渠道高级设置' });
    await expect(dialog).toBeVisible();
    return dialog;
}
