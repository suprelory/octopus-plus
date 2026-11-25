'use client';

import {
    MorphingDialog,
    MorphingDialogTrigger,
    MorphingDialogContainer,
    MorphingDialogContent,
} from '@/components/ui/morphing-dialog';
import { ArrowUpRight, KeyRound, Layers3, Network } from 'lucide-react';
import { type StatsMetricsFormatted } from '@/api/endpoints/stats';
import { ChannelType, type Channel, useEnableChannel } from '@/api/endpoints/channel';
import { CardContent } from './CardContent';
import { useTranslations } from 'next-intl';
import { Switch } from '@/components/ui/switch';
import { toast } from '@/components/common/Toast';
import { cn } from '@/lib/utils';

const TYPE_LABELS: Record<ChannelType, string> = {
    [ChannelType.OpenAIChat]: 'OpenAI Chat',
    [ChannelType.OpenAIResponse]: 'OpenAI Responses',
    [ChannelType.Anthropic]: 'Anthropic',
    [ChannelType.Gemini]: 'Gemini',
    [ChannelType.OpenAIEmbedding]: 'OpenAI Embeddings',
};

export function Card({ channel, stats, layout = 'grid' }: {
    channel: Channel;
    stats: StatsMetricsFormatted;
    layout?: 'grid' | 'list';
}) {
    const t = useTranslations('channel.card');
    const tMetrics = useTranslations('channel.detail.metrics');
    const enableChannel = useEnableChannel();
    const models = [...new Set(`${channel.model},${channel.custom_model}`.split(',').map(value => value.trim()).filter(Boolean))];
    const enabledKeyCount = channel.keys.filter(key => key.enabled).length;
    const successRate = stats.request_count.raw > 0 ? `${(stats.request_success.raw / stats.request_count.raw * 100).toFixed(1)}%` : '—';

    const handleEnableChange = (enabled: boolean) => {
        enableChannel.mutate({ id: channel.id, enabled }, {
            onSuccess: () => toast.success(t(enabled ? 'toast.enabled' : 'toast.disabled')),
            onError: (error) => toast.error(error.message),
        });
    };

    return (
        <MorphingDialog>
            <MorphingDialogTrigger aria-label={t('openDetails', { name: channel.name })}
                className="group h-full w-full rounded-2xl outline-none focus-visible:ring-2 focus-visible:ring-ring">
                <article className={cn(
                    'flex h-full flex-col gap-4 rounded-2xl border border-border/70 bg-card p-5 text-left shadow-sm transition-colors hover:border-primary/35',
                    layout === 'list' && 'md:grid md:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)] md:items-center md:gap-x-6',
                )}>
                    <div className="min-w-0 space-y-4">
                        <header className="flex items-center gap-3">
                            <span className={cn('flex size-10 shrink-0 items-center justify-center rounded-xl', channel.enabled ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground')}>
                                <Network aria-hidden className="size-5" />
                            </span>
                            <div className="min-w-0 flex-1">
                                <h3 className="truncate text-base font-semibold tracking-tight" title={channel.name}>{channel.name}</h3>
                                <p className="mt-1 truncate text-xs text-muted-foreground">{TYPE_LABELS[channel.type] ?? t('customType')} <span className="mx-1 text-border">/</span> #{channel.id}</p>
                            </div>
                            <Switch checked={channel.enabled} onCheckedChange={handleEnableChange}
                                aria-label={t('toggle', { name: channel.name })}
                                disabled={enableChannel.isPending || channel.managed}
                                onClick={(event) => event.stopPropagation()}
                                onKeyDown={(event) => event.stopPropagation()} />
                        </header>
                        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-muted-foreground">
                            <span className={cn('inline-flex items-center gap-1.5 rounded-full px-2 py-1 text-[11px] font-medium', channel.enabled ? 'bg-primary/8 text-primary' : 'bg-muted text-muted-foreground')}>
                                <span className={cn('size-1.5 rounded-full', channel.enabled ? 'bg-primary' : 'bg-muted-foreground/50')} />
                                {t(channel.enabled ? 'enabled' : 'disabled')}
                            </span>
                            <span className="inline-flex items-center gap-1" title={models.join(', ')}><Layers3 aria-hidden className="size-3.5" />{t('models', { count: models.length })}</span>
                            <span className="inline-flex items-center gap-1"><KeyRound aria-hidden className="size-3.5" />{t('keys', { enabled: enabledKeyCount, total: channel.keys.length })}</span>
                            {channel.managed && <span className="text-amber-700 dark:text-amber-300">{t('migrationPending')}</span>}
                        </div>
                    </div>

                    <dl className="grid grid-cols-2 gap-3 rounded-xl bg-muted/35 p-3.5">
                        <div className="min-w-0">
                            <dt className="text-xs text-muted-foreground">{t('requestCount')}</dt>
                            <dd className="mt-1.5 text-xl font-semibold tracking-tight tabular-nums">
                                {stats.request_count.formatted.value}<span className="ml-1 text-xs font-normal text-muted-foreground">{stats.request_count.formatted.unit}</span>
                            </dd>
                            <p className="mt-1 text-[11px] text-muted-foreground">{t('successRate')} <span className="font-medium text-foreground/80">{successRate}</span></p>
                        </div>
                        <div className="min-w-0 border-l border-border/60 pl-3.5">
                            <dt className="text-xs text-muted-foreground">{t('totalCost')}</dt>
                            <dd className="mt-1.5 text-xl font-semibold tracking-tight tabular-nums">
                                <span className="mr-0.5 text-sm font-normal text-muted-foreground">$</span>{stats.total_cost.formatted.value}
                                <span className="ml-0.5 text-xs font-normal text-muted-foreground">{stats.total_cost.formatted.unit.replace('$', '')}</span>
                            </dd>
                            <p className="mt-1 text-[11px] text-muted-foreground">{t('lifetime')}</p>
                        </div>
                    </dl>

                    <footer className={cn('mt-auto flex items-center justify-between gap-2 border-t border-border/60 pt-3 text-[11px] text-muted-foreground', layout === 'list' && 'md:col-span-2')}>
                        <div className="flex flex-wrap gap-x-3 gap-y-1 tabular-nums">
                            <span>{tMetrics('successRequests')} <span className="text-foreground/80">{stats.request_success.formatted.value}{stats.request_success.formatted.unit}</span></span>
                            <span>{tMetrics('failedRequests')} <span className={stats.request_failed.raw > 0 ? 'text-destructive' : 'text-foreground/80'}>{stats.request_failed.formatted.value}{stats.request_failed.formatted.unit}</span></span>
                        </div>
                        <span className="inline-flex shrink-0 items-center gap-1 font-medium transition-colors group-hover:text-primary">{t('details')}<ArrowUpRight aria-hidden className="size-3.5" /></span>
                    </footer>
                </article>
            </MorphingDialogTrigger>
            <MorphingDialogContainer>
                <MorphingDialogContent className="w-full md:max-w-xl bg-card text-card-foreground px-4 py-2 rounded-3xl max-h-[90vh] overflow-y-auto">
                    <CardContent channel={channel} stats={stats} />
                </MorphingDialogContent>
            </MorphingDialogContainer>
        </MorphingDialog>
    );
}
