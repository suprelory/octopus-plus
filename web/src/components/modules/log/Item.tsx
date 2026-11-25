'use client';

import { useEffect, useMemo, useState } from 'react';
import { Clock, Cpu, Gauge, Percent, Globe, Zap, ArrowDownToLine, DollarSign, ArrowRight, Pin, KeyRound, TestTube2, Brain, Type, Sigma } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { getLogDetail, type RelayLog, type RelayLogDetail } from '@/api/endpoints/log';
import { getModelIcon } from '@/lib/model-icons';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import {
    MorphingDialog,
    MorphingDialogTrigger,
    MorphingDialogContainer,
    MorphingDialogContent,
    MorphingDialogClose,
    MorphingDialogTitle,
    MorphingDialogDescription,
} from '@/components/ui/morphing-dialog';
import { toast } from '@/components/common/Toast';
import { resolveLogDisplayFields } from './display';
import { formatEndpointLabel, formatRequestTypeLabel, formatTime, formatDurationCompact, formatTPS, formatCacheHitRate, formatCompactTokenCount, resolveTokenUsageDisplay, isLogFailure } from './log-format';
import { RetryBadgeWithTooltip, WSModeBadge } from './log-status';
import { LogContentPanels } from './LogContentPanels';
import { LogTracePanels } from './LogTracePanels';
import { LogDiagnosticPanel } from './LogDiagnosticPanel';
import { LogSummary } from './LogSummary';
import { useLogFieldVisibility } from './ui-store';
export function LogCard({ log, channelNameById }: { log: RelayLog; channelNameById?: ReadonlyMap<number, string> }) {
    const t = useTranslations('log.card');
    const tOverview = useTranslations('workspace.log');
    const hasError = isLogFailure(log);
    const hasMultipleAttempts = (log.attempts?.length ?? 0) > 1;
    const [isDiagnosticExpanded, setIsDiagnosticExpanded] = useState(false);
    const [detailLog, setDetailLog] = useState<RelayLogDetail | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [detailRequestID, setDetailRequestID] = useState(0);
    const [requestJsonCollapsed, setRequestJsonCollapsed] = useState(false);
    const [responseJsonCollapsed, setResponseJsonCollapsed] = useState(false);

    const displayFields = useMemo(() => resolveLogDisplayFields(log, detailLog, channelNameById), [channelNameById, detailLog, log]);
    const visibility = useLogFieldVisibility();
    const displayActualModelName = displayFields.actualModelName;
    const displayRequestModelName = displayFields.requestModelName;
    const displayChannelName = displayFields.channelName || '-';
    const endpointLabel = displayFields.adapterType
        ? formatEndpointLabel(t, displayFields.adapterType)
        : displayFields.requestType
            ? formatRequestTypeLabel(t, displayFields.requestType)
            : formatEndpointLabel(t, displayFields.endpointType);
    const tokenUsage = useMemo(() => resolveTokenUsageDisplay({
        outputTokens: displayFields.outputTokens,
        billInputTokens: displayFields.billInputTokens,
        cacheReadTokens: displayFields.cacheReadTokens,
        cacheWriteTokens: displayFields.cacheWriteTokens,
    }), [
        displayFields.billInputTokens,
        displayFields.cacheReadTokens,
        displayFields.cacheWriteTokens,
        displayFields.outputTokens,
    ]);
    const cacheReadTokens = tokenUsage.cacheReadTokens;
    const cacheWriteTokens = tokenUsage.cacheWriteTokens;
    const totalInputTokens = tokenUsage.totalInputTokens;
    const totalTokens = tokenUsage.totalTokens;
    const { Avatar: ModelAvatar, color: brandColor } = useMemo(
        () => getModelIcon(displayActualModelName),
        [displayActualModelName],
    );
    const requestAPIKeyName = displayFields.requestAPIKeyName;
    const clientIP = displayFields.clientIP;

    const requestContent = detailLog?.request_content;
    const responseContent = detailLog?.response_content;

    useEffect(() => {
        if (detailRequestID === 0 || detailLog) return;
        let cancelled = false;
        getLogDetail(log.id)
            .then((item) => {
                if (!cancelled) setDetailLog(item);
            })
            .catch((error) => {
                if (!cancelled) {
                    toast.error(error instanceof Error ? error.message : 'Failed to load log detail');
                }
            })
            .finally(() => {
                if (!cancelled) setDetailLoading(false);
            });
        return () => {
            cancelled = true;
        };
    }, [detailLog, detailRequestID, log.id]);

    return (
        <MorphingDialog>
            <MorphingDialogTrigger
                aria-label={tOverview('openDetails', { model: displayRequestModelName, id: log.id })}
                onClick={() => {
                    if (!detailLog && !detailLoading) {
                        setDetailLoading(true);
                        setDetailRequestID((value) => value + 1);
                    }
                }}
                className={cn(
                    'page-card w-full text-left transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    hasError ? 'border-destructive/40 hover:border-destructive/60' : 'hover:border-primary/35',
                )}
            >
                <LogSummary
                    log={log}
                    displayFields={displayFields}
                    tokenUsage={tokenUsage}
                    endpointLabel={endpointLabel}
                    channelNameById={channelNameById}
                />
            </MorphingDialogTrigger>

            <MorphingDialogContainer>
                <MorphingDialogContent className="relative w-[calc(100vw-2rem)] md:w-[80vw] bg-card text-card-foreground px-4 sm:px-6 py-4 rounded-2xl h-[calc(100dvh-2rem)] flex flex-col overflow-hidden">
                    <MorphingDialogClose className="top-4 right-5 text-muted-foreground hover:text-foreground transition-colors" />
                    <MorphingDialogTitle className="mb-3 flex min-w-0 items-start gap-3 pr-14 text-sm md:pr-16">
                        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
                            <ModelAvatar size={28} />
                            <span className="max-w-full truncate font-semibold text-card-foreground" title={displayRequestModelName}>{displayRequestModelName}</span>
                            {log.is_test ? <Badge variant="outline" className="shrink-0 border-blue-400/50 px-1.5 py-0 text-xs text-blue-500 dark:text-blue-400"><TestTube2 className="mr-1 size-3" />{t('testLog')}</Badge> : null}
                            <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" />
                            {visibility.endpointType && endpointLabel ? <Badge variant="secondary" className="shrink-0 px-1.5 py-0 text-xs" style={{ backgroundColor: `${brandColor}15`, color: brandColor }}>{endpointLabel}</Badge> : null}
                            {visibility.channelName ? <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" /> : null}
                            {visibility.channelName && hasMultipleAttempts ? (
                                <RetryBadgeWithTooltip
                                    channelName={displayChannelName}
                                    brandColor={brandColor}
                                    attempts={log.attempts!}
                                    channelNameById={channelNameById}
                                />
                            ) : visibility.channelName ? (
                                <Badge
                                    variant="secondary"
                                    className="shrink-0 text-xs px-1.5 py-0"
                                    style={{ backgroundColor: `${brandColor}15`, color: brandColor }}
                                >
                                    {displayChannelName}
                                </Badge>
                            ) : null}
                            {visibility.actualModel ? <span className="text-muted-foreground truncate">{displayActualModelName}</span> : null}
                            {log.attempts?.some((attempt) => attempt.sticky) ? (
                                <Pin className="size-3.5 shrink-0 text-amber-500" />
                            ) : null}
                        </div>
                        <WSModeBadge log={log} />
                    </MorphingDialogTitle>

                    <MorphingDialogDescription className="flex-1 min-h-0">
                        <div className="flex flex-col min-h-0 h-full gap-4">
                            <LogDiagnosticPanel
                                log={log}
                                channelNameById={channelNameById}
                                expanded={isDiagnosticExpanded}
                                onToggle={() => setIsDiagnosticExpanded((value) => !value)}
                            />
                            {detailLog?.trace ? <LogTracePanels id={log.id} trace={detailLog.trace} /> : <LogContentPanels
                                requestContent={requestContent}
                                responseContent={responseContent}
                                requestTokens={totalInputTokens}
                                responseTokens={displayFields.outputTokens}
                                detailLoading={detailLoading}
                                requestCollapsed={requestJsonCollapsed}
                                responseCollapsed={responseJsonCollapsed}
                                onToggleRequest={() => setRequestJsonCollapsed((value) => !value)}
                                onToggleResponse={() => setResponseJsonCollapsed((value) => !value)}
                            />}
                        </div>
                    </MorphingDialogDescription>

                    <div className="mt-auto flex shrink-0 flex-wrap items-center gap-3 pt-4 text-xs text-muted-foreground md:gap-4">
                        <div className="flex items-center gap-1.5">
                            <Clock className="size-3.5" style={{ color: brandColor }} />
                            <span className="tabular-nums">{formatTime(log.time)}</span>
                        </div>
                        {visibility.apiKeyName && requestAPIKeyName ? (
                            <div className="flex min-w-0 items-center gap-1.5">
                                <KeyRound className="size-3.5 shrink-0 text-orange-500" />
                                <span className="truncate" title={requestAPIKeyName}>
                                    {requestAPIKeyName}
                                </span>
                            </div>
                        ) : null}
                        {visibility.clientIP && clientIP ? <div className="flex items-center gap-1.5"><Globe className="size-3.5 text-sky-500" /><span>{clientIP}</span></div> : null}
                        <div className="flex items-center gap-1.5">
                            <Zap className="size-3.5 text-amber-500" />
                            <span>{t('firstTokenTime')}: {formatDurationCompact(log.ftut)}</span>
                        </div>
                        <div className="flex items-center gap-1.5"><Cpu className="size-3.5 text-blue-500" /><span>{t('totalTime')}: {formatDurationCompact(log.use_time)}</span></div>
                        {visibility.cost ? <div className="flex items-center gap-1.5">
                            <DollarSign className="size-3.5 text-emerald-500" />
                            <span className="font-medium text-emerald-600 dark:text-emerald-400">{t('cost')}: {Number(log.cost).toFixed(6)}</span>
                        </div> : null}
                        {visibility.tps ? <div className="flex items-center gap-1.5"><Gauge className="size-3.5 text-lime-500" /><span>{t('tps')}: {formatTPS(log.output_tokens, log.use_time)}</span></div> : null}
                        {visibility.cacheHitRate && cacheReadTokens > 0 ? <div className="flex items-center gap-1.5"><Percent className="size-3.5 text-teal-500" /><span>{t('cacheHitRate')}: {formatCacheHitRate(cacheReadTokens, totalInputTokens)}</span></div> : null}
                        <div className="flex items-center gap-1.5"><Sigma className="size-3.5 text-rose-500" /><span className="font-medium text-rose-600 dark:text-rose-400">{t('totalTokens')}: {totalTokens.toLocaleString()}</span></div>
                        {cacheReadTokens > 0 ? <div className="flex items-center gap-1.5"><ArrowDownToLine className="size-3.5 text-teal-500" /><span>{t('cacheHit')}: {formatCompactTokenCount(cacheReadTokens)}</span></div> : null}
                        {cacheWriteTokens > 0 ? <div className="flex items-center gap-1.5"><ArrowDownToLine className="size-3.5 text-sky-500" /><span>{t('cacheWrite')}: {formatCompactTokenCount(cacheWriteTokens)}</span></div> : null}
                        {displayFields.semanticCacheHit ? <div className="flex items-center gap-1.5"><ArrowDownToLine className="size-3.5 text-cyan-500" /><span>{t('semanticCacheHit')}</span></div> : null}
                        {visibility.reasoningEffort && log.reasoning_effort ? <div className="flex items-center gap-1.5"><Brain className="size-3.5 text-violet-500" /><span>{t('reasoningEffort')}: {log.reasoning_effort}</span></div> : null}
                        {visibility.reasoningTokens && (log.reasoning_tokens ?? 0) > 0 ? <div className="flex items-center gap-1.5"><Brain className="size-3.5 text-indigo-500" /><span>{t('reasoningTokens')}: {formatCompactTokenCount(log.reasoning_tokens ?? 0)}t</span></div> : null}
                        {visibility.reasoningTokens && (log.reasoning_tokens ?? 0) <= 0 && (log.reasoning_chars ?? 0) > 0 ? <div className="flex items-center gap-1.5"><Type className="size-3.5 text-indigo-500" /><span>{t('reasoningChars')}: {formatCompactTokenCount(log.reasoning_chars ?? 0)}{t('reasoningCharsUnit')}</span></div> : null}
                    </div>
                </MorphingDialogContent>
            </MorphingDialogContainer>
        </MorphingDialog>
    );
}
