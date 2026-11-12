'use client';

import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { Site as SiteRecord, SitePlatform } from '@/api/endpoints/site';
import { type SiteFormFieldsProps, AUTO_DETECT_VALUE, DEFAULT_ROUTE_TYPE_OPTIONS, PLATFORM_LABELS } from './site-form';

export function SiteBasicFields({ siteForm, setSiteForm, site }: SiteFormFieldsProps & { site: SiteRecord | null }) {
    return (
        <>
            <div className="grid gap-4 md:grid-cols-2">
                <label className="grid gap-2 text-sm">
                    <span className="font-medium">站点名称</span>
                    <Input
                        value={siteForm.name}
                        onChange={(event) =>
                            setSiteForm((current) => ({
                                ...current,
                                name: event.target.value,
                            }))
                        }
                        placeholder="例如：主站 OneAPI"
                        className="rounded-xl"
                    />
                </label>

                <label className="grid gap-2 text-sm">
                    <span className="font-medium">平台类型</span>
                    <Select
                        value={siteForm.platform || AUTO_DETECT_VALUE}
                        onValueChange={(value) =>
                            setSiteForm((current) => ({
                                ...current,
                                platform:
                                    value === AUTO_DETECT_VALUE
                                        ? ''
                                        : (value as SitePlatform),
                                checkin_use_subscription_token: value === SitePlatform.Sub2API && current.checkin_use_subscription_token,
                            }))
                        }
                    >
                        <SelectTrigger className="w-full rounded-xl">
                            <SelectValue placeholder="自动检测" />
                        </SelectTrigger>
                        <SelectContent className="rounded-xl">
                            {!site && (
                                <SelectItem className="rounded-xl" value={AUTO_DETECT_VALUE}>自动检测</SelectItem>
                            )}
                            {Object.entries(PLATFORM_LABELS).map(([value, label]) => (
                                <SelectItem className="rounded-xl" key={value} value={value}>
                                    {label}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </label>
            </div>

            <label className="grid gap-2 text-sm">
                <span className="font-medium">{siteForm.kind === 'checkin' ? '签到站点地址' : '站点地址'}</span>
                <Input
                    value={siteForm.base_url}
                    onChange={(event) =>
                        setSiteForm((current) => ({
                            ...current,
                            base_url: event.target.value,
                        }))
                    }
                    placeholder="https://example.com"
                    className="rounded-xl"
                />
            </label>

            {siteForm.kind !== 'checkin' && siteForm.platform === SitePlatform.API && (
                <div className="grid gap-2 text-sm">
                    <div className="flex items-center gap-1.5">
                        <span className="font-medium">默认协议</span>
                        <Tooltip>
                            <TooltipTrigger asChild>
                                <button
                                    type="button"
                                    className="inline-flex items-center justify-center rounded-full text-muted-foreground hover:text-foreground transition-colors"
                                    tabIndex={-1}
                                >
                                    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="currentColor" className="size-3.5">
                                        <path fillRule="evenodd" d="M15 8A7 7 0 1 1 1 8a7 7 0 0 1 14 0ZM9 5a1 1 0 1 1-2 0 1 1 0 0 1 2 0ZM6.75 8a.75.75 0 0 0 0 1.5h.75v1.75a.75.75 0 0 0 1.5 0v-2.5A.75.75 0 0 0 8.25 8h-1.5Z" clipRule="evenodd" />
                                    </svg>
                                </button>
                            </TooltipTrigger>
                            <TooltipContent className="max-w-xs">
                                决定获取模型列表的请求格式，以及未手动指定路由类型的模型的默认端点格式
                            </TooltipContent>
                        </Tooltip>
                    </div>
                    <Select
                        value={siteForm.default_route_type}
                        onValueChange={(value) =>
                            setSiteForm((current) => ({
                                ...current,
                                default_route_type: value,
                            }))
                        }
                    >
                        <SelectTrigger className="w-full rounded-xl">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="rounded-xl">
                            {DEFAULT_ROUTE_TYPE_OPTIONS.map((option) => (
                                <SelectItem className="rounded-xl" key={option.value} value={option.value}>
                                    {option.label}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </div>
            )}
        </>
    );
}
