'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { AnimatePresence, motion } from 'motion/react';
import { ChevronDown, Search, WandSparkles, X } from 'lucide-react';
import { AutoGroupType } from '@/api/endpoints/channel';
import {
    type GroupAutoGroupSource,
    useGroupAutoGroupConfig,
    useUpdateGroupAutoGroupConfig,
} from '@/api/endpoints/group';
import {
    MorphingDialogClose,
    MorphingDialogDescription,
    MorphingDialogTitle,
    useMorphingDialog,
} from '@/components/ui/morphing-dialog';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { HoverCard, HoverCardContent, HoverCardTrigger } from '@/components/ui/hover-card';
import { toast } from '@/components/common/Toast';
import { cn } from '@/lib/utils';

const AUTO_GROUP_VALUES = [
    AutoGroupType.None,
    AutoGroupType.Fuzzy,
    AutoGroupType.Exact,
    AutoGroupType.Regex,
] as const;
const MODEL_PREVIEW_LIMIT = 24;

type SourceTreeGroup = {
    key: string;
    label: string;
    sources: GroupAutoGroupSource[];
};

function modeKey(value: AutoGroupType) {
    switch (value) {
        case AutoGroupType.Fuzzy:
            return 'fuzzy';
        case AutoGroupType.Exact:
            return 'exact';
        case AutoGroupType.Regex:
            return 'regex';
        case AutoGroupType.None:
        default:
            return 'none';
    }
}

function matchesKeyword(source: GroupAutoGroupSource, keyword: string) {
    if (!keyword) return true;
    const haystack = [
        source.channel_name,
        source.endpoint_type,
        ...source.models,
    ].join('\n').toLowerCase();
    return haystack.includes(keyword);
}

function TristateCheckbox({
    state,
    onChange,
    ariaLabel,
    className,
}: {
    state: 'unchecked' | 'partial' | 'checked';
    onChange: (next: boolean) => void;
    ariaLabel: string;
    className?: string;
}) {
    const ref = useRef<HTMLInputElement>(null);
    useEffect(() => {
        if (ref.current) ref.current.indeterminate = state === 'partial';
    }, [state]);
    return (
        <input
            ref={ref}
            type="checkbox"
            checked={state === 'checked'}
            aria-label={ariaLabel}
            onClick={(event) => event.stopPropagation()}
            onChange={(event) => onChange(event.target.checked)}
            className={cn(
                'size-3.5 shrink-0 rounded border-border bg-background accent-primary',
                className,
            )}
        />
    );
}

function ModelPreview({ source }: { source: GroupAutoGroupSource }) {
    const t = useTranslations('group.autoGroup');
    const models = source.models.slice(0, MODEL_PREVIEW_LIMIT);
    const extraCount = Math.max(0, source.models.length - MODEL_PREVIEW_LIMIT);

    return (
        <HoverCard openDelay={120} closeDelay={150}>
            <HoverCardTrigger asChild>
                <button
                    type="button"
                    className="h-5 rounded-md px-1.5 text-[10px] tabular-nums text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                    aria-label={t('source.modelCount', { count: source.model_count })}
                >
                    {source.model_count}
                </button>
            </HoverCardTrigger>
            <HoverCardContent side="top" align="center" sideOffset={8} className="w-72 rounded-xl p-3">
                <div className="mb-2 text-xs font-medium text-foreground">
                    {t('source.modelCount', { count: source.model_count })}
                </div>
                {models.length > 0 ? (
                    <div className="flex max-h-56 flex-wrap gap-1 overflow-y-auto">
                        {models.map((model) => (
                            <Badge key={model} variant="secondary" className="max-w-64 truncate px-1.5 text-[10px] font-normal">
                                {model}
                            </Badge>
                        ))}
                        {extraCount > 0 ? (
                            <Badge variant="secondary" className="px-1.5 text-[10px] font-normal">
                                {t('source.moreModels', { count: extraCount })}
                            </Badge>
                        ) : null}
                    </div>
                ) : (
                    <div className="text-xs text-muted-foreground">{t('source.noModels')}</div>
                )}
            </HoverCardContent>
        </HoverCard>
    );
}

