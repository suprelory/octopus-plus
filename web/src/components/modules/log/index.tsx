'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Columns3, Inbox, TriangleAlert } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useChannelList } from '@/api/endpoints/channel';
import {
    logPageQueryKey,
    useLogPage,
    useLogStream,
    type LogKeywordMode,
    type LogKeywordScope,
    type LogPageResponse,
    type LogStatusFilter,
    type RelayLog,
} from '@/api/endpoints/log';
import { Pagination } from '@/components/common/Pagination';
import { PageOverview } from '@/components/common/PageOverview';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useSearchStore } from '@/components/modules/toolbar';
import { useToolbarViewOptionsStore } from '@/components/modules/toolbar/view-options-store';
import { cn } from '@/lib/utils';
import { LogCard } from './Item';
import { LogListSkeleton } from './Skeleton';
import { useLogFieldVisibilityStore, useLogUIStore, type LogFieldName } from './ui-store';

type LogFilters = {
    keyword: string;
    keywordMode: LogKeywordMode;
    keywordScope: LogKeywordScope;
    status: LogStatusFilter;
    channelIds: number[];
    startTime?: number;
    endTime?: number;
};

/** 实时推送的四种可见状态；suspended 表示开关是开的但当前页/筛选下不适用。 */
type LiveState = 'on' | 'connecting' | 'suspended' | 'off';

const VIEW_OPTION_FIELDS: readonly LogFieldName[] = [
    'endpointType',
    'channelName',
    'actualModel',
    'apiKeyName',
    'clientIP',
    'cost',
    'tps',
    'cacheHitRate',
    'reasoningEffort',
    'reasoningTokens',
];

function useDebouncedValue<T>(value: T, delay = 200) {
    const [debounced, setDebounced] = useState(value);
    useEffect(() => {
        const handle = setTimeout(() => setDebounced(value), delay);
        return () => clearTimeout(handle);
    }, [value, delay]);
    return debounced;
}

function filtersActive(filters: LogFilters) {
    return (
        !!filters.keyword.trim() ||
        filters.status !== 'all' ||
        filters.channelIds.length > 0 ||
        !!filters.startTime ||
        !!filters.endTime
    );
}

/**
 * 日志页面组件
 * - 页码分页读取历史日志（后端 page 模式只回 total，页数由 total 推导）
 * - 第 1 页且无筛选时保持 SSE 实时推送，新日志直接插入当前页缓存
 * - 其余页码/筛选下挂起推送，避免与 offset 分页互相打架
 */
