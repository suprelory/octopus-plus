'use client';

import { type SiteChannelAccount, type SiteChannelGroup, useUpdateSiteSourceKeys } from '@/api/endpoints/site-channel';
import { toast } from '@/components/common/Toast';
import { Button } from '@/components/ui/button';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';
import { getErrorMessage } from '@/lib/errors';
import { useSettingStore } from '@/stores/setting';
import { Eye, EyeOff, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useCallback, useState } from 'react';
import { translateSiteMessage } from '../site/site-message';
import {
    type SiteSourceKeyFormItem,
    buildSourceKeyFormItems,
    buildSourceKeyUpdatePayload,
    hasSourceKeyChanges,
    isMaskedTokenValue,
    matchesMaskedToken,
} from './utils';

export function SourceKeysDialog({
    siteId,
    editingProjectedGroup,
    account,
    onClose,
}: {
    siteId: number;
    editingProjectedGroup: SiteChannelGroup;
    account: SiteChannelAccount;
    onClose: () => void;
}) {
    const t = useTranslations();
    const locale = useSettingStore((state) => state.locale);
    const translateSiteError = useCallback(
        (error: unknown, fallback: string) => translateSiteMessage(locale, getErrorMessage(error, fallback), t),
        [locale, t],
    );

    const [sourceKeyForm, setSourceKeyForm] = useState<SiteSourceKeyFormItem[]>(() =>
        buildSourceKeyFormItems(editingProjectedGroup),
    );

    const [visibleSourceKeyRows, setVisibleSourceKeyRows] = useState<Record<string, boolean>>({});

    const sourceKeyMutation = useUpdateSiteSourceKeys(siteId, account.account_id);

    const handleCloseProjectedKeys = () => {
        if (sourceKeyMutation.isPending) return;
        onClose();
        setSourceKeyForm([]);
        setVisibleSourceKeyRows({});
    };

    const projectedKeyRowId = (item: SiteSourceKeyFormItem, index: number) => `${item.id ?? 'new'}-${index}`;

    const handleToggleProjectedKeyVisibility = (item: SiteSourceKeyFormItem, index: number) => {
        const rowId = projectedKeyRowId(item, index);
        setVisibleSourceKeyRows((current) => ({
            ...current,
            [rowId]: !current[rowId],
        }));
    };

    const handleProjectedKeyFieldChange = (index: number, patch: Partial<SiteSourceKeyFormItem>) => {
        setSourceKeyForm((current) =>
            current.map((item, itemIndex) => (itemIndex === index ? { ...item, ...patch } : item)),
        );
    };

    const handleAddProjectedKeyRow = () => {
        setSourceKeyForm((current) => [
            ...current,
            {
                enabled: true,
                token: '',
                is_new: true,
                name: '',
                value_status: 'ready',
            },
        ]);
    };

    const handleRemoveProjectedKeyRow = (index: number) => {
        setSourceKeyForm((current) => current.filter((_, itemIndex) => itemIndex !== index));
    };

    const handleSaveProjectedKeys = () => {
        if (!editingProjectedGroup) return;
        const originalById = new Map(editingProjectedGroup.source_keys.map((key) => [key.id, key] as const));
        for (const item of sourceKeyForm) {
            if (!item.id) continue;
            const original = originalById.get(item.id);
            if (!original) continue;
            if (original.value_status !== 'masked_pending') continue;
            const trimmed = item.token.trim();
            if (trimmed === (original.token ?? '').trim()) continue;
            if (!trimmed) continue;
            if (isMaskedTokenValue(trimmed)) {
                toast.error(`Key #${item.id} 仍是脱敏值，必须填写完整 Key`);
                return;
            }
            if (!matchesMaskedToken(trimmed, original.token)) {
                toast.error(`Key #${item.id} 与已同步的脱敏值不匹配，请核对输入`);
                return;
            }
        }
        const payload = buildSourceKeyUpdatePayload(
            editingProjectedGroup.group_key,
            editingProjectedGroup.source_keys,
            sourceKeyForm,
        );
        if (!payload.keys_to_add?.length && !payload.keys_to_update?.length && !payload.keys_to_delete?.length) {
            toast.error('没有需要保存的 Key 变更');
            return;
        }
        sourceKeyMutation.mutate(payload, {
            onSuccess: () => {
                toast.success(
                    `分组「${editingProjectedGroup.group_name || editingProjectedGroup.group_key}」的站点 Key 已更新`,
                );
                onClose();
                setSourceKeyForm([]);
                setVisibleSourceKeyRows({});
            },
            onError: (error) => {
                toast.error(translateSiteError(error, '更新站点 Key 失败'));
            },
        });
    };

    return (
        <Dialog open={!!editingProjectedGroup} onOpenChange={(open) => !open && handleCloseProjectedKeys()}>
            <DialogContent className="flex h-[min(85vh,42rem)] max-w-3xl flex-col overflow-hidden rounded-3xl border-border/70 p-0 sm:max-w-3xl">
                <DialogHeader className="shrink-0 border-b border-border/60 px-6 py-4">
                    <DialogTitle className="text-lg font-semibold">管理站点 Key</DialogTitle>
                    <DialogDescription>
                        分组 {editingProjectedGroup?.group_name || editingProjectedGroup?.group_key || '-'} 的站点 Key
                        真源会在保存后更新，并重新投影到所有托管渠道。
                    </DialogDescription>
                </DialogHeader>

                <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-hidden px-6 py-4">
                    <div className="rounded-2xl border border-border/70 bg-muted/30 px-3 py-2 text-xs text-muted-foreground shrink-0">
                        投影渠道：{editingProjectedGroup?.projected_channel_ids.join(', ') || '-'}
                    </div>

                    <div className="min-h-0 flex-1 space-y-3 overflow-y-auto pr-1">
                        {sourceKeyForm.map((item, index) => (
                            <div
                                key={projectedKeyRowId(item, index)}
                                className="rounded-2xl border border-border/70 bg-background/80 p-3"
                            >
                                {(() => {
                                    const rowId = projectedKeyRowId(item, index);
                                    const isVisible = item.is_new || Boolean(visibleSourceKeyRows[rowId]);

                                    return (
                                        <>
                                            <div className="flex items-center justify-between gap-2">
                                                <div className="text-xs text-muted-foreground">
                                                    {item.id ? `站点 Key #${item.id}` : '新站点 Key'}
                                                    {item.value_status === 'masked_pending' ? ' · 待补全' : ''}
                                                </div>
                                                <Button
                                                    type="button"
                                                    variant="ghost"
                                                    size="sm"
                                                    className="rounded-xl"
                                                    onClick={() => handleRemoveProjectedKeyRow(index)}
                                                    disabled={sourceKeyMutation.isPending}
                                                >
                                                    删除
                                                </Button>
                                            </div>
                                            <div className="mt-3 grid gap-3 md:grid-cols-[auto,1fr,12rem]">
                                                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                                                    <input
                                                        type="checkbox"
                                                        checked={item.enabled}
                                                        disabled={sourceKeyMutation.isPending}
                                                        onChange={(event) =>
                                                            handleProjectedKeyFieldChange(index, {
                                                                enabled: event.target.checked,
                                                            })
                                                        }
                                                        className="size-4 rounded border-border bg-background align-middle accent-primary"
                                                    />
                                                    启用
                                                </label>
                                                <label className="grid gap-1.5 text-xs text-muted-foreground">
                                                    Key
                                                    <div className="flex items-center gap-2">
                                                        <Input
                                                            type={isVisible ? 'text' : 'password'}
                                                            value={item.token}
                                                            onChange={(event) =>
                                                                handleProjectedKeyFieldChange(index, {
                                                                    token: event.target.value,
                                                                })
                                                            }
                                                            placeholder={
                                                                item.id
                                                                    ? '点击眼睛查看或直接修改完整 Key'
                                                                    : '输入新的站点 Key'
                                                            }
                                                            disabled={sourceKeyMutation.isPending}
                                                            className="h-10 rounded-2xl"
                                                        />
                                                        <Button
                                                            type="button"
                                                            variant="outline"
                                                            size="icon"
                                                            className="size-10 rounded-2xl shrink-0"
                                                            onClick={() =>
                                                                handleToggleProjectedKeyVisibility(item, index)
                                                            }
                                                            disabled={sourceKeyMutation.isPending}
                                                            aria-label={isVisible ? '隐藏完整 Key' : '显示完整 Key'}
                                                            title={isVisible ? '隐藏完整 Key' : '显示完整 Key'}
                                                        >
                                                            {isVisible ? (
                                                                <EyeOff className="size-4" />
                                                            ) : (
                                                                <Eye className="size-4" />
                                                            )}
                                                        </Button>
                                                    </div>
                                                    {!isVisible && item.token_masked ? (
                                                        <span className="text-[11px] text-muted-foreground">
                                                            当前值：{item.token_masked}
                                                        </span>
                                                    ) : null}
                                                </label>
                                                <label className="grid gap-1.5 text-xs text-muted-foreground">
                                                    名称
                                                    <Input
                                                        value={item.name}
                                                        onChange={(event) =>
                                                            handleProjectedKeyFieldChange(index, {
                                                                name: event.target.value,
                                                            })
                                                        }
                                                        placeholder="Key 名称"
                                                        disabled={sourceKeyMutation.isPending}
                                                        className="h-10 rounded-2xl"
                                                    />
                                                </label>
                                            </div>
                                            {item.last_sync_at ? (
                                                <div className="mt-2 text-[11px] text-muted-foreground">
                                                    上次同步：{new Date(item.last_sync_at).toLocaleString()}
                                                </div>
                                            ) : null}
                                        </>
                                    );
                                })()}
                            </div>
                        ))}
                    </div>

                    <Button
                        type="button"
                        variant="outline"
                        className="rounded-2xl shrink-0"
                        onClick={handleAddProjectedKeyRow}
                        disabled={sourceKeyMutation.isPending}
                    >
                        新增 Key
                    </Button>
                </div>

                <DialogFooter className="shrink-0 border-t border-border/60 px-6 py-4">
                    <Button
                        type="button"
                        variant="outline"
                        className="rounded-2xl"
                        onClick={handleCloseProjectedKeys}
                        disabled={sourceKeyMutation.isPending}
                    >
                        取消
                    </Button>
                    <Button
                        type="button"
                        className="rounded-2xl"
                        onClick={handleSaveProjectedKeys}
                        disabled={
                            sourceKeyMutation.isPending ||
                            !editingProjectedGroup ||
                            !hasSourceKeyChanges(editingProjectedGroup.source_keys, sourceKeyForm)
                        }
                    >
                        <RefreshCw className={cn('size-4', sourceKeyMutation.isPending && 'animate-spin')} />
                        {sourceKeyMutation.isPending ? '保存中...' : '保存站点 Key'}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
