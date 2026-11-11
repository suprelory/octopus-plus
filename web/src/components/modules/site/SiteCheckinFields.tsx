'use client';

import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Plus, X } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useSiteCheckinDefaults } from '@/api/endpoints/site-checkin';
import type { SiteCheckinMode } from '@/api/endpoints/site';
import type { SiteFormFieldsProps } from './site-form';
import { CheckinRewardExtractorField } from './CheckinRewardExtractorField';
import { CheckinStatusFields } from './CheckinStatusFields';

export function SiteCheckinFields({ siteForm, setSiteForm }: SiteFormFieldsProps) {
    const t = useTranslations('siteCheckinCapability');
    const { data: defaults } = useSiteCheckinDefaults();
    const platformDefaults = siteForm.platform ? defaults?.[siteForm.platform] : undefined;
    return (
        <>
            <div className="grid gap-2 rounded-xl border border-border/60 p-4 text-sm">
                <label htmlFor="site-checkin-mode" className="font-medium">{t('mode')}</label>
                <Select value={siteForm.checkin_mode} onValueChange={(value: SiteCheckinMode) => setSiteForm((current) => ({ ...current, checkin_mode: value }))}>
                    <SelectTrigger id="site-checkin-mode" aria-label={t('mode')} className="w-full rounded-xl"><SelectValue /></SelectTrigger>
                    <SelectContent>
                        <SelectItem value="auto">{t('auto')}</SelectItem>
                        <SelectItem value="enabled">{t('enabled')}</SelectItem>
                        <SelectItem value="disabled">{t('disabled')}</SelectItem>
                    </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">{t('modeHint')}</p>
                {platformDefaults ? <p className="text-xs text-muted-foreground">{t(platformDefaults.enabled ? 'defaultEnabled' : 'defaultDisabled')}</p> : null}
                {platformDefaults && !platformDefaults.has_builtin && !siteForm.checkin_http_enabled ? <p className="text-xs text-amber-600">{t('requiresCustom')}</p> : null}
                <p className="text-xs text-muted-foreground">{t('verifyHint')}</p>
            </div>

            <label className="grid gap-2 text-sm">
                <span className="font-medium">手动签到 URL</span>
                <Input
                    value={siteForm.external_checkin_url}
                    onChange={(event) =>
                        setSiteForm((current) => ({
                            ...current,
                            external_checkin_url: event.target.value,
                        }))
                    }
                    placeholder="可选：例如 https://example.com/signin"
                    className="rounded-xl"
                />
                <span className="text-xs text-muted-foreground">
                    配置后可在签到页面中一键打开此网址进行手动签到。
                </span>
            </label>

            <section className="space-y-4 rounded-xl border border-border/60 p-4">
                <div className="flex items-center justify-between gap-4">
                    <div className="min-w-0">
                        <div className="text-sm font-medium">自定义 HTTP 签到</div>
                        <p className="text-xs text-muted-foreground">使用签到账号中单独填写的 Cookie 发送请求，启用后覆盖平台内置签到端点。</p>
                    </div>
                    <Switch
                        aria-label="启用自定义 HTTP 签到"
                        checked={siteForm.checkin_http_enabled}
                        onCheckedChange={(checked) => setSiteForm((current) => ({ ...current, checkin_http_enabled: checked }))}
                    />
                </div>
                {siteForm.checkin_http_enabled ? (
                    <div className="grid gap-4">
                        <div className="grid gap-4 sm:grid-cols-[8rem_minmax(0,1fr)]">
                            <label className="grid gap-2 text-sm">
                                <span className="font-medium">请求方法</span>
                                <Select
                                    value={siteForm.checkin_http_method}
                                    onValueChange={(value: 'GET' | 'POST') => setSiteForm((current) => ({ ...current, checkin_http_method: value, checkin_http_body: value === 'GET' ? '' : current.checkin_http_body }))}
                                >
                                    <SelectTrigger aria-label="签到请求方法" className="w-full rounded-xl"><SelectValue /></SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value="POST">POST</SelectItem>
                                        <SelectItem value="GET">GET</SelectItem>
                                    </SelectContent>
                                </Select>
                            </label>
                            <label className="grid min-w-0 gap-2 text-sm">
                                <span className="font-medium">请求路径</span>
                                <Input
                                    value={siteForm.checkin_http_path}
                                    onChange={(event) => setSiteForm((current) => ({ ...current, checkin_http_path: event.target.value }))}
                                    placeholder="/api/checkin/spin"
                                    className="rounded-xl"
                                />
                            </label>
                        </div>
                        <div className="space-y-2">
                            <div className="flex items-center justify-between gap-3">
                                <span className="text-sm font-medium">签到专属 Header</span>
                                <Button type="button" variant="outline" size="sm" onClick={() => setSiteForm((current) => ({ ...current, checkin_http_headers: [...current.checkin_http_headers, { header_key: '', header_value: '' }] }))}>
                                    <Plus className="size-4" />添加
                                </Button>
                            </div>
                            {siteForm.checkin_http_headers.map((header, index) => (
                                <div key={`checkin-hdr-${index}`} className="grid min-w-0 grid-cols-[minmax(0,1fr)_minmax(0,1fr)_2rem] gap-2">
                                    <Input aria-label={`签到 Header ${index + 1} 名称`} value={header.header_key} onChange={(event) => setSiteForm((current) => ({ ...current, checkin_http_headers: current.checkin_http_headers.map((item, itemIndex) => itemIndex === index ? { ...item, header_key: event.target.value } : item) }))} placeholder="Header 名称" className="min-w-0 rounded-xl" />
                                    <Input aria-label={`签到 Header ${index + 1} 值`} value={header.header_value} onChange={(event) => setSiteForm((current) => ({ ...current, checkin_http_headers: current.checkin_http_headers.map((item, itemIndex) => itemIndex === index ? { ...item, header_value: event.target.value } : item) }))} placeholder="Header 值" className="min-w-0 rounded-xl" />
                                    <Button type="button" variant="ghost" size="icon" aria-label={`删除签到 Header ${index + 1}`} onClick={() => setSiteForm((current) => ({ ...current, checkin_http_headers: current.checkin_http_headers.filter((_, itemIndex) => itemIndex !== index) }))}>
                                        <X className="size-4" />
                                    </Button>
                                </div>
                            ))}
                        </div>
                        {siteForm.checkin_http_method === 'POST' ? (
                            <label className="grid gap-2 text-sm">
                                <span className="font-medium">JSON 请求体</span>
                                <textarea
                                    value={siteForm.checkin_http_body}
                                    onChange={(event) => setSiteForm((current) => ({ ...current, checkin_http_body: event.target.value }))}
                                    placeholder={'可选，例如：{"action":"checkin"}\n支持 {{cookie}}、{{username}} 占位符；Cookie 请求头会自动发送'}
                                    rows={4}
                                    className="w-full min-w-0 resize-y rounded-xl border border-input bg-background px-3 py-2 font-mono text-xs"
                                />
                            </label>
                        ) : null}
                        <CheckinStatusFields siteForm={siteForm} setSiteForm={setSiteForm} />
                        <CheckinRewardExtractorField siteForm={siteForm} setSiteForm={setSiteForm} />
                    </div>
                ) : null}
            </section>

            <div className="grid gap-4 rounded-xl border border-border/60 bg-muted/20 p-4 md:grid-cols-2">
                <label className="grid gap-2 text-sm md:col-span-2">
                    <span className="font-medium">自动签到时区</span>
                    <Input
                        value={siteForm.checkin_timezone}
                        onChange={(event) =>
                            setSiteForm((current) => ({
                                ...current,
                                checkin_timezone: event.target.value,
                            }))
                        }
                        list="site-checkin-timezones"
                        placeholder="Asia/Shanghai"
                        className="rounded-xl"
                    />
                    <datalist id="site-checkin-timezones">
                        <option value="Asia/Shanghai" />
                        <option value="Asia/Hong_Kong" />
                        <option value="Asia/Tokyo" />
                        <option value="Europe/London" />
                        <option value="America/New_York" />
                        <option value="UTC" />
                    </datalist>
                </label>
                <label className="grid gap-2 text-sm">
                    <span className="font-medium">开始时间</span>
                    <Input
                        type="time"
                        value={siteForm.checkin_window_start}
                        onChange={(event) =>
                            setSiteForm((current) => ({
                                ...current,
                                checkin_window_start: event.target.value,
                            }))
                        }
                        className="rounded-xl"
                    />
                </label>
                <label className="grid gap-2 text-sm">
                    <span className="font-medium">结束时间</span>
                    <Input
                        type="time"
                        value={siteForm.checkin_window_end}
                        onChange={(event) =>
                            setSiteForm((current) => ({
                                ...current,
                                checkin_window_end: event.target.value,
                            }))
                        }
                        className="rounded-xl"
                    />
                </label>
                <p className="text-xs text-muted-foreground md:col-span-2">
                    自动签到只会在该站点当地时间窗口内执行；例如仅允许 8 点后签到时，将开始时间设为 08:00。
                </p>
            </div>
        </>
    );
}
