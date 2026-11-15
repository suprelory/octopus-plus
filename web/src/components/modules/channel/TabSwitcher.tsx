'use client';

import { useMemo } from 'react';
import { useTranslations } from 'next-intl';
import { motion } from 'motion/react';
import { useChannelList } from '@/api/endpoints/channel';
import { useSiteChannelList } from '@/api/endpoints/site-channel';
import { cn } from '@/lib/utils';
import { useChannelTabStore, type ChannelTab } from './tab-store';

const TABS: { value: ChannelTab; key: 'site' | 'manual' }[] = [
    { value: 'site', key: 'site' },
    { value: 'manual', key: 'manual' },
];

type Props = { className?: string };

export function ChannelTabSwitcher({ className }: Props) {
    const t = useTranslations('channel.tabs');
    const activeTab = useChannelTabStore((s) => s.activeTab);
    const setActiveTab = useChannelTabStore((s) => s.setActiveTab);
    const { data: channelsData } = useChannelList();
    const { data: siteChannelsData } = useSiteChannelList({ includeHistory: false });

    const counts = useMemo(
        () => ({
            site: (siteChannelsData ?? []).filter((card) => card.account_count > 0).length,
            manual: (channelsData ?? []).filter((c) => !c.raw.managed).length,
        }),
        [channelsData, siteChannelsData],
    );

    return (
        <div role="group" aria-label={t('label')} className={cn('inline-flex w-fit items-center gap-1 rounded-xl border border-border/60 bg-card/70 p-1', className)}>
            {TABS.map(({ value, key }) => {
                const active = activeTab === value;
                return (
                    <button
                        key={value}
                        type="button"
                        aria-pressed={active}
                        onClick={() => setActiveTab(value)}
                        className={cn(
                            'relative inline-flex items-center gap-1.5 whitespace-nowrap rounded-lg px-3 py-1.5 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring',
                            active ? 'text-foreground' : 'text-muted-foreground hover:text-foreground',
                        )}
                    >
                        <span className="relative z-10">{t(key)}</span>
                        <span
                            className={cn(
                                'relative z-10 rounded-md px-1.5 py-0.5 text-[10px] tabular-nums transition-colors',
                                active ? 'bg-primary/10 font-semibold text-primary' : 'text-muted-foreground',
                            )}
                        >
                            {counts[value]}
                        </span>
                        {active && (
                            <motion.span
                                layoutId="channel-tab-indicator"
                                className="absolute inset-0 rounded-lg bg-background shadow-sm"
                                transition={{ type: 'spring', stiffness: 320, damping: 30, mass: 0.8 }}
                            />
                        )}
                    </button>
                );
            })}
        </div>
    );
}
