'use client';

import { type SiteChannelGroup, type SiteModelRouteType, useAddSiteManualModels } from '@/api/endpoints/site-channel';
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { useSettingStore } from '@/stores/setting';
import { useTranslations } from 'next-intl';
import { useCallback, useState } from 'react';
import { translateSiteMessage } from '../site/site-message';
import { SITE_ROUTE_COLUMN_ORDER } from './constants';
import { getErrorMessage } from '@/lib/errors';
import { routeTypeLabel } from './utils';

export function AddManualModelsDialog({
    addingManualGroup,
    addManualModelsMutation,
    onClose,
}: {
    addingManualGroup: SiteChannelGroup;
    addManualModelsMutation: ReturnType<typeof useAddSiteManualModels>;
    onClose: () => void;
}) {
    const t = useTranslations();
    const locale = useSettingStore((state) => state.locale);
    const translateSiteError = useCallback(
        (error: unknown, fallback: string) => translateSiteMessage(locale, getErrorMessage(error, fallback), t),
        [locale, t],
    );

    const [manualModelsInput, setManualModelsInput] = useState('');

    const [manualModelRouteType, setManualModelRouteType] = useState<SiteModelRouteType>('openai_chat');

    const handleCloseAddManualModels = () => {
        if (addManualModelsMutation.isPending) return;
        onClose();
        setManualModelsInput('');
    };

    const parseManualModelNames = () =>
        Array.from(
            new Set(
                manualModelsInput
                    .split(/[\n,]+/)
                    .map((item) => item.trim())
                    .filter(Boolean),
            ),
        );

    const handleAddManualModels = () => {
        if (!addingManualGroup) return;
        const names = parseManualModelNames();
        if (names.length === 0) {
            toast.error('请填写模型名称');
            return;
        }
        const existing = new Set(addingManualGroup.models.map((model) => model.model_name));
        const duplicated = names.filter((name) => existing.has(name));
        if (duplicated.length > 0) {
            toast.error(`模型已存在：${duplicated.join(', ')}`);
            return;
        }
        addManualModelsMutation.mutate(
            {
                group_key: addingManualGroup.group_key,
                models: names.map((name) => ({ model_name: name, route_type: manualModelRouteType })),
            },
            {
                onSuccess: () => {
                    toast.success(`已添加 ${names.length} 个自定义模型`);
                    onClose();
                },
                onError: (error) => {
                    toast.error(translateSiteError(error, '添加自定义模型失败'));
                },
            },
        );
    };

    return (
        <Dialog open={!!addingManualGroup} onOpenChange={(open) => !open && handleCloseAddManualModels()}>
            <DialogContent className="max-h-[85vh] overflow-y-auto rounded-3xl sm:max-w-2xl">
                <DialogHeader>
                    <DialogTitle className="text-lg font-semibold">添加自定义模型</DialogTitle>
                    <DialogDescription>
                        批量添加到分组 {addingManualGroup?.group_name || addingManualGroup?.group_key || '-'}
                        。同组已存在的模型不能重复添加。
                    </DialogDescription>
                </DialogHeader>
                <div className="space-y-4">
                    <label className="grid gap-1.5 text-xs text-muted-foreground">
                        模型名称（支持换行或逗号分隔）
                        <textarea
                            value={manualModelsInput}
                            onChange={(event) => setManualModelsInput(event.target.value)}
                            placeholder={'gpt-4o\ngpt-4.1-mini'}
                            className="min-h-36 rounded-xl border border-border bg-background px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        />
                    </label>
                    <label className="grid gap-1.5 text-xs text-muted-foreground">
                        端点格式
                        <Select
                            value={manualModelRouteType}
                            onValueChange={(value) => setManualModelRouteType(value as SiteModelRouteType)}
                        >
                            <SelectTrigger className="h-10 rounded-xl bg-background">
                                <SelectValue />
                            </SelectTrigger>
                            <SelectContent className="rounded-xl">
                                {SITE_ROUTE_COLUMN_ORDER.map((routeType) => (
                                    <SelectItem key={routeType} value={routeType}>
                                        {routeTypeLabel(routeType)}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                    </label>
                </div>
                <DialogFooter>
                    <Button
                        type="button"
                        variant="outline"
                        className="rounded-xl"
                        onClick={handleCloseAddManualModels}
                        disabled={addManualModelsMutation.isPending}
                    >
                        取消
                    </Button>
                    <Button
                        type="button"
                        className="rounded-xl"
                        onClick={handleAddManualModels}
                        disabled={addManualModelsMutation.isPending || !addingManualGroup}
                    >
                        {addManualModelsMutation.isPending ? '添加中...' : '添加'}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