function ChannelRow({
    source,
    mode,
    selected,
    onSelectedChange,
    onModeChange,
}: {
    source: GroupAutoGroupSource;
    mode: AutoGroupType;
    selected: boolean;
    onSelectedChange: (next: boolean) => void;
    onModeChange: (mode: AutoGroupType) => void;
}) {
    const t = useTranslations('group.autoGroup');
    const configured = mode !== AutoGroupType.None;

    return (
        <div
            role="group"
            aria-label={source.channel_name}
            className={cn(
                'mx-2 mb-1 flex h-8 items-center gap-2 rounded-lg border px-2 text-left transition-colors',
                selected
                    ? 'border-primary/40 bg-primary/5 hover:bg-primary/10'
                    : 'border-border/40 bg-background hover:bg-muted/60',
            )}
        >
            <TristateCheckbox
                state={selected ? 'checked' : 'unchecked'}
                onChange={onSelectedChange}
                ariaLabel={source.channel_name}
            />
            <span
                className={cn(
                    'min-w-0 flex-1 truncate text-xs',
                    configured ? 'font-medium text-foreground' : 'text-muted-foreground',
                )}
            >
                {source.channel_name}
            </span>
            {!source.enabled ? (
                <Badge variant="outline" className="h-5 px-1.5 text-[10px] text-muted-foreground">
                    {t('source.disabled')}
                </Badge>
            ) : null}
            <ModelPreview source={source} />
            <Select value={String(mode)} onValueChange={(value) => onModeChange(Number(value) as AutoGroupType)}>
                <SelectTrigger
                    size="sm"
                    className={cn(
                        '!h-6 w-auto min-w-18 justify-end rounded-md border-transparent bg-transparent px-1 py-0 text-xs shadow-none hover:text-primary focus-visible:border-transparent focus-visible:ring-0 dark:bg-transparent dark:hover:bg-transparent',
                        configured ? 'font-medium text-foreground' : 'text-muted-foreground',
                    )}
                >
                    <SelectValue />
                </SelectTrigger>
                <SelectContent className="rounded-xl">
                    {AUTO_GROUP_VALUES.map((value) => (
                        <SelectItem key={value} value={String(value)}>
                            {t(`mode.${modeKey(value)}`)}
                        </SelectItem>
                    ))}
                </SelectContent>
            </Select>
        </div>
    );
}

