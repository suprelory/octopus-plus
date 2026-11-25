'use client';

import { useCallback, useMemo, useState, type FormEvent } from 'react';
import { Check, ChevronDownIcon, FlaskConical, Plus, Search, SlidersHorizontal, Sparkles, Trash2, Waves } from 'lucide-react';
import { useTranslations } from 'next-intl';
import * as AccordionPrimitive from '@radix-ui/react-accordion';
import { useModelChannelList, type LLMChannel } from '@/api/endpoints/model';
import { Button } from '@/components/ui/button';
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Accordion, AccordionContent, AccordionItem } from '@/components/ui/accordion';
import { cn } from '@/lib/utils';
import { getModelIcon } from '@/lib/model-icons';
import { GroupMode } from '@/api/endpoints/group';
import type { SelectedMember } from './ItemList';
import { MemberList } from './ItemList';
import { matchesGroupName, memberKey, normalizeKey, MODE_LABELS } from './utils';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { HelpCircle } from 'lucide-react';



export type GroupEditorValues = {
    name: string;
    match_regex: string;
    mode: GroupMode;
    first_token_time_out: number;
    session_keep_time: number;
    retry_enabled: boolean;
    max_retries: number;
    members: SelectedMember[];
};

function ModelPickerSection({
    modelChannels,
    selectedMembers,
    onAdd,
    onAutoAdd,
    autoAddDisabled,
}: {
    modelChannels: LLMChannel[];
    selectedMembers: SelectedMember[];
    onAdd: (channel: LLMChannel) => void;
    onAutoAdd: () => void;
    autoAddDisabled: boolean;
}) {
    const t = useTranslations('group');
    const [searchKeyword, setSearchKeyword] = useState('');

    const selectedKeys = useMemo(() => new Set(selectedMembers.map(memberKey)), [selectedMembers]);
    const normalizedSearch = searchKeyword.trim().toLowerCase();

    const channels = useMemo(() => {
        const byId = new Map<number, { id: number; name: string; models: LLMChannel[] }>();
        modelChannels.forEach((mc) => {
            const existing = byId.get(mc.channel_id);
            if (existing) existing.models.push(mc);
            else byId.set(mc.channel_id, { id: mc.channel_id, name: mc.channel_name, models: [mc] });
        });

        return Array.from(byId.values())
            .map((c) => ({ ...c, models: [...c.models].sort((a, b) => a.name.localeCompare(b.name)) }))
            .sort((a, b) => a.id - b.id);
    }, [modelChannels]);

    const filteredChannels = useMemo(() => {
        if (!normalizedSearch) return channels;
        return channels.reduce<typeof channels>((acc, channel) => {
            if (channel.name.toLowerCase().includes(normalizedSearch)) {
                acc.push(channel);
                return acc;
            }

            const models = channel.models.filter((model) => model.name.toLowerCase().includes(normalizedSearch));
            if (models.length > 0) acc.push({ ...channel, models });
            return acc;
        }, []);
    }, [channels, normalizedSearch]);

    return (
        <div className="flex min-h-[28rem] flex-col rounded-lg border border-border/30 bg-card lg:min-h-0">
            <div className="grid grid-cols-[1fr_auto_1fr] items-center gap-2 border-b border-border/20 px-4 py-3">
                <span className="min-w-0 justify-self-start text-sm font-medium text-foreground">
                    {t('form.addItem')}
                </span>

                <div className="relative w-32 justify-self-center sm:w-40">
                    <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <Input
                        value={searchKeyword}
                        onChange={(event) => setSearchKeyword(event.target.value)}
                        className="h-8 rounded-lg border-border/35 bg-card pl-7 pr-2 text-xs shadow-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/20"
                        aria-label="search"
                    />
                </div>

                <button
                    type="button"
                    onClick={onAutoAdd}
                    className={cn(
                        'justify-self-end shrink-0 flex items-center gap-1 px-2 py-1 rounded-lg text-xs font-medium transition-colors',
                        autoAddDisabled
                            ? 'text-muted-foreground/50 cursor-not-allowed'
                            : 'hover:bg-muted text-muted-foreground hover:text-foreground'
                    )}
                    disabled={autoAddDisabled}
                    title={t('form.autoAdd')}
                >
                    <Sparkles className="size-3.5" />
                    <span>{t('form.autoAdd')}</span>
                </button>
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto p-3 max-md:max-h-[28rem]">
                <Accordion type="multiple" className="w-full space-y-2">
                    {filteredChannels.map((channel) => {
                        const total = channel.models.length;
                        const selectedCount = channel.models.reduce(
                            (acc, m) => acc + (selectedKeys.has(memberKey(m)) ? 1 : 0),
                            0
                        );
                        const available = total - selectedCount;

                        return (
                            <AccordionItem key={channel.id} value={`channel-${channel.id}`}>
                                <AccordionPrimitive.Header className="sticky top-0 z-10 flex overflow-hidden rounded-lg border border-border/25 bg-card px-3">
                                    <AccordionPrimitive.Trigger className="flex min-w-0 flex-1 items-center gap-4 py-3.5 text-left text-sm transition-all outline-none focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-50 [&[data-state=open]>svg]:rotate-180">
                                        <span className="truncate">{channel.name}</span>
                                        <span className="text-xs text-muted-foreground shrink-0">
                                            {available}/{total}
                                        </span>
                                        <ChevronDownIcon className="text-muted-foreground pointer-events-none size-4 shrink-0 transition-transform duration-200" />
                                    </AccordionPrimitive.Trigger>
                                </AccordionPrimitive.Header>
                                <AccordionContent className="px-1 pt-2">
                                    <div className="flex flex-col gap-1.5">
                                        {channel.models.map((m) => {
                                            const isSelected = selectedKeys.has(memberKey(m));
                                            const { Avatar } = getModelIcon(m.name);
                                            const protocol = m.endpoint_type?.trim();
                                            return (
                                                <button
                                                    key={memberKey(m)}
                                                    type="button"
                                                    onClick={() => !isSelected && onAdd(m)}
                                                    disabled={isSelected}
                                                    className={cn(
                                                        'flex w-full items-center justify-between gap-2 rounded-lg border border-border/30 bg-card px-2.5 py-2 text-left transition-colors',
                                                        isSelected ? 'cursor-not-allowed opacity-60' : 'hover:border-primary/15 hover:bg-muted/20'
                                                    )}
                                                >
                                                    <span className="flex items-center gap-2 min-w-0">
                                                        <Avatar size={16} />
                                                        <span className="min-w-0 flex flex-col">
                                                            <span className="text-sm font-medium truncate">{m.name}</span>
                                                            {protocol && <span className="text-[10px] text-muted-foreground truncate">{protocol}</span>}
                                                        </span>
                                                    </span>

                                                    <span className="shrink-0 text-muted-foreground">
                                                        {isSelected ? (
                                                            <Check className="size-4 text-primary" />
                                                        ) : (
                                                            <Plus className="size-4" />
                                                        )}
                                                    </span>
                                                </button>
                                            );
                                        })}
                                    </div>
                                </AccordionContent>
                            </AccordionItem>
                        );
                    })}
                </Accordion>
            </div>
        </div>
    );
}

