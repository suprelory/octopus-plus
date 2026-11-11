import { Site as SiteRecord, SitePlatform, type CustomHeader, type SiteRouteBaseURL, type SiteCheckinMode, type SiteKind } from '@/api/endpoints/site';
import type { ProxyMode } from '@/api/endpoints/proxy-pool';
import type { Dispatch, SetStateAction } from 'react';
import type { SiteCheckinStatusConfig } from '@/api/endpoints/site-types';

export type SiteFormState = {
    name: string;
    kind: SiteKind;
    linked_site_id: number | null;
    platform: SitePlatform | '';
    base_url: string;
    enabled: boolean;
    proxy_mode: Exclude<ProxyMode, 'inherit'>;
    proxy_config_id: number | null;
    external_checkin_url: string;
    checkin_mode: SiteCheckinMode;
    checkin_http_enabled: boolean;
    checkin_http_method: 'GET' | 'POST';
    checkin_http_path: string;
    checkin_http_body: string;
    checkin_http_headers: CustomHeader[];
    checkin_reward_extractor: string;
    checkin_status: SiteCheckinStatusConfig | null;
    checkin_timezone: string;
    checkin_window_start: string;
    checkin_window_end: string;
    is_pinned: boolean;
    sort_order: number;
    global_weight: number;
    custom_header: CustomHeader[];
    route_base_urls: SiteRouteBaseURL[];
    tags: string[];
    default_route_type: string;
};

export const AUTO_DETECT_VALUE = '__auto__';

export const ROUTE_BASE_URL_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
    { value: 'openai_chat', label: 'OpenAI Chat' },
    { value: 'openai_response', label: 'OpenAI Responses' },
    { value: 'anthropic', label: 'Anthropic Messages' },
    { value: 'gemini', label: 'Gemini' },
    { value: 'openai_embedding', label: 'OpenAI Embedding' },
];

export const DEFAULT_ROUTE_TYPE_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
    { value: 'openai_chat', label: 'OpenAI Chat' },
    { value: 'anthropic', label: 'Anthropic' },
    { value: 'gemini', label: 'Gemini' },
];

export const PLATFORM_LABELS: Record<SitePlatform, string> = {
    [SitePlatform.API]: 'API 直连',
    [SitePlatform.NewAPI]: 'New API',
    [SitePlatform.AnyRouter]: 'AnyRouter',
    [SitePlatform.OneAPI]: 'One API',
    [SitePlatform.OneHub]: 'One Hub',
    [SitePlatform.DoneHub]: 'Done Hub',
    [SitePlatform.Sub2API]: 'Sub2API',
};

export function createEmptySiteForm(kind: SiteKind = 'relay'): SiteFormState {
    return {
        name: '',
        kind,
        linked_site_id: null,
        platform: '',
        base_url: '',
        enabled: true,
        proxy_mode: 'direct',
        proxy_config_id: null,
        external_checkin_url: '',
        checkin_mode: kind === 'checkin' ? 'auto' : 'disabled',
        checkin_http_enabled: false,
        checkin_http_method: 'POST',
        checkin_http_path: '',
        checkin_http_body: '',
        checkin_http_headers: [],
        checkin_reward_extractor: '',
        checkin_status: null,
        checkin_timezone: 'Asia/Shanghai',
        checkin_window_start: '00:00',
        checkin_window_end: '23:59',
        is_pinned: false,
        sort_order: 0,
        global_weight: 1,
        custom_header: [{ header_key: '', header_value: '' }],
        route_base_urls: [],
        tags: [],
        default_route_type: 'openai_chat',
    };
}

