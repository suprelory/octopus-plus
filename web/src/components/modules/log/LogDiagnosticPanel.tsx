'use client';

import { AlertCircle, ChevronDown, ChevronUp, Pin, RotateCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { AnimatePresence, motion } from 'motion/react';
import { Badge } from '@/components/ui/badge';
import { CopyIconButton } from '@/components/common/CopyButton';
import { cn } from '@/lib/utils';
import type { RelayLog } from '@/api/endpoints/log';
import { formatAttemptAdapterLabel, formatDuration, mergeAdjacentAttempts, sanitizeErrorMessage, isLogFailure } from './log-format';
import { getAttemptStatusMeta } from './log-status';
import { RoutingDetails } from './RoutingDetails';

export function LogDiagnosticPanel({
    log,
    channelNameById,
    expanded,
    onToggle,
}: {
    log: RelayLog;
    channelNameById?: ReadonlyMap<number, string>;
    expanded: boolean;
    onToggle: () => void;
}) {
    const t = useTranslations('log.card');
    const hasError = isLogFailure(log);
    const hasAttempts = (log.attempts?.length ?? 0) > 0;
    const forwardedAttempts = (log.attempts ?? []).filter((attempt) => attempt.status === 'success' || attempt.status === 'failed').length;

    if (!hasError && !hasAttempts) return null;

    const diagnosticTitle = hasAttempts ? t('retryDetails') : t('errorInfo');
    const DiagnosticIcon = hasAttempts ? RotateCw : AlertCircle;
    const mergedAttempts = mergeAdjacentAttempts(log.attempts ?? []);

    return (
        <div className={cn(
            'flex-initial min-h-0 flex flex-col rounded-2xl border overflow-hidden max-h-[40%]',
            hasError ? 'bg-destructive/5 border-destructive/20' : 'bg-secondary/30 border-border/50',
        )}>
            <div
                className={cn(
                    'flex items-center gap-2 px-3 py-2.5 shrink-0 cursor-pointer select-none hover:bg-muted/50 transition-colors',
                    hasError && 'hover:bg-destructive/10',
                )}
                onClick={onToggle}
            >
                <DiagnosticIcon className={cn('size-4', hasError ? 'text-destructive' : 'text-muted-foreground')} />
                <span className={cn('text-sm font-medium', hasError ? 'text-destructive' : 'text-secondary-foreground')}>
                    {diagnosticTitle}
                </span>
                <div className="ml-auto flex items-center gap-2">
                    {hasAttempts ? (
                        <Badge
                            variant="outline"
                            className={cn(
                                'text-xs border-0',
                                hasError ? 'bg-destructive/10 text-destructive' : 'bg-secondary text-secondary-foreground',
                            )}
                        >
                            {log.total_attempts || log.attempts!.length} {t('attempts')}
                        </Badge>
                    ) : null}
                    {hasAttempts && forwardedAttempts < (log.total_attempts || log.attempts!.length) ? (
                        <Badge variant="outline" className="border-0 bg-muted/50 text-xs text-muted-foreground">
                            {t('forwardedAttempts', { count: forwardedAttempts })}
                        </Badge>
                    ) : null}
                    {expanded ? <ChevronUp className="size-4 text-muted-foreground" /> : <ChevronDown className="size-4 text-muted-foreground" />}
                </div>
            </div>

            <AnimatePresence initial={false}>
                {expanded ? (
                    <motion.div
                        initial={{ height: 0, opacity: 0 }}
                        animate={{ height: 'auto', opacity: 1 }}
                        exit={{ height: 0, opacity: 0 }}
                        transition={{ duration: 0.2, ease: 'easeInOut' }}
                        className="overflow-hidden flex flex-col min-h-0"
                    >
                        <div className="flex-1 overflow-auto p-2.5 md:p-3 flex flex-col gap-4">
                            {hasError ? (
                                <div className="relative pl-1">
                                    <div className="absolute right-0 top-0">
                                        <CopyIconButton
                                            text={log.error || t('failed')}
                                            className="p-1 rounded-md text-destructive/60 hover:text-destructive hover:bg-destructive/10 transition-colors"
                                            copyIconClassName="size-4"
                                            checkIconClassName="size-4"
                                        />
                                    </div>
                                    <p className="text-sm text-destructive whitespace-pre-wrap wrap-break-word pr-8 leading-relaxed">
                                        {sanitizeErrorMessage(log.error) || t('failed')}
                                    </p>
                                </div>
                            ) : null}

                            {hasAttempts ? (
                                <div className="flex flex-col gap-2">
                                    {mergedAttempts.map((attempt, idx) => {
                                        const statusMeta = getAttemptStatusMeta(attempt.status, t);
                                        const sanitizedMsg = sanitizeErrorMessage(attempt.msg);

                                        return (
                                            <div
                                                key={`${attempt.attempt_num || idx}-${attempt.channel_id}-${attempt.model_name}-${idx}`}
                                                className={cn(
                                                    'text-xs p-2.5 rounded-xl border transition-colors flex flex-col gap-2',
                                                    statusMeta.containerClassName,
                                                )}
                                            >
                                                <div className="flex items-start gap-2">
                                                    <Badge className={cn('h-5 shrink-0 px-1.5 text-[10px] font-bold uppercase shadow-none border-0', statusMeta.badgeClassName)}>
                                                        {statusMeta.label}
                                                    </Badge>
                                                    <div className="min-w-0 flex-1">
                                                        <div className="flex items-center gap-2">
                                                            <span className="font-semibold text-foreground">
                                                                {attempt.channel_name?.trim() || channelNameById?.get(attempt.channel_id) || t('channelFallback', { id: attempt.channel_id })}
                                                            </span>
                                                            <span className="text-muted-foreground truncate">({attempt.model_name})</span>
                                                            {attempt.adapter_type ? (
                                                                <span className="rounded-md bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                                                                    {formatAttemptAdapterLabel(t, attempt.adapter_type)}
                                                                </span>
                                                            ) : null}
                                                            {attempt.sticky ? <Pin className="size-3.5 shrink-0 text-amber-500" /> : null}
                                                            {attempt.repeat > 1 ? (
                                                                <Badge variant="outline" className="h-5 px-1.5 text-[10px] font-semibold tabular-nums">×{attempt.repeat}</Badge>
                                                            ) : null}
                                                        </div>
                                                    </div>
                                                    <div className="ml-auto flex items-center gap-2 shrink-0">
                                                        <span className="text-muted-foreground tabular-nums font-mono">{formatDuration(attempt.totalDuration)}</span>
                                                    </div>
                                                </div>
                                                {sanitizedMsg ? (
                                                    <div className={cn('pl-2 border-l-2 text-[11px] leading-relaxed whitespace-pre-wrap wrap-break-word', statusMeta.messageClassName)}>
                                                        {sanitizedMsg}
                                                    </div>
                                                ) : null}
                                                <RoutingDetails attempt={attempt} />
                                            </div>
                                        );
                                    })}
                                </div>
                            ) : null}
                        </div>
                    </motion.div>
                ) : null}
            </AnimatePresence>
        </div>
    );
}