function SortSection({
    members,
    onReorder,
    onRemove,
    onWeightChange,
    removingIds,
    showWeight,
    onClear,
}: {
    members: SelectedMember[];
    onReorder: (members: SelectedMember[]) => void;
    onRemove: (id: string) => void;
    onWeightChange: (id: string, weight: number) => void;
    removingIds: Set<string>;
    showWeight: boolean;
    onClear: () => void;
}) {
    const t = useTranslations('group');

    return (
        <div className="flex min-h-[28rem] flex-col rounded-lg border border-border/30 bg-card lg:min-h-0">
            <div className="flex items-center justify-between border-b border-border/20 px-4 py-3">
                <span className="text-sm font-medium text-foreground">
                    {t('form.items')}
                    {members.length > 0 && (
                        <span className="ml-1.5 text-xs text-muted-foreground font-normal">
                            ({members.length})
                        </span>
                    )}
                </span>
                <button
                    type="button"
                    onClick={onClear}
                    disabled={members.length === 0}
                    className={cn(
                        'flex items-center gap-1 px-2 py-1 rounded-lg text-xs font-medium transition-colors',
                        members.length === 0
                            ? 'text-muted-foreground/50 cursor-not-allowed'
                            : 'hover:bg-muted text-muted-foreground hover:text-foreground'
                    )}
                    title={t('form.clear')}
                >
                    <Trash2 className="size-3.5" />
                    <span>{t('form.clear')}</span>
                </button>
            </div>

            <div className="flex-1 min-h-0">
                <MemberList
                    members={members}
                    onReorder={onReorder}
                    onRemove={onRemove}
                    onWeightChange={onWeightChange}
                    removingIds={removingIds}
                    showWeight={showWeight}
                    showConfirmDelete={false}
                />
            </div>
        </div>
    );
}