export function createSiteForm(site: SiteRecord): SiteFormState {
    return {
        name: site.name,
        kind: site.kind ?? 'relay',
        linked_site_id: site.linked_site_id ?? null,
        platform: site.platform,
        base_url: site.base_url,
        enabled: site.enabled,
        proxy_mode: site.proxy_mode ?? 'direct',
        proxy_config_id: site.proxy_config_id ?? null,
        external_checkin_url: site.external_checkin_url ?? '',
        checkin_mode: site.checkin_mode ?? 'auto',
        checkin_http_enabled: site.checkin_http_enabled ?? false,
        checkin_http_method: site.checkin_http_method ?? 'POST',
        checkin_http_path: site.checkin_http_path ?? '',
        checkin_http_body: site.checkin_http_body ?? '',
        checkin_http_headers: (site.checkin_http_headers ?? []).map((item) => ({ ...item })),
        checkin_reward_extractor: site.checkin_reward_extractor ?? '',
        checkin_status: site.checkin_status ? structuredClone(site.checkin_status) : null,
        checkin_timezone: site.checkin_timezone || 'Asia/Shanghai',
        checkin_window_start: site.checkin_window_start || '00:00',
        checkin_window_end: site.checkin_window_end || '23:59',
        is_pinned: site.is_pinned,
        sort_order: site.sort_order,
        global_weight: site.global_weight,
        custom_header: site.custom_header.length > 0
            ? site.custom_header.map((item) => ({ ...item }))
            : [{ header_key: '', header_value: '' }],
        route_base_urls: (site.route_base_urls ?? []).map((item) => ({ ...item })),
        tags: [...(site.tags ?? [])],
        default_route_type: site.default_route_type || 'openai_chat',
    };
}

export function normalizeSiteRecord(site: SiteRecord): SiteRecord {
    return {
        ...site,
        kind: site.kind ?? 'relay',
        linked_site_id: site.linked_site_id ?? null,
        custom_header: site.custom_header ?? [],
        route_base_urls: site.route_base_urls ?? [],
        tags: site.tags ?? [],
        proxy_mode: site.proxy_mode ?? 'direct',
        proxy_config_id: site.proxy_config_id ?? null,
        external_checkin_url: site.external_checkin_url ?? null,
        checkin_mode: site.checkin_mode ?? 'auto',
        checkin_http_enabled: site.checkin_http_enabled ?? false,
        checkin_http_method: site.checkin_http_method ?? 'POST',
        checkin_http_path: site.checkin_http_path ?? '',
        checkin_http_body: site.checkin_http_body ?? '',
        checkin_http_headers: site.checkin_http_headers ?? [],
        checkin_reward_extractor: site.checkin_reward_extractor ?? '',
        checkin_timezone: site.checkin_timezone || 'Asia/Shanghai',
        checkin_window_start: site.checkin_window_start || '00:00',
        checkin_window_end: site.checkin_window_end || '23:59',
        is_pinned: site.is_pinned ?? false,
        sort_order: typeof site.sort_order === 'number' ? site.sort_order : 0,
        global_weight:
            typeof site.global_weight === 'number' && site.global_weight > 0
                ? site.global_weight
                : 1,
        accounts: (site.accounts ?? []).map((account) => ({
            ...account,
            proxy_mode: account.proxy_mode ?? 'inherit',
            proxy_config_id: account.proxy_config_id ?? null,
        })),
    };
}

export function trimHeaders(items: CustomHeader[]) {
    return items
        .map((item) => ({
            header_key: item.header_key.trim(),
            header_value: item.header_value.trim(),
        }))
        .filter((item) => item.header_key || item.header_value);
}

export function trimRouteBaseURLs(items: SiteRouteBaseURL[]) {
    return items
        .map((item) => ({
            route_type: item.route_type.trim(),
            base_url: item.base_url.trim().replace(/\/+$/, ''),
        }))
        .filter((item) => item.route_type || item.base_url);
}

export function getErrorMessage(error: unknown) {
    if (error instanceof Error) return error.message;
    if (typeof error === 'object' && error !== null && 'message' in error) {
        const message = (error as { message?: unknown }).message;
        if (typeof message === 'string') return message;
    }
    return '操作失败';
}

export type SiteFormFieldsProps = {
    siteForm: SiteFormState;
    setSiteForm: Dispatch<SetStateAction<SiteFormState>>;
};
