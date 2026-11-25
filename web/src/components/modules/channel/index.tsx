'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useChannelList } from '@/api/endpoints/channel';
import { Card } from './Card';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { cn } from '@/lib/utils';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';
import {
    isChannelJumpTarget,
    type ChannelJumpTarget,
    type PendingJump,
    useJumpStore,
} from '@/stores/jump';
import { ChannelListSummary } from './ListSummary';
import { Network, SearchX } from 'lucide-react';
import { useTranslations } from 'next-intl';

type ChannelPendingJump = PendingJump & { target: ChannelJumpTarget };

export function Channel() {
    const t = useTranslations('channel.list');
    const { data: channelsData, isLoading, error } = useChannelList();
    const pendingJump = useJumpStore((state) => state.pending);
    const clearPending = useJumpStore((state) => state.clearPending);
    const pageKey = 'channel' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const layout = useToolbarViewOptionsStore((s) => s.getLayout(pageKey));
    const sortField = useToolbarViewOptionsStore((s) => s.getSortField(pageKey));
    const sortOrder = useToolbarViewOptionsStore((s) => s.getSortOrder(pageKey));
    const [highlightedChannelId, setHighlightedChannelId] = useState<number | null>(null);
    const channelCardRefs = useRef<Map<number, HTMLDivElement>>(new Map());

    const pendingChannelJump = pendingJump && isChannelJumpTarget(pendingJump.target)
        ? pendingJump as ChannelPendingJump
        : null;
    const targetedChannelId = pendingChannelJump?.target.channelId ?? null;

    const setChannelCardRef = useCallback((channelId: number, node: HTMLDivElement | null) => {
        const refs = channelCardRefs.current;
        if (node) {
            refs.set(channelId, node);
            return;
        }
        refs.delete(channelId);
    }, []);

    const flashChannelCard = useCallback((channelId: number) => {
        setHighlightedChannelId(channelId);
        window.setTimeout(() => {
            setHighlightedChannelId((current) => (current === channelId ? null : current));
        }, 1800);
    }, []);

    const sortedChannels = useMemo(() => {
        if (!channelsData) return [];
        return [...channelsData].sort((a, b) => {
            const diff = sortField === 'name'
                ? a.raw.name.localeCompare(b.raw.name)
                : a.raw.id - b.raw.id;
            return sortOrder === 'asc' ? diff : -diff;
        });
    }, [channelsData, sortField, sortOrder]);

    const visibleChannels = useMemo(() => {
        const term = searchTerm.toLowerCase().trim();

        return sortedChannels.filter((channel) => {
            if (channel.raw.id === targetedChannelId) {
                return true;
            }

            if (term && !channel.raw.name.toLowerCase().includes(term)) {
                return false;
            }

            return true;
        });
    }, [sortedChannels, searchTerm, targetedChannelId]);

    useEffect(() => {
        if (!pendingChannelJump) return;
        const channelId = pendingChannelJump.target.channelId;
        const node = channelCardRefs.current.get(channelId);
        if (!node) return;

        const timer = window.setTimeout(() => {
            node.scrollIntoView({ behavior: 'smooth', block: 'center' });
            flashChannelCard(channelId);
            clearPending(pendingChannelJump.requestId);
        }, 80);

        return () => window.clearTimeout(timer);
    }, [pendingChannelJump, clearPending, flashChannelCard, visibleChannels.length]);

    const renderChannelCard = useCallback((item: NonNullable<typeof channelsData>[number]) => (
        <div
            ref={(node) => setChannelCardRef(item.raw.id, node)}
            className={cn(
                'h-full rounded-2xl transition-shadow',
                highlightedChannelId === item.raw.id && 'ring-2 ring-primary/35 ring-offset-2 ring-offset-background',
            )}
        >
            <Card channel={item.raw} stats={item.formatted} layout={layout} />
        </div>
    ), [highlightedChannelId, layout, setChannelCardRef]);

    const computeColumns = useCallback((width: number) => {
        if (layout === 'list') return 1;
        const MIN_CARD_WIDTH = 320;
        const GUTTER = 16;
        const cols = Math.floor((width + GUTTER) / (MIN_CARD_WIDTH + GUTTER));
        return Math.max(1, Math.min(6, cols));
    }, [layout]);

    const header = !isLoading && !error
        ? <ChannelListSummary
            total={sortedChannels.length}
            enabled={sortedChannels.filter(channel => channel.raw.enabled).length}
            visible={visibleChannels.length}
        />
        : null;

    const footer = isLoading ? (
        <div className={cn('grid gap-4', layout === 'list' ? 'grid-cols-1' : 'md:grid-cols-2 lg:grid-cols-3')}>
            {Array.from({ length: layout === 'list' ? 2 : 3 }).map((_, index) => (
                <div key={index} className="h-65 animate-pulse rounded-2xl border border-border/60 bg-muted/40" />
            ))}
        </div>
    ) : error ? (
        <div className="page-card border-destructive/30 bg-destructive/10 px-4 py-6 text-sm text-destructive">
            {t('loadFailed', { message: error.message })}
        </div>
    ) : visibleChannels.length === 0 ? (
        <div className="flex flex-col items-center rounded-2xl border border-dashed border-border bg-card/60 px-5 py-12 text-center">
            <span className="mb-4 rounded-2xl bg-primary/8 p-3 text-primary">{searchTerm ? <SearchX className="size-6" /> : <Network className="size-6" />}</span>
            <p className="text-sm font-medium">{t(searchTerm ? 'noResults' : 'empty')}</p>
            <p className="mt-2 max-w-xs text-xs leading-relaxed text-muted-foreground">{t(searchTerm ? 'noResultsHint' : 'emptyHint')}</p>
        </div>
    ) : null;

    return (
        <VirtualizedGrid
            items={visibleChannels}
            layout={layout}
            columns={computeColumns}
            estimateItemHeight={layout === 'list' ? 200 : 280}
            header={header}
            footer={footer}
            getItemKey={(item) => `channel-${item.raw.id}`}
            renderItem={renderChannelCard}
        />
    );
}
