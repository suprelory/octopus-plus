'use client';

import { type SiteChannelAccount, type SiteChannelGroup, useCreateSiteChannelKey } from '@/api/endpoints/site-channel';
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
import { useSettingStore } from '@/stores/setting';
import { RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useCallback, useState } from 'react';
import { translateSiteMessage } from '../site/site-message';
import { getErrorMessage } from '@/lib/errors';

export function CreateSiteKeyDialog({
    creatingGroup,
    account,
    createKeyMutation,
    onClose,
}: {
    creatingGroup: SiteChannelGroup;
    account: SiteChannelAccount;
    createKeyMutation: ReturnType<typeof useCreateSiteChannelKey>;
    onClose: () => void;
}) {
    const t = useTranslations();
    const locale = useSettingStore((state) => state.locale);
    const translateSiteError = useCallback(
        (error: unknown, fallback: string) => translateSiteMessage(locale, getErrorMessage(error, fallback), t),
        [locale, t],
    );

    const [quickCreateName, setQuickCreateName] = useState('');

    const handleCloseCreateKey = () => {
        if (createKeyMutation.isPending) return;
        onClose();
        setQuickCreateName('');
    };

    const handleCreateKey = () => {
        if (!creatingGroup) return;

        createKeyMutation.mutate(
            {
                group_key: creatingGroup.group_key,
                name: quickCreateName.trim() || undefined,
            },
            {
                onSuccess: () => {
                    toast.success(
                        `分组「${creatingGroup.group_name || creatingGroup.group_key}」已创建 Key 并完成同步`,
                    );
                    onClose();
                    setQuickCreateName('');
                },
                onError: (error) => {
                    toast.error(translateSiteError(error, '快捷创建 Key 失败'));
                },
            },
        );
    };

    return (
        <Dialog open={!!creatingGroup} onOpenChange={(open) => !open && handleCloseCreateKey()}>
            <DialogContent className="rounded-3xl sm:max-w-md">
                <DialogHeader>
                    <DialogTitle className="text-lg font-semibold">快捷创建站点 Key</DialogTitle>
                    <DialogDescription>
                        为分组 {creatingGroup?.group_name || creatingGroup?.group_key || '-'} 在账号{' '}
                        {account.account_name} 下创建新 Key，并在创建后立即同步当前卡片。
                    </DialogDescription>
                </DialogHeader>

                <div className="space-y-3">
                    <div className="rounded-2xl border border-border/70 bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
                        分组 Key：<span className="font-medium text-foreground">{creatingGroup?.group_key || '-'}</span>
                    </div>

                    <label className="grid gap-1.5 text-xs text-muted-foreground">
                        Key 名称（可选）
                        <Input
                            value={quickCreateName}
                            onChange={(event) => setQuickCreateName(event.target.value)}
                            placeholder="留空时自动生成"
                            disabled={createKeyMutation.isPending}
                            className="h-10 rounded-2xl"
                        />
                    </label>
                </div>

                <DialogFooter>
                    <Button
                        type="button"
                        variant="outline"
                        className="rounded-2xl"
                        onClick={handleCloseCreateKey}
                        disabled={createKeyMutation.isPending}
                    >
                        取消
                    </Button>
                    <Button
                        type="button"
                        className="rounded-2xl"
                        onClick={handleCreateKey}
                        disabled={createKeyMutation.isPending || !creatingGroup}
                    >
                        <RefreshCw className={cn('size-4', createKeyMutation.isPending && 'animate-spin')} />
                        {createKeyMutation.isPending ? '创建并同步中...' : '创建并同步 Key'}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