export function Log() {
    const tList = useTranslations('log.list');
    const tOverview = useTranslations('workspace');
    const tLive = useTranslations('log.live');
    const tView = useTranslations('log.viewOptions');
    const pageKey = 'log' as const;
    const queryClient = useQueryClient();

    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const refreshRequestId = useLogUIStore((s) => s.refreshRequestId);
    const setRefreshing = useLogUIStore((s) => s.setRefreshing);
    const page = useLogUIStore((s) => s.page);
    const pageSize = useLogUIStore((s) => s.pageSize);
    const liveEnabled = useLogUIStore((s) => s.liveEnabled);
    const setPage = useLogUIStore((s) => s.setPage);
    const setPageSize = useLogUIStore((s) => s.setPageSize);
    const setLiveEnabled = useLogUIStore((s) => s.setLiveEnabled);
    const lastHandledRefreshRequestIdRef = useRef(refreshRequestId);

    const logDateRange = useToolbarViewOptionsStore((s) => s.logDateRange);
    const logChannelIds = useToolbarViewOptionsStore((s) => s.logChannelIds);
    const logKeywordMode = useToolbarViewOptionsStore((s) => s.logKeywordMode);
    const logKeywordScope = useToolbarViewOptionsStore((s) => s.logKeywordScope);
    const logStatus = useToolbarViewOptionsStore((s) => s.logStatus);

    const filters = useMemo<LogFilters>(() => ({
        keyword: searchTerm,
        keywordMode: logKeywordMode,
        keywordScope: logKeywordScope,
        status: logStatus,
        channelIds: logChannelIds,
        startTime: logDateRange.start,
        endTime: logDateRange.end,
    }), [logDateRange.end, logDateRange.start, logChannelIds, searchTerm, logKeywordMode, logKeywordScope, logStatus]);
    const debouncedFilters = useDebouncedValue(filters, 200);
    const hasFilters = filtersActive(debouncedFilters);
    const logFilters = useMemo(() => ({
        keyword: debouncedFilters.keyword.trim() || undefined,
        keyword_mode: debouncedFilters.keyword.trim() ? debouncedFilters.keywordMode : undefined,
        keyword_scope: debouncedFilters.keyword.trim() ? debouncedFilters.keywordScope : undefined,
        status: debouncedFilters.status,
        channel_ids: debouncedFilters.channelIds.length > 0 ? debouncedFilters.channelIds : undefined,
        start_time: debouncedFilters.startTime,
        end_time: debouncedFilters.endTime,
    }), [debouncedFilters]);

    // 筛选变化后停留在旧页码没有意义（总页数已经变了），统一回到第 1 页。
    const filterSignature = useMemo(() => JSON.stringify(logFilters), [logFilters]);
    const lastFilterSignatureRef = useRef(filterSignature);
    useEffect(() => {
        if (lastFilterSignatureRef.current === filterSignature) return;
        lastFilterSignatureRef.current = filterSignature;
        setPage(1);
    }, [filterSignature, setPage]);

    const logsQuery = useLogPage({
        page,
        page_size: pageSize,
        with_total: true,
        include_content: false,
        ...logFilters,
    });
    const { refetch } = logsQuery;

    const logs = useMemo(() => logsQuery.data?.logs ?? [], [logsQuery.data]);
    const total = logsQuery.data?.total ?? 0;
    const totalExact = logsQuery.data?.total_exact ?? true;
    const hasMore = logsQuery.data?.has_more ?? false;
    const warning = logsQuery.data?.warning ?? null;
    const errorMessage = logsQuery.isError
        ? (logsQuery.error instanceof Error ? logsQuery.error.message : String(logsQuery.error))
        : null;

    // 日志清理或筛选收窄后当前页可能越界，回退到最后一页而不是停在空白页。
    // total 是下界时（后端有界计数）不能这样回退——真实页数还在后面。
    useEffect(() => {
        if (logsQuery.isFetching || total === 0 || !totalExact) return;
        const totalPages = Math.max(1, Math.ceil(total / pageSize));
        if (page > totalPages) setPage(totalPages);
    }, [logsQuery.isFetching, page, pageSize, setPage, total, totalExact]);

    // 只有第 1 页 + 无筛选时才推送：其余情况下推送与 offset 分页语义冲突，
    // 且流里的日志未必符合当前筛选条件。
    const liveApplicable = page === 1 && !hasFilters;
    const handleStreamLog = useCallback((log: RelayLog) => {
        queryClient.setQueryData<LogPageResponse>(logPageQueryKey(pageSize, 1, logFilters), (old) => {
            if (!old) return old;
            if (old.logs.some((item) => item.id === log.id)) return old;
            return {
                ...old,
                logs: [log, ...old.logs].slice(0, pageSize),
                total: old.total + 1,
            };
        });
    }, [logFilters, pageSize, queryClient]);
    const isLiveConnected = useLogStream(handleStreamLog, liveEnabled && liveApplicable);

    const liveState: LiveState = !liveEnabled
        ? 'off'
        : !liveApplicable
            ? 'suspended'
            : isLiveConnected
                ? 'on'
                : 'connecting';

    const visibility = useLogFieldVisibilityStore((state) => state.visibility);
    const { data: channels = [] } = useChannelList();
    const channelNameById = useMemo(() => {
        const map = new Map<number, string>();
        for (const channel of channels) map.set(channel.raw.id, channel.raw.name);
        return map;
    }, [channels]);

    const refreshIdRef = useRef(0);
    const handleRefresh = useCallback(async () => {
        refreshIdRef.current += 1;
        const myId = refreshIdRef.current;
        setRefreshing(true);
        const startedAt = Date.now();
        try {
            await refetch();
        } finally {
            const elapsed = Date.now() - startedAt;
            const remaining = Math.max(0, 500 - elapsed);
            setTimeout(() => {
                if (refreshIdRef.current === myId) setRefreshing(false);
            }, remaining);
        }
    }, [refetch, setRefreshing]);

    useEffect(() => {
        if (refreshRequestId === lastHandledRefreshRequestIdRef.current) return;
        lastHandledRefreshRequestIdRef.current = refreshRequestId;
        void handleRefresh();
    }, [handleRefresh, refreshRequestId]);

    const showSkeleton = logsQuery.isLoading;
    const showEmpty = !logsQuery.isLoading && !logsQuery.isError && logs.length === 0;
    const isRefetching = logsQuery.isFetching && !logsQuery.isLoading;

    return (
        <div className="flex h-full min-h-0 flex-col gap-3 overflow-hidden">
            <PageOverview className="shrink-0" title={tOverview('log.title')} description={tOverview('log.description')} metrics={[
                { label: tOverview('total'), value: logsQuery.isLoading || logsQuery.isError ? '—' : `${total.toLocaleString()}${totalExact ? '' : '+'}` },
                { label: tOverview('log.currentPage'), value: logs.length, accent: true },
            ]}>
            <div className="flex flex-wrap items-center gap-1">
                <button
                    type="button"
                    aria-pressed={liveEnabled}
                    aria-label={liveEnabled ? tLive('disable') : tLive('enable')}
                    onClick={() => setLiveEnabled(!liveEnabled)}
                    title={liveState === 'suspended'
                        ? tLive('suspendedHint')
                        : liveEnabled ? tLive('disable') : tLive('enable')}
                    className={cn(
                        'flex items-center gap-1.5 rounded-lg border border-border/60 bg-card px-3 py-2 text-xs transition-colors hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring',
                        liveState === 'on' ? 'text-emerald-600 dark:text-emerald-400' : 'text-muted-foreground hover:text-foreground',
                    )}
                >
                    <span
                        className={cn(
                            'size-1.5 rounded-full',
                            liveState === 'on' && 'animate-pulse bg-emerald-500',
                            liveState === 'connecting' && 'animate-pulse bg-amber-500',
                            liveState === 'suspended' && 'bg-amber-500/50',
                            liveState === 'off' && 'bg-muted-foreground/40',
                        )}
                    />
                    {tLive(liveState)}
                </button>
                <Popover>
                    <PopoverTrigger asChild>
                        <button type="button" className="flex items-center gap-1.5 rounded-lg px-3 py-2 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring">
                            <Columns3 className="size-3.5" />{tView('title')}
                        </button>
                    </PopoverTrigger>
                    <PopoverContent align="end" className="w-56 p-3">
                        <div className="mb-2 text-xs font-medium text-muted-foreground">{tView('title')}</div>
                        <div className="flex flex-col gap-1">
                            {VIEW_OPTION_FIELDS.map((field) => (
                                <label key={field} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-xs transition-colors hover:bg-muted">
                                    <input type="checkbox" checked={visibility[field]} onChange={() => useLogFieldVisibilityStore.getState().toggleField(field)} className="size-3 rounded" />
                                    {tView(field)}
                                </label>
                            ))}
                        </div>
                        <button type="button" onClick={() => useLogFieldVisibilityStore.getState().resetFields()} className="mt-2 w-full rounded-md border border-border/50 px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">{tView('reset')}</button>
                    </PopoverContent>
                </Popover>
            </div>
            </PageOverview>

            {errorMessage ? (
                <div className="flex shrink-0 items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
                    <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
                    <div className="flex-1 min-w-0">
                        <p className="font-medium">{tList('loadFailed')}</p>
                        <p className="mt-0.5 break-words opacity-90">{errorMessage}</p>
                    </div>
                    <button
                        type="button"
                        onClick={() => void refetch()}
                        className="shrink-0 rounded-md border border-destructive/40 px-2 py-0.5 font-medium transition-colors hover:bg-destructive/15"
                    >
                        {tList('retry')}
                    </button>
                </div>
            ) : null}

            {warning ? (
                <div className="shrink-0 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
                    {warning}
                </div>
            ) : null}

            <div
                aria-busy={isRefetching}
                className={cn(
                    'relative min-h-0 flex-1 transition-opacity duration-200',
                    isRefetching && 'opacity-60',
                )}
            >
                {showSkeleton ? (
                    <div className="h-full overflow-hidden rounded-t-xl">
                        <LogListSkeleton count={Math.min(pageSize, 6)} />
                    </div>
                ) : showEmpty ? (
                    <div className="page-empty-state flex h-full flex-col items-center justify-center gap-2 border-border/60">
                        <Inbox className="size-8 text-muted-foreground/40" />
                        <p className="text-sm font-medium text-muted-foreground">
                            {hasFilters ? tList('emptyFiltered') : tList('empty')}
                        </p>
                        <p className="text-xs text-muted-foreground/70">
                            {hasFilters ? tList('emptyFilteredHint') : tList('emptyHint')}
                        </p>
                    </div>
                ) : (
                    <VirtualizedGrid
                        items={logs}
                        layout="list"
                        columns={{ default: 1 }}
                        estimateItemHeight={180}
                        overscan={8}
                        getItemKey={(log) => `log-${log.id}`}
                        renderItem={(log) => <LogCard log={log} channelNameById={channelNameById} />}
                        scrollResetKey={`${page}|${pageSize}|${filterSignature}`}
                    />
                )}
            </div>

            <Pagination
                page={page}
                pageSize={pageSize}
                total={total}
                totalExact={totalExact}
                hasMore={hasMore}
                onPageChange={setPage}
                onPageSizeChange={setPageSize}
                className="rounded-xl border border-border/60 bg-card/60 px-3 py-2"
            />
        </div>
    );
}