export function GroupEditor({
    initial,
    submitText,
    submittingText,
    isSubmitting,
    onSubmit,
    onCancel,
    nameLabel,
    className,
}: {
    initial?: Partial<GroupEditorValues>;
    submitText: string;
    submittingText: string;
    isSubmitting: boolean;
    onSubmit: (values: GroupEditorValues) => void;
    onCancel?: () => void;
    nameLabel?: string;
    className?: string;
}) {
    const t = useTranslations('group');
    const { data: modelChannels = [] } = useModelChannelList();

    const [groupName, setGroupName] = useState(initial?.name ?? '');
    const [matchRegex, setMatchRegex] = useState(initial?.match_regex ?? '');
    const [mode, setMode] = useState<GroupMode>(initial?.mode ?? GroupMode.RoundRobin);
    const [firstTokenTimeOut, setFirstTokenTimeOut] = useState<number>(initial?.first_token_time_out ?? 0);
    const [sessionKeepTime, setSessionKeepTime] = useState<number>(initial?.session_keep_time ?? 0);
    const [retryEnabled, setRetryEnabled] = useState<boolean>(initial?.retry_enabled ?? false);
    const [maxRetries, setMaxRetries] = useState<number>(initial?.max_retries ?? 3);
    const [selectedMembers, setSelectedMembers] = useState<SelectedMember[]>(initial?.members ?? []);
    const [removingIds, setRemovingIds] = useState<Set<string>>(new Set());

    const groupKey = normalizeKey(groupName);
    const regexKey = matchRegex.trim();

    const { matchedModelChannels, regexError } = useMemo(() => {
        const parseRegex = (input: string): RegExp => {
            const inlineMatch = input.match(/^\(\?([ism]+)\)(.+)$/);
            if (inlineMatch) {
                const flagMap: Record<string, string> = { i: 'i', s: 's', m: 'm' };
                const flags = inlineMatch[1].split('').map(f => flagMap[f] || '').join('');
                return new RegExp(inlineMatch[2], flags);
            }

            return new RegExp(input);
        };

        if (regexKey) {
            try {
                const re = parseRegex(regexKey);
                return { matchedModelChannels: modelChannels.filter((mc) => re.test(mc.name)), regexError: '' };
            } catch (e) {
                return { matchedModelChannels: [], regexError: (e as Error)?.message ?? 'Invalid regex' };
            }
        }
        if (!groupKey) return { matchedModelChannels: [], regexError: '' };
        return { matchedModelChannels: modelChannels.filter((mc) => matchesGroupName(mc.name, groupKey)), regexError: '' };
    }, [groupKey, regexKey, modelChannels]);

    const handleAddMember = useCallback((channel: LLMChannel) => {
        const key = memberKey(channel);
        setSelectedMembers((prev) => {
            if (prev.some((m) => m.id === key)) return prev;
            return [...prev, { ...channel, id: key, weight: 1 }];
        });
    }, []);

    const autoAddDisabled = useMemo(() => {
        if ((!regexKey && !groupKey) || regexError || matchedModelChannels.length === 0) return true;
        const existing = new Set(selectedMembers.map((m) => m.id));
        return matchedModelChannels.every((mc) => existing.has(memberKey(mc)));
    }, [groupKey, regexKey, regexError, matchedModelChannels, selectedMembers]);

    const handleAutoAdd = useCallback(() => {
        if (matchedModelChannels.length === 0) return;
        setSelectedMembers((prev) => {
            const existing = new Set(prev.map((m) => m.id));
            const toAdd = matchedModelChannels
                .filter((mc) => !existing.has(memberKey(mc)))
                .map((mc) => ({ ...mc, id: memberKey(mc), weight: 1 }));
            return toAdd.length ? [...prev, ...toAdd] : prev;
        });
    }, [matchedModelChannels]);

    const handleWeightChange = useCallback((id: string, weight: number) => {
        setSelectedMembers((prev) => prev.map((m) => m.id === id ? { ...m, weight } : m));
    }, []);

    const handleRemoveMember = useCallback((id: string) => {
        setRemovingIds((prev) => new Set(prev).add(id));
        setTimeout(() => {
            setSelectedMembers((prev) => prev.filter((m) => m.id !== id));
            setRemovingIds((prev) => { const n = new Set(prev); n.delete(id); return n; });
        }, 200);
    }, []);

    const handleClearMembers = useCallback(() => {
        setSelectedMembers([]);
        setRemovingIds(new Set());
    }, []);

    const isValid = groupKey.length > 0 && selectedMembers.length > 0 && !regexError;

    const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        if (!isValid) return;
        onSubmit({
            name: groupName,
            match_regex: regexKey,
            mode,
            first_token_time_out: firstTokenTimeOut,
            session_keep_time: sessionKeepTime,
            retry_enabled: retryEnabled,
            max_retries: maxRetries,
            members: selectedMembers,
        });
    };


    return (
        <form onSubmit={handleSubmit} className={cn('flex h-full min-h-0 flex-col overflow-hidden', className)}>
            <div className="flex-1 min-h-0 overflow-y-auto pr-1">
                <FieldGroup className="flex min-h-full flex-col gap-4 lg:h-full">
                    <div className="grid min-h-full gap-4 2xl:grid-cols-[minmax(21rem,0.9fr)_minmax(0,1.55fr)] 2xl:items-stretch">
                        <section className="flex flex-col gap-4 rounded-xl border border-border/30 bg-card p-3 md:p-5">
                            <div className="flex flex-wrap items-center justify-between gap-3">
                                <div className="space-y-2">
                                    <div className="inline-flex items-center gap-2 rounded-full border border-primary/15 bg-card px-3 py-1 text-[0.68rem] font-semibold text-primary">
                                        <Waves className="size-3.5" />
                                        {nameLabel ?? t('form.name')}
                                    </div>
                                    <p className="text-sm text-muted-foreground">{t('emptyState.description')}</p>
                                </div>
                                <div className="inline-flex items-center gap-2 rounded-full border border-border/25 bg-card px-3 py-1 text-xs text-muted-foreground">
                                    <SlidersHorizontal className="size-3.5" />
                                    {t(`mode.${MODE_LABELS[mode]}`)}
                                </div>
                            </div>

                            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 md:gap-4">
                        <Field>
                            <FieldLabel htmlFor="group-name">{nameLabel ?? t('form.name')}</FieldLabel>
                            <Input
                                id="group-name"
                                value={groupName}
                                onChange={(e) => setGroupName(e.target.value)}
                                className="h-10 rounded-lg text-sm md:h-11"
                            />
                        </Field>
                        <Field>
                            <FieldLabel htmlFor="group-match-regex">{t('form.matchRegex')}</FieldLabel>
                            <Input
                                id="group-match-regex"
                                value={matchRegex}
                                onChange={(e) => setMatchRegex(e.target.value)}
                                className="h-10 rounded-lg text-sm md:h-11"
                                placeholder={t('form.matchRegexPlaceholder')}
                            />
                            {regexError && (
                                <p className="mt-1 text-xs text-destructive">
                                    {t('form.matchRegexInvalid')}: {regexError}
                                </p>
                            )}
                        </Field>

                        <Field>
                            <FieldLabel htmlFor="group-first-token-time-out">
                                {t('form.firstTokenTimeOut')}
                                <TooltipProvider>
                                    <Tooltip>
                                        <TooltipTrigger asChild>
                                            <HelpCircle className="size-4 text-muted-foreground cursor-help" />
                                        </TooltipTrigger>
                                        <TooltipContent>
                                            {t('form.firstTokenTimeOutHint')}
                                        </TooltipContent>
                                    </Tooltip>
                                </TooltipProvider>
                            </FieldLabel>
                            <Input
                                id="group-first-token-time-out"
                                type="number"
                                inputMode="numeric"
                                min={0}
                                step={1}
                                value={String(firstTokenTimeOut)}
                                onChange={(e) => {
                                    const raw = e.target.value;
                                    if (raw.trim() === '') {
                                        setFirstTokenTimeOut(0);
                                        return;
                                    }
                                    const n = Number.parseInt(raw, 10);
                                    setFirstTokenTimeOut(Number.isFinite(n) && n > 0 ? n : 0);
                                }}
                                className="h-10 rounded-lg text-sm md:h-11"
                            />
                        </Field>

                        <Field>
                            <FieldLabel htmlFor="group-session-keep-time">
                                {t('form.sessionKeepTime')}
                                <TooltipProvider>
                                    <Tooltip>
                                        <TooltipTrigger asChild>
                                            <HelpCircle className="size-4 text-muted-foreground cursor-help" />
                                        </TooltipTrigger>
                                        <TooltipContent>
                                            {t('form.sessionKeepTimeHint')}
                                        </TooltipContent>
                                    </Tooltip>
                                </TooltipProvider>
                            </FieldLabel>
                            <Input
                                id="group-session-keep-time"
                                type="number"
                                inputMode="numeric"
                                min={0}
                                step={1}
                                value={String(sessionKeepTime)}
                                onChange={(e) => {
                                    const raw = e.target.value;
                                    if (raw.trim() === '') {
                                        setSessionKeepTime(0);
                                        return;
                                    }
                                    const n = Number.parseInt(raw, 10);
                                    setSessionKeepTime(Number.isFinite(n) && n > 0 ? n : 0);
                                }}
                                className="h-10 rounded-lg text-sm md:h-11"
                            />
                        </Field>
                            </div>

                            <div className="space-y-2">
                                <div className="inline-flex items-center gap-2 rounded-full border border-border/25 bg-card px-3 py-1 text-[0.68rem] font-semibold text-muted-foreground">
                                    <Sparkles className="size-3.5" />
                                    {t(`mode.${MODE_LABELS[mode]}`)}
                                </div>
                                <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3 md:gap-2">
                            {([GroupMode.RoundRobin, GroupMode.Failover, GroupMode.Weighted] as const).map((m) => (
                                <button
                                    key={m}
                                    type="button"
                                    onClick={() => setMode(m)}
                                    className={cn(
                                        'rounded-lg border px-2 py-1.5 text-[10px] font-medium transition-[transform,border-color,background-color] duration-300 md:px-3 md:py-2 md:text-xs',
                                        mode === m
                                            ? 'border-primary/20 bg-primary text-primary-foreground'
                                            : 'border-border/30 bg-card text-foreground hover:-translate-y-0.5 hover:border-primary/20 hover:bg-muted/20'
                                    )}
                                >
                                    {t(`mode.${MODE_LABELS[m]}`)}
                                </button>
                            ))}
                                </div>
                            </div>

                            <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border/25 bg-muted/10 px-3 py-2.5">
                        <TooltipProvider>
                            <Tooltip>
                                <TooltipTrigger asChild>
                                    <label className="flex shrink-0 cursor-pointer items-center gap-2">
                                        <Switch
                                            checked={retryEnabled}
                                            onCheckedChange={setRetryEnabled}
                                        />
                                        <span className="text-sm text-muted-foreground">{t('form.retryEnabled')}</span>
                                    </label>
                                </TooltipTrigger>
                                <TooltipContent>
                                    {t('form.retryEnabledHint')}
                                </TooltipContent>
                            </Tooltip>
                        </TooltipProvider>
                        {retryEnabled && (
                            <TooltipProvider>
                                <Tooltip>
                                    <TooltipTrigger asChild>
                                        <label className="flex shrink-0 items-center gap-2">
                                            <Input
                                                type="number"
                                                inputMode="numeric"
                                                min={1}
                                                step={1}
                                                value={String(maxRetries)}
                                                onChange={(e) => {
                                                    const raw = e.target.value;
                                                    if (raw.trim() === '') { setMaxRetries(1); return; }
                                                    const n = Number.parseInt(raw, 10);
                                                    setMaxRetries(Number.isFinite(n) && n > 0 ? n : 1);
                                                }}
                                                className="h-8 w-16 rounded-lg text-center text-xs"
                                            />
                                            <span className="text-xs text-muted-foreground">{t('form.maxRetries')}</span>
                                        </label>
                                    </TooltipTrigger>
                                    <TooltipContent>
                                        {t('form.maxRetriesHint')}
                                    </TooltipContent>
                                </Tooltip>
                            </TooltipProvider>
                        )}
                            </div>
                        </section>

                        <section className="flex min-h-[34rem] min-w-0 flex-col gap-4 rounded-xl border border-border/30 bg-card p-3 md:p-5 xl:min-h-0">
                            <div className="flex flex-wrap items-center justify-between gap-3">
                                <div className="space-y-2">
                                    <div className="inline-flex items-center gap-2 rounded-full border border-primary/15 bg-card px-3 py-1 text-[0.68rem] font-semibold text-primary">
                                        <FlaskConical className="size-3.5" />
                                        {t('form.items')}
                                    </div>
                                    <p className="text-sm text-muted-foreground">{t('card.empty')}</p>
                                </div>
                                <div className="inline-flex items-center gap-2 rounded-full border border-border/25 bg-card px-3 py-1 text-xs text-muted-foreground">
                                    {selectedMembers.length}
                                </div>
                            </div>

                            <div className="grid min-w-0 grid-cols-1 gap-3 xl:flex-1 xl:min-h-0 2xl:grid-cols-[minmax(18rem,0.92fr)_minmax(20rem,1.18fr)] 2xl:gap-4">
                            <ModelPickerSection
                                modelChannels={modelChannels}
                                selectedMembers={selectedMembers}
                                onAdd={handleAddMember}
                                onAutoAdd={handleAutoAdd}
                                autoAddDisabled={autoAddDisabled}
                            />
                            <SortSection
                                members={selectedMembers}
                                onReorder={setSelectedMembers}
                                onRemove={handleRemoveMember}
                                onWeightChange={handleWeightChange}
                                removingIds={removingIds}
                                showWeight={mode === GroupMode.Weighted}
                                onClear={handleClearMembers}
                            />
                            </div>
                        </section>
                    </div>
                </FieldGroup>
            </div>

            <div className="mt-auto shrink-0 px-1 pt-4 pb-[calc(env(safe-area-inset-bottom)+0.5rem)]">
                <div className="flex gap-2">
                    {onCancel && (
                        <Button type="button" variant="secondary" className="h-11 flex-1 rounded-lg" onClick={onCancel}>
                            {t('detail.actions.cancel')}
                        </Button>
                    )}
                    <Button
                        type="submit"
                        disabled={!isValid || isSubmitting}
                        className="h-11 flex-1 rounded-lg"
                    >
                        {isSubmitting ? submittingText : submitText}
                    </Button>
                </div>
            </div>
        </form>
    );
}
