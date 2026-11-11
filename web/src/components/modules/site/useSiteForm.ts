'use client';

import { useCallback, useState, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';
import { toast } from '@/components/common/Toast';
import { useSettingStore } from '@/stores/setting';
import {
    Site as SiteRecord,
    SitePlatform,
    type SiteKind,
    useCreateSite,
    useDetectSitePlatform,
    useUpdateSite,
} from '@/api/endpoints/site';
import { translateSiteMessage } from './site-message';
import {
    type SiteFormState,
    createSiteForm,
    createEmptySiteForm,
    normalizeSiteRecord,
    trimHeaders,
    trimRouteBaseURLs,
    getErrorMessage,
    PLATFORM_LABELS,
} from './site-form';

export function useSiteForm({ site, onOpenChange, onCreated, kind = 'relay' }: {
    site: SiteRecord | null;
    kind?: SiteKind;
    onOpenChange: (open: boolean) => void;
    onCreated?: (site: SiteRecord) => void;
}) {
    const t = useTranslations();
    const tProxy = useTranslations('proxyPool');
    const locale = useSettingStore((state) => state.locale);
    const createSite = useCreateSite();
    const updateSite = useUpdateSite();
    const detectPlatform = useDetectSitePlatform();
    const [siteForm, setSiteForm] = useState<SiteFormState>(() =>
        site ? createSiteForm(site) : createEmptySiteForm(kind),
    );

    const handleSubmit = useCallback(
        async (event: FormEvent<HTMLFormElement>) => {
            event.preventDefault();

            if (!siteForm.name.trim()) {
                toast.error('请输入站点名称');
                return;
            }
            if (!siteForm.base_url.trim()) {
                toast.error('请输入站点地址');
                return;
            }
            if (!siteForm.checkin_timezone.trim()) {
                toast.error('请输入签到时区');
                return;
            }
            if (!siteForm.checkin_window_start || !siteForm.checkin_window_end) {
                toast.error('请输入完整的签到时间窗口');
                return;
            }
            if (siteForm.checkin_window_end < siteForm.checkin_window_start) {
                toast.error('签到结束时间不能早于开始时间');
                return;
            }

            let platform = siteForm.platform;
            let defaultRouteType = siteForm.default_route_type;
            if (!platform && !site) {
                try {
                    const detected = await detectPlatform.mutateAsync(
                        siteForm.base_url.trim(),
                    );
                    platform = detected.platform as SitePlatform;
                    if (detected.default_route_type) {
                        defaultRouteType = detected.default_route_type;
                        setSiteForm((current) => ({
                            ...current,
                            default_route_type: detected.default_route_type!,
                        }));
                    }
                    toast.success(
                        `自动检测到平台：${PLATFORM_LABELS[platform] ?? platform}`,
                    );
                } catch {
                    toast.error('无法自动检测平台类型，请手动选择');
                    return;
                }
            }
            if (!platform) {
                toast.error('请选择平台类型');
                return;
            }

            const customHeader = trimHeaders(siteForm.custom_header);
            const invalidHeader = customHeader.find(
                (item) => !item.header_key || !item.header_value,
            );
            if (invalidHeader) {
                toast.error('自定义 Header 的键和值都不能为空');
                return;
            }

            const routeBaseURLs = trimRouteBaseURLs(siteForm.route_base_urls);
            const invalidRouteBaseURL = routeBaseURLs.find(
                (item) => !item.route_type || !item.base_url,
            );
            if (invalidRouteBaseURL) {
                toast.error('协议路径覆盖的类型和地址都不能为空');
                return;
            }
            const routeTypeSet = new Set<string>();
            const duplicateRoute = routeBaseURLs.find((item) => {
                if (routeTypeSet.has(item.route_type)) return true;
                routeTypeSet.add(item.route_type);
                return false;
            });
            if (duplicateRoute) {
                toast.error('同一协议的路径覆盖只能配置一条');
                return;
            }

            if (siteForm.proxy_mode === 'pool' && !siteForm.proxy_config_id) {
                toast.error(tProxy('selectRequired'));
                return;
            }

            if (new TextEncoder().encode(siteForm.checkin_reward_extractor).length > 16 * 1024 || siteForm.checkin_reward_extractor.includes('\0')) {
                toast.error('奖励提取代码不能超过 16 KiB 或包含空字符');
                return;
            }
            if (siteForm.checkin_http_enabled) {
                const checkinPath = siteForm.checkin_http_path.trim();
                if (!checkinPath.startsWith('/') || checkinPath.startsWith('//') || /[\\\r\n\0#]/.test(checkinPath)) {
                    toast.error('自定义签到路径必须是以 / 开头的站内路径');
                    return;
                }
                let decodedPath: string;
                try {
                    decodedPath = decodeURIComponent(checkinPath.split('?', 1)[0]);
                } catch {
                    toast.error('自定义签到路径编码无效');
                    return;
                }
                if (decodedPath.split('/').some(segment => segment === '.' || segment === '..' || segment.includes('\\'))) {
                    toast.error('自定义签到路径不能包含路径跳转片段');
                    return;
                }
                if (siteForm.checkin_http_method === 'POST' && siteForm.checkin_http_body.trim()) {
                    try {
                        JSON.parse(siteForm.checkin_http_body);
                    } catch {
                        toast.error('自定义签到请求体必须是有效 JSON');
                        return;
                    }
                }
            }

            const payload = {
                name: siteForm.name.trim(),
                kind: siteForm.kind,
                linked_site_id: siteForm.kind === 'checkin' ? siteForm.linked_site_id : null,
                platform: platform as SitePlatform,
                base_url: siteForm.base_url.trim(),
                enabled: siteForm.enabled,
                proxy_mode: siteForm.proxy_mode,
                proxy_config_id:
                    siteForm.proxy_mode === 'pool' ? siteForm.proxy_config_id : null,
                external_checkin_url: siteForm.external_checkin_url.trim() || null,
                checkin_mode: siteForm.kind === 'checkin' ? siteForm.checkin_mode : 'disabled',
                checkin_http_enabled: siteForm.checkin_http_enabled,
                checkin_http_method: siteForm.checkin_http_method,
                checkin_http_path: siteForm.checkin_http_path.trim(),
                checkin_http_body: siteForm.checkin_http_body,
                checkin_http_headers: trimHeaders(siteForm.checkin_http_headers),
                checkin_reward_extractor: siteForm.checkin_reward_extractor.trim(),
                checkin_status: siteForm.checkin_status ? { ...siteForm.checkin_status, headers: trimHeaders(siteForm.checkin_status.headers ?? []) } : null,
                checkin_timezone: siteForm.checkin_timezone.trim(),
                checkin_window_start: siteForm.checkin_window_start,
                checkin_window_end: siteForm.checkin_window_end,
                is_pinned: siteForm.is_pinned,
                sort_order: siteForm.sort_order,
                global_weight: siteForm.global_weight,
                custom_header: customHeader,
                route_base_urls: routeBaseURLs,
                tags: siteForm.tags,
                default_route_type:
                    platform === SitePlatform.API ? defaultRouteType : undefined,
            };

            try {
                if (site) {
                    await updateSite.mutateAsync({ id: site.id, ...payload });
                    toast.success('站点已更新');
                    onOpenChange(false);
                } else {
                    const createdSite = normalizeSiteRecord(
                        await createSite.mutateAsync(payload),
                    );
                    toast.success('站点已创建');
                    onOpenChange(false);
                    onCreated?.(createdSite);
                }
            } catch (submitError) {
                toast.error(
                    translateSiteMessage(locale, getErrorMessage(submitError), t),
                );
            }
        },
        [
            siteForm,
            site,
            detectPlatform,
            tProxy,
            updateSite,
            createSite,
            onOpenChange,
            onCreated,
            locale,
            t,
        ],
    );

    const isPending = createSite.isPending || updateSite.isPending || detectPlatform.isPending;
    return { siteForm, setSiteForm, handleSubmit, isPending };
}
