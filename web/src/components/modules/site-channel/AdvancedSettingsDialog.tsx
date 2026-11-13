'use client';

import { type SiteChannelGroup, useUpdateSiteProjectedChannelSettings } from '@/api/endpoints/site-channel';
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
import { cn } from '@/lib/utils';
import { useSettingStore } from '@/stores/setting';
import { useTranslations } from 'next-intl';
import { useCallback, useState } from 'react';
import { translateSiteMessage } from '../site/site-message';
import { getErrorMessage } from '@/lib/errors';
import { routeTypeLabel } from './utils';

export function AdvancedSettingsDialog({
    editingAdvancedGroup,
    advancedMutation,
    onClose,
}: {
    editingAdvancedGroup: SiteChannelGroup;
    advancedMutation: ReturnType<typeof useUpdateSiteProjectedChannelSettings>;
    onClose: () => void;
}) {
    const t = useTranslations();
    const locale = useSettingStore((state) => state.locale);
    const translateSiteError = useCallback(
        (error: unknown, fallback: string) => translateSiteMessage(locale, getErrorMessage(error, fallback), t),
        [locale, t],
    );

    const [selectedAdvancedChannelId, setSelectedAdvancedChannelId] = useState<number | null>(
        editingAdvancedGroup.projected_channels[0]?.channel_id ?? null,
    );

    const [advancedForm, setAdvancedForm] = useState<Record<number, { param_override: string }>>(() =>
        Object.fromEntries(
            editingAdvancedGroup.projected_channels.map((channel) => [
                channel.channel_id,
                { param_override: channel.param_override ?? '' },
            ]),
        ),
    );

    const handleCloseAdvancedSettings = () => {
        if (advancedMutation.isPending) return;
        onClose();
        setSelectedAdvancedChannelId(null);
        setAdvancedForm({});
    };

    const handleAdvancedParamChange = (channelId: number, value: string) => {
        setAdvancedForm((current) => ({
            ...current,
            [channelId]: { ...(current[channelId] ?? { param_override: '' }), param_override: value },
        }));
    };

    const validateAdvancedSettings = () => {
        for (const item of Object.values(advancedForm)) {
            const value = item.param_override.trim();
            if (!value) continue;
            try {
                const parsed = JSON.parse(value) as unknown;
                // The API accepts both merge objects and operation arrays;
                // it validates the individual operations when saving.
                if (!parsed || typeof parsed !== 'object') {
                    return false;
                }
            } catch {
                return false;
            }
        }
        return true;
    };

    const selectedAdvancedChannel =
        editingAdvancedGroup?.projected_channels.find((channel) => channel.channel_id === selectedAdvancedChannelId) ??
        editingAdvancedGroup?.projected_channels[0] ??
        null;

    const handleSaveAdvancedSettings = () => {
        if (!editingAdvancedGroup) return;
        if (!validateAdvancedSettings()) {
            toast.error(t('siteChannel.advanced.invalidParamOverride'));
            return;
        }
        const payload = editingAdvancedGroup.projected_channels.map((channel) => ({
            channel_id: channel.channel_id,
            auto_group: channel.auto_group,
            param_override: advancedForm[channel.channel_id]?.param_override?.trim() ?? '',
        }));
        advancedMutation.mutate(payload, {
            onSuccess: () => {
                toast.success(t('siteChannel.advanced.saved'));
                onClose();
            },
            onError: (error) => {
                toast.error(translateSiteError(error, t('siteChannel.advanced.saveFailed')));
            },
        });
    };

    return (
        <Dialog open={!!editingAdvancedGroup} onOpenChange={(open) => !open && handleCloseAdvancedSettings()}>
            <DialogContent className="max-h-[85vh] overflow-y-auto rounded-3xl sm:max-w-4xl">
                <DialogHeader>
                    <DialogTitle className="text-lg font-semibold">{t('siteChannel.advanced.title')}</DialogTitle>
                    <DialogDescription>
                        {t('siteChannel.advanced.description', {
                            group: editingAdvancedGroup?.group_name || editingAdvancedGroup?.group_key || '-',
                        })}
                    </DialogDescription>
                </DialogHeader>
                <div className="space-y-4">
                    <div className="grid gap-4 lg:grid-cols-[16rem_1fr]">
                        <div className="space-y-2">
                            <div className="px-1 text-xs font-medium text-muted-foreground">
                                {t('siteChannel.advanced.channelList')}
                            </div>
                            <div className="space-y-2">
                                {editingAdvancedGroup?.projected_channels.map((channel) => {
                                    const active = selectedAdvancedChannel?.channel_id === channel.channel_id;
                                    return (
                                        <button
                                            key={channel.channel_id}
                                            type="button"
                                            onClick={() => setSelectedAdvancedChannelId(channel.channel_id)}
                                            className={cn(
                                                'flex w-full items-center justify-between gap-3 rounded-2xl border px-3 py-3 text-left transition',
                                                active
                                                    ? 'border-primary/30 bg-primary/10 text-foreground'
                                                    : 'border-border/60 bg-muted/10 hover:bg-muted/40',
                                            )}
                                        >
                                            <div className="min-w-0">
                                                <div className="truncate text-sm font-medium">
                                                    {routeTypeLabel(channel.route_type)}
                                                </div>
                                                <div className="mt-0.5 truncate text-xs text-muted-foreground">
                                                    #{channel.channel_id}
                                                </div>
                                            </div>
                                        </button>
                                    );
                                })}
                            </div>
                        </div>

                        {selectedAdvancedChannel ? (
                            (() => {
                                const channel = selectedAdvancedChannel;
                                const form = advancedForm[channel.channel_id] ?? {
                                    param_override: channel.param_override ?? '',
                                };
                                return (
                                    <div className="space-y-4 rounded-2xl border border-border/60 bg-muted/10 p-4">
                                        <div className="flex flex-wrap items-start justify-between gap-3">
                                            <div className="min-w-0">
                                                <div className="text-sm font-medium text-foreground">
                                                    {routeTypeLabel(channel.route_type)}
                                                </div>
                                                <div className="mt-1 truncate text-xs text-muted-foreground">
                                                    #{channel.channel_id} · {channel.channel_name}
                                                </div>
                                            </div>
                                        </div>

                                        <div className="space-y-4">
                                            <label className="grid gap-2 text-sm">
                                                <span className="font-medium">
                                                    {t('siteChannel.advanced.paramOverride')}
                                                </span>
                                                <textarea
                                                    value={form.param_override}
                                                    onChange={(event) =>
                                                        handleAdvancedParamChange(
                                                            channel.channel_id,
                                                            event.target.value,
                                                        )
                                                    }
                                                    placeholder={t('siteChannel.advanced.paramOverridePlaceholder')}
                                                    className="min-h-40 rounded-xl border border-border bg-background px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                                                />
                                            </label>
                                        </div>
                                    </div>
                                );
                            })()
                        ) : (
                            <div className="flex min-h-48 items-center justify-center rounded-2xl border border-dashed border-border/70 bg-muted/10 text-sm text-muted-foreground">
                                {t('siteChannel.advanced.empty')}
                            </div>
                        )}
                    </div>
                </div>
                <DialogFooter>
                    <Button
                        type="button"
                        variant="outline"
                        className="rounded-xl"
                        onClick={handleCloseAdvancedSettings}
                        disabled={advancedMutation.isPending}
                    >
                        {t('siteChannel.advanced.cancel')}
                    </Button>
                    <Button
                        type="button"
                        className="rounded-xl"
                        onClick={handleSaveAdvancedSettings}
                        disabled={advancedMutation.isPending || !editingAdvancedGroup}
                    >
                        {advancedMutation.isPending ? t('siteChannel.advanced.saving') : t('siteChannel.advanced.save')}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