export function GroupAutoGroupDialogContent() {
    const t = useTranslations('group.autoGroup');
    const { setIsOpen } = useMorphingDialog();
    const { data: config, isLoading, error } = useGroupAutoGroupConfig();
    const updateConfig = useUpdateGroupAutoGroupConfig();
    const [keyword, setKeyword] = useState('');
    const [modes, setModes] = useState<Record<number, AutoGroupType>>({});
    const [expanded, setExpanded] = useState<Set<string>>(new Set());
    const [selection, setSelection] = useState<Set<number>>(new Set());

    useEffect(() => {
        if (!config) return;
        const next: Record<number, AutoGroupType> = {};
        for (const source of config.sources) {
            next[source.channel_id] = source.auto_group;
        }
        queueMicrotask(() => setModes(next));
    }, [config]);

    const normalizedKeyword = keyword.trim().toLowerCase();
    const sources = useMemo(() => config?.sources ?? [], [config?.sources]);
    const migrationPending = sources.some(source => source.managed);

    const groups = useMemo<SourceTreeGroup[]>(() => {
        const protocols = new Map<string, SourceTreeGroup>();
        for (const source of sources) {
            if (!matchesKeyword(source, normalizedKeyword)) continue;
            const protocol = source.endpoint_type?.trim() || t('otherProtocol');
            const group = protocols.get(protocol) ?? { key: `protocol:${protocol}`, label: protocol, sources: [] };
            group.sources.push(source);
            protocols.set(protocol, group);
        }
        return [...protocols.values()].sort((a, b) => a.label.localeCompare(b.label)).map(group => ({
            ...group, sources: group.sources.sort((a, b) => a.channel_name.localeCompare(b.channel_name)),
        }));
    }, [sources, normalizedKeyword, t]);

    const configuredCount = useMemo(
        () => sources.filter((s) => (modes[s.channel_id] ?? AutoGroupType.None) !== AutoGroupType.None).length,
        [sources, modes],
    );

    const dirtyItems = useMemo(() => {
        if (!config) return [];
        return sources
            .map((source) => ({
                channel_id: source.channel_id,
                auto_group: modes[source.channel_id] ?? AutoGroupType.None,
                original: source.auto_group,
            }))
            .filter((item) => item.auto_group !== item.original)
            .map(({ channel_id, auto_group }) => ({ channel_id, auto_group }));
    }, [config, modes, sources]);

    const shouldRunAfterSave = dirtyItems.some((item) => item.auto_group !== AutoGroupType.None);
    const hasChanges = dirtyItems.length > 0;
    const isPending = updateConfig.isPending;

    const toggleCollapsed = (key: string) => {
        setExpanded((current) => {
            const next = new Set(current);
            if (next.has(key)) next.delete(key);
            else next.add(key);
            return next;
        });
    };

    const toggleSelected = (channelId: number, next: boolean) => {
        setSelection((current) => {
            const updated = new Set(current);
            if (next) updated.add(channelId);
            else updated.delete(channelId);
            return updated;
        });
    };

    const setGroupSelection = (group: SourceTreeGroup, next: boolean) => {
        setSelection((current) => {
            const updated = new Set(current);
            for (const source of group.sources) {
                if (next) updated.add(source.channel_id);
                else updated.delete(source.channel_id);
            }
            return updated;
        });
    };

    const applyBulkMode = (mode: AutoGroupType) => {
        if (selection.size === 0) return;
        setModes((current) => {
            const updated = { ...current };
            for (const id of selection) updated[id] = mode;
            return updated;
        });
        setSelection(new Set());
    };

    const clearSelection = () => setSelection(new Set());

    const handleSave = () => {
        if (!config || migrationPending) return;
        updateConfig.mutate(
            {
                items: dirtyItems,
                run_now: shouldRunAfterSave,
            },
            {
                onSuccess: () => {
                    toast.success(shouldRunAfterSave ? t('toast.savedAndRun') : t('toast.saved'));
                    setIsOpen(false);
                },
                onError: (err) => toast.error(t('toast.saveFailed'), { description: err.message }),
            },
        );
    };

    return (
        <div className="relative flex h-full min-h-0 w-full max-w-full flex-col overflow-hidden">
            <MorphingDialogTitle className="shrink-0">
                <header className="relative mb-4 flex items-start justify-between gap-4">
                    <div className="space-y-3">
                        <div className="inline-flex items-center gap-2 rounded-full border border-primary/15 bg-card px-3 py-1 text-[0.68rem] font-semibold text-primary">
                            <WandSparkles className="size-3.5" />
                            {t('title')}
                        </div>
                        <div className="space-y-1">
                            <h2 className="text-2xl font-bold text-card-foreground">{t('title')}</h2>
                            <p className="text-sm text-muted-foreground">{t('description')}</p>
                        </div>
                    </div>
                    <MorphingDialogClose className="relative right-0 top-0" />
                </header>
            </MorphingDialogTitle>

            <MorphingDialogDescription className="flex min-h-0 flex-1 flex-col overflow-hidden">
                {error ? (
                    <div className="rounded-2xl border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
                        {t('loadFailed', { message: error.message })}
                    </div>
                ) : (
                    <>
                        {migrationPending && <p role="alert" className="mb-3 rounded-lg border border-amber-500/30 p-3 text-sm">{t('migrationRequired')}</p>}
                        <section className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-border/30 bg-card">
                            <div className="flex h-11 shrink-0 items-center gap-2 border-b border-border/20 px-4 py-3">
                                <span className="min-w-0 truncate text-sm font-medium text-foreground">
                                    {t('sections.channels')}
                                </span>
                                {configuredCount > 0 ? (
                                    <Badge variant="outline" className="h-5 border-primary/30 bg-primary/10 px-1.5 text-[10px] text-primary">
                                        {configuredCount}
                                    </Badge>
                                ) : null}
                                <div className="ml-auto flex items-center gap-2">
                                    <div className="relative w-40 sm:w-48">
                                        <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                                        <input
                                            value={keyword}
                                            onChange={(event) => setKeyword(event.target.value)}
                                            placeholder={t('searchPlaceholder')}
                                            className="h-8 w-full rounded-lg border border-border/35 bg-card pl-7 pr-2 text-xs shadow-none outline-none focus-visible:ring-2 focus-visible:ring-ring/20"
                                        />
                                    </div>
                                </div>
                            </div>
                            <AnimatePresence initial={false}>
                                {selection.size > 0 ? (
                                    <motion.div
                                        key="bulk-bar"
                                        initial={{ height: 0, opacity: 0 }}
                                        animate={{ height: 'auto', opacity: 1 }}
                                        exit={{ height: 0, opacity: 0 }}
                                        transition={{ duration: 0.22, ease: 'easeOut' }}
                                        className="overflow-hidden"
                                    >
                                        <div className="flex h-10 items-center gap-2 border-b border-primary/20 bg-primary/5 px-3 text-xs">
                                            <span className="font-medium text-primary">
                                                {t('bulk.selected', { count: selection.size })}
                                            </span>
                                            <Select onValueChange={(value) => applyBulkMode(Number(value) as AutoGroupType)}>
                                                <SelectTrigger
                                                    size="sm"
                                                    className="!h-7 ml-auto w-36 rounded-lg border-primary/30 bg-background text-xs"
                                                >
                                                    <SelectValue placeholder={t('bulk.placeholder')} />
                                                </SelectTrigger>
                                                <SelectContent className="rounded-xl">
                                                    {AUTO_GROUP_VALUES.map((value) => (
                                                        <SelectItem key={value} value={String(value)}>
                                                            {t(`mode.${modeKey(value)}`)}
                                                        </SelectItem>
                                                    ))}
                                                </SelectContent>
                                            </Select>
                                            <button
                                                type="button"
                                                onClick={clearSelection}
                                                className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                                                aria-label={t('bulk.clear')}
                                            >
                                                <X className="size-3.5" />
                                            </button>
                                        </div>
                                    </motion.div>
                                ) : null}
                            </AnimatePresence>
                            <div className="min-h-0 flex-1 overflow-y-auto rounded-b-xl">
                                {isLoading ? (
                                    <div className="p-6 text-center text-sm text-muted-foreground">{t('loading')}</div>
                                ) : groups.length === 0 ? (
                                    <div className="p-6 text-center text-sm text-muted-foreground">{t('emptyChannels')}</div>
                                ) : (
                                    groups.map((group) => {
                                        const isExpanded = expanded.has(group.key) || !!normalizedKeyword;
                                        const groupConfigured = group.sources.filter(
                                            (s) => (modes[s.channel_id] ?? AutoGroupType.None) !== AutoGroupType.None,
                                        ).length;
                                        const groupSelected = group.sources.filter((s) => selection.has(s.channel_id)).length;
                                        const groupState: 'unchecked' | 'partial' | 'checked' =
                                            groupSelected === 0
                                                ? 'unchecked'
                                                : groupSelected === group.sources.length
                                                    ? 'checked'
                                                    : 'partial';
                                        return (
                                            <div key={group.key} className="border-b border-border/40 last:border-b-0">
                                                <div className="mx-2 my-1 flex h-8 w-[calc(100%-1rem)] items-center gap-2 rounded-lg bg-muted px-2 transition-colors hover:bg-muted/80">
                                                    <TristateCheckbox
                                                        state={groupState}
                                                        onChange={(next) => setGroupSelection(group, next)}
                                                        ariaLabel={group.label}
                                                    />
                                                    <button
                                                        type="button"
                                                        onClick={() => toggleCollapsed(group.key)}
                                                        className="flex min-w-0 flex-1 items-center gap-2 text-left"
                                                    >
                                                        <ChevronDown
                                                            className={cn(
                                                                'size-3.5 shrink-0 text-muted-foreground transition-transform',
                                                                isExpanded ? '' : '-rotate-90',
                                                            )}
                                                        />
                                                        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-foreground">
                                                            {group.label}
                                                        </span>
                                                        {groupConfigured > 0 ? (
                                                            <span className="text-[10px] tabular-nums text-primary">
                                                                {groupConfigured}/{group.sources.length}
                                                            </span>
                                                        ) : (
                                                            <span className="text-[10px] tabular-nums text-muted-foreground">
                                                                {group.sources.length}
                                                            </span>
                                                        )}
                                                    </button>
                                                </div>
                                                <AnimatePresence initial={false}>
                                                    {isExpanded ? (
                                                        <motion.div
                                                            key="content"
                                                            initial={{ height: 0, opacity: 0 }}
                                                            animate={{ height: 'auto', opacity: 1 }}
                                                            exit={{ height: 0, opacity: 0 }}
                                                            transition={{ duration: 0.22, ease: 'easeOut' }}
                                                            className="overflow-hidden"
                                                        >
                                                            <div className="flex flex-col">
                                                                {group.sources.map((source) => (
                                                                    <ChannelRow
                                                                        key={source.channel_id}
                                                                        source={source}
                                                                        mode={modes[source.channel_id] ?? AutoGroupType.None}
                                                                        selected={selection.has(source.channel_id)}
                                                                        onSelectedChange={(next) =>
                                                                            toggleSelected(source.channel_id, next)
                                                                        }
                                                                        onModeChange={(mode) =>
                                                                            setModes((current) => ({
                                                                                ...current,
                                                                                [source.channel_id]: mode,
                                                                            }))
                                                                        }
                                                                    />
                                                                ))}
                                                            </div>
                                                        </motion.div>
                                                    ) : null}
                                                </AnimatePresence>
                                            </div>
                                        );
                                    })
                                )}
                            </div>
                        </section>

                        <div className="mt-4 flex shrink-0 flex-col gap-2 sm:flex-row">
                            <Button
                                type="button"
                                variant="secondary"
                                className="h-11 flex-1 rounded-lg"
                                onClick={() => setIsOpen(false)}
                                disabled={isPending}
                            >
                                {t('buttons.cancel')}
                            </Button>
                            <Button
                                type="button"
                                className="h-11 flex-1 rounded-lg"
                                onClick={handleSave}
                                disabled={isPending || isLoading || !hasChanges || migrationPending}
                            >
                                {updateConfig.isPending ? t('buttons.saving') : t('buttons.save')}
                            </Button>
                        </div>
                    </>
                )}
            </MorphingDialogDescription>
        </div>
    );
}
