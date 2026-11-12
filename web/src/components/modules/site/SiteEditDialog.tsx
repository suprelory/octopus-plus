'use client';

import { XIcon } from 'lucide-react';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { TagInput } from './TagInput';
import { Site as SiteRecord, SitePlatform, type SiteKind } from '@/api/endpoints/site';
import type { ProxyMode } from '@/api/endpoints/proxy-pool';
import { useSiteForm } from './useSiteForm';
import { SiteBasicFields } from './SiteBasicFields';
import { SiteCheckinFields } from './SiteCheckinFields';
import { SiteAdvancedFields } from './SiteAdvancedFields';

interface SiteEditDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    site: SiteRecord | null;
    onCreated?: (site: SiteRecord) => void;
    allTags?: string[];
    kind?: SiteKind;
    linkedSites?: SiteRecord[];
}

/**
 * 站点编辑/创建弹窗。视觉风格与 Channel/Group 卡片编辑面板（MorphingDialog）保持一致：
 * bg-card / rounded-3xl / text-2xl 标题 / 自定义 close 按钮 / 整体 flex 布局并对长表单
 * 提供独立滚动区域，避免视口高度较小时底部按钮被裁切。
 */
export function SiteEditDialog({ open, onOpenChange, site, onCreated, allTags, kind = 'relay', linkedSites = [] }: SiteEditDialogProps) {
    const { siteForm, setSiteForm, handleSubmit, isPending } = useSiteForm({ site, onOpenChange, onCreated, kind });
    const isCheckin = siteForm.kind === 'checkin';

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent
                aria-describedby={undefined}
                showCloseButton={false}
                className="w-screen max-w-full md:max-w-xl bg-card text-card-foreground px-6 py-4 rounded-3xl flex flex-col gap-0 border-0 sm:max-w-xl h-[min(90dvh,52rem)] overflow-hidden"
            >
                <header className="mb-4 flex items-start justify-between gap-4 shrink-0">
                    <div className="min-w-0 flex-1">
                        <DialogTitle className="text-2xl font-bold text-card-foreground truncate">
                            {isCheckin ? (site ? '编辑签到站点' : '新增签到站点') : (site ? '编辑站点' : '新增站点')}
                        </DialogTitle>
                    </div>
                    <button
                        type="button"
                        onClick={() => onOpenChange(false)}
                        aria-label="关闭"
                        className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors shrink-0"
                    >
                        <XIcon className="size-5" />
                    </button>
                </header>

                <form className="flex flex-1 min-h-0 flex-col" onSubmit={handleSubmit}>
                    <div className="flex-1 min-h-0 space-y-5 overflow-y-auto px-1">
                        <SiteBasicFields siteForm={siteForm} setSiteForm={setSiteForm} site={site} />

                        {isCheckin ? <>
                            <div className="grid gap-2 text-sm">
                                <span className="font-medium">关联订阅站（可选）</span>
                                <Select
                                    value={siteForm.linked_site_id?.toString() ?? 'none'}
                                    onValueChange={(value) => setSiteForm((current) => {
                                        const linked = linkedSites.find((item) => item.id === Number(value));
                                        const previous = linkedSites.find((item) => item.id === current.linked_site_id);
                                        const canFillAddress = !current.base_url.trim() || current.base_url === previous?.base_url || current.base_url === site?.base_url;
                                        return { ...current, linked_site_id: linked?.id ?? null,
                                            checkin_use_subscription_token: linked?.platform === SitePlatform.Sub2API && current.checkin_use_subscription_token,
                                            ...(!current.checkin_http_enabled && linked && canFillAddress ? { platform: linked.platform, base_url: linked.base_url } : {}) };
                                    })}
                                >
                                    <SelectTrigger className="w-full rounded-xl" aria-label="关联订阅站"><SelectValue /></SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value="none">不关联</SelectItem>
                                        {linkedSites.filter((item) => item.kind !== 'checkin').map((item) => <SelectItem key={item.id} value={String(item.id)}>{item.name}</SelectItem>)}
                                        {siteForm.linked_site_id && !linkedSites.some((item) => item.id === siteForm.linked_site_id) ? <SelectItem value={String(siteForm.linked_site_id)}>订阅站 #{siteForm.linked_site_id}（已归档）</SelectItem> : null}
                                    </SelectContent>
                                </Select>
                                <p className="text-xs text-muted-foreground">平台签到使用关联订阅账号及相同的平台和地址。自定义 HTTP 签到可关联订阅账号查询余额；Sub2API 还可选择使用订阅账号的 Access Token。</p>
                            </div>
                            <SiteCheckinFields siteForm={siteForm} setSiteForm={setSiteForm} linkedSite={linkedSites.find((item) => item.id === siteForm.linked_site_id)} />
                        </> : null}

                        <label className="grid gap-2 text-sm">
                            <span className="font-medium">标签</span>
                            <TagInput
                                value={siteForm.tags}
                                onChange={(tags) =>
                                    setSiteForm((current) => ({ ...current, tags }))
                                }
                                suggestions={allTags}
                            />
                            <span className="text-xs text-muted-foreground">
                                可选：为站点打标签，便于在列表中分类筛选。
                            </span>
                        </label>

                        <ProxySelector
                            value={{ proxy_mode: siteForm.proxy_mode, proxy_config_id: siteForm.proxy_config_id }}
                            onChange={(next) => setSiteForm((current) => ({
                                ...current,
                                proxy_mode: next.proxy_mode as Exclude<ProxyMode, 'inherit'>,
                                proxy_config_id: next.proxy_config_id ?? null,
                            }))}
                        />

                        <div className="flex items-center justify-between rounded-xl border border-border/60 bg-muted/20 px-4 py-3">
                            <div>
                                <div className="text-sm font-medium">启用站点</div>
                                <div className="text-xs text-muted-foreground">
                                    {isCheckin ? '停用后暂停自动签到' : '停用后不再投影托管渠道'}
                                </div>
                            </div>
                            <Switch
                                checked={siteForm.enabled}
                                onCheckedChange={(checked) =>
                                    setSiteForm((current) => ({ ...current, enabled: checked }))
                                }
                            />
                        </div>

                        {!isCheckin ? <SiteAdvancedFields siteForm={siteForm} setSiteForm={setSiteForm} /> : null}
                    </div>

                    <footer className="mt-5 flex shrink-0 flex-col gap-3 px-1 pt-2 sm:flex-row">
                        <Button
                            type="button"
                            variant="secondary"
                            className="h-12 w-full rounded-2xl sm:flex-1"
                            onClick={() => onOpenChange(false)}
                        >
                            取消
                        </Button>
                        <Button
                            type="submit"
                            className="h-12 w-full rounded-2xl sm:flex-1"
                            disabled={isPending}
                        >
                            {isPending ? '保存中...' : site ? '保存修改' : '创建站点'}
                        </Button>
                    </footer>
                </form>
            </DialogContent>
        </Dialog>
    );
}
