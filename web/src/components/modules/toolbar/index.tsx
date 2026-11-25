'use client';

import { Suspense, useMemo, useState } from 'react';
import {
    ArrowDownZA,
    ArrowUpAZ,
    Clock3,
    LayoutGrid,
    List,
    LoaderCircle,
    Plus,
    RefreshCw,
    Search,
    SlidersHorizontal,
    WandSparkles,
    X
} from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import {
    MorphingDialog,
    MorphingDialogTrigger,
    MorphingDialogContainer,
    MorphingDialogContent,
} from '@/components/ui/morphing-dialog';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { buttonVariants } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import type { NavItem } from '@/components/modules/navbar';
import { useLogUIStore } from '@/components/modules/log/ui-store';
import { useTranslations } from 'next-intl';
import { lazyWithPreload } from '@/route/lazy-with-preload';
import { useSearchStore } from './search-store';
import { ToolbarMenu, type ToolbarAction } from './ToolbarMenu';
import {
    useToolbarViewOptionsStore,
    TOOLBAR_PAGES,
    type ToolbarPage,
    type ToolbarSortField,
    type ToolbarSortOrder,
} from './view-options-store';

const ChannelCreateContent = lazyWithPreload(() =>
    import('@/components/modules/channel/Create').then((module) => ({ default: module.CreateDialogContent }))
);
const GroupCreateContent = lazyWithPreload(() =>
    import('@/components/modules/group/Create').then((module) => ({ default: module.CreateDialogContent }))
);
const GroupAutoGroupDialogContent = lazyWithPreload(() =>
    import('@/components/modules/group/AutoGroupDialog').then((module) => ({ default: module.GroupAutoGroupDialogContent }))
);
const ModelCreateContent = lazyWithPreload(() =>
    import('@/components/modules/model/Create').then((module) => ({ default: module.CreateDialogContent }))
);
const LogFilterPopover = lazyWithPreload(() =>
    import('@/components/modules/log/FilterPopover').then((module) => ({ default: module.LogFilterPopover }))
);

type CombinedSortOption = {
    value: `${ToolbarSortField}-${ToolbarSortOrder}`;
    field: ToolbarSortField;
    order: ToolbarSortOrder;
    labelKey: string;
};

const COMBINED_SORT_OPTIONS: readonly CombinedSortOption[] = [
    { value: 'name-asc', field: 'name', order: 'asc', labelKey: 'popover.nameAsc' },
    { value: 'name-desc', field: 'name', order: 'desc', labelKey: 'popover.nameDesc' },
    { value: 'created-asc', field: 'created', order: 'asc', labelKey: 'popover.createdAsc' },
    { value: 'created-desc', field: 'created', order: 'desc', labelKey: 'popover.createdDesc' },
] as const;

function isToolbarPage(item: NavItem): item is ToolbarPage {
    return (TOOLBAR_PAGES as readonly NavItem[]).includes(item);
}

function CreateDialogContent({ activeItem }: { activeItem: ToolbarPage }) {
    switch (activeItem) {
        case 'channel':
            return <ChannelCreateContent />;
        case 'group':
            return <GroupCreateContent />;
        case 'model':
            return <ModelCreateContent />;
        case 'log':
            return null;
    }
}

export function Toolbar({ activeItem }: { activeItem: NavItem }) {
    const t = useTranslations('toolbar');
    const toolbarItem = isToolbarPage(activeItem) ? activeItem : null;
    const searchTerm = useSearchStore((s) => (toolbarItem ? s.searchTerms[toolbarItem] || '' : ''));
    const setSearchTerm = useSearchStore((s) => s.setSearchTerm);
    const layout = useToolbarViewOptionsStore((s) => (toolbarItem ? s.getLayout(toolbarItem) : 'grid'));
    const sortField = useToolbarViewOptionsStore((s) =>
        toolbarItem === 'channel' || toolbarItem === 'group' ? s.getSortField(toolbarItem) : 'name'
    );
    const sortOrder = useToolbarViewOptionsStore((s) => (toolbarItem ? s.getSortOrder(toolbarItem) : 'asc'));
    const setLayout = useToolbarViewOptionsStore((s) => s.setLayout);
    const setSortConfig = useToolbarViewOptionsStore((s) => s.setSortConfig);
    const setSortOrder = useToolbarViewOptionsStore((s) => s.setSortOrder);

    // Log actions
    const requestLogRefresh = useLogUIStore((s) => s.requestRefresh);
    const isLogRefreshing = useLogUIStore((s) => s.isRefreshing);

    const [expandedSearchItem, setExpandedSearchItem] = useState<ToolbarPage | null>(null);
    const [viewOptionsOpen, setViewOptionsOpen] = useState(false);
    const [createDialogOpen, setCreateDialogOpen] = useState(false);
    const [autoGroupDialogOpen, setAutoGroupDialogOpen] = useState(false);

    const searchExpanded = expandedSearchItem === toolbarItem;

    const isLogToolbar = toolbarItem === 'log';
    const showLayoutOptions = toolbarItem === 'channel' || toolbarItem === 'model';
    const showCombinedSortOptions = toolbarItem === 'channel' || toolbarItem === 'group';
    const showSortOptions = !isLogToolbar;

    // 构建工具栏按钮配置
    const actions = useMemo((): ToolbarAction[] => {
        const result: ToolbarAction[] = [];

        // 渠道页面按钮
        if (toolbarItem === 'channel') {
            result.push({
                id: 'create-channel',
                icon: <Plus className="size-4" />,
                label: '新增渠道',
                onClick: () => setCreateDialogOpen(true),
                priority: 'desktop',
            });
        }

        // 分组页面按钮
        if (toolbarItem === 'group') {
            result.push(
                {
                    id: 'auto-group',
                    icon: <WandSparkles className="size-4" />,
                    label: '自动分组',
                    onClick: () => setAutoGroupDialogOpen(true),
                    priority: 'large',
                },
                {
                    id: 'create-group',
                    icon: <Plus className="size-4" />,
                    label: '新增分组',
                    onClick: () => setCreateDialogOpen(true),
                    priority: 'desktop',
                }
            );
        }

        // 模型页面按钮
        if (toolbarItem === 'model') {
            result.push({
                id: 'create-model',
                icon: <Plus className="size-4" />,
                label: '新增模型',
                onClick: () => setCreateDialogOpen(true),
                priority: 'desktop',
            });
        }

        // 日志页面按钮
        if (toolbarItem === 'log') {
            result.push({
                id: 'refresh',
                icon: <RefreshCw className={cn('size-4', isLogRefreshing && 'animate-spin')} />,
                label: '刷新',
                onClick: requestLogRefresh,
                disabled: isLogRefreshing,
                priority: 'desktop',
            });
        }

        return result;
    }, [toolbarItem, isLogRefreshing, requestLogRefresh]);

    if (!toolbarItem) return null;

    return (
        <>
        <AnimatePresence mode="wait">
            <motion.div
                key="toolbar"
                initial={{ opacity: 0, scale: 0.9 }}
                animate={{ opacity: 1, scale: 1 }}
                exit={{ opacity: 0, scale: 0.9 }}
                transition={{ duration: 0.2 }}
                className="flex items-center gap-2"
            >
                {/* 搜索框 - 始终可见 */}
                <div className="relative h-9 w-9">
                    {!searchExpanded ? (
                        <motion.button
                            layoutId="search-box"
                            type="button"
                            aria-label={t('search')}
                            onClick={() => setExpandedSearchItem(toolbarItem)}
                            className={buttonVariants({
                                variant: 'ghost',
                                size: 'icon',
                                className:
                                    'absolute inset-0 rounded-xl transition-none hover:bg-transparent text-muted-foreground hover:text-foreground',
                            })}
                        >
                            <motion.span layout="position">
                                <Search className="size-4 transition-colors duration-300" />
                            </motion.span>
                        </motion.button>
                    ) : (
                        <motion.div
                            layoutId="search-box"
                            className="absolute right-0 top-0 flex items-center gap-2 h-9 px-3 rounded-xl border"
                            transition={{ type: 'spring', stiffness: 400, damping: 30 }}
                        >
                            <motion.span layout="position">
                                <Search className="size-4 text-muted-foreground shrink-0" />
                            </motion.span>
                            <input
                                type="text"
                                aria-label={t('search')}
                                value={searchTerm}
                                onChange={(e) => setSearchTerm(toolbarItem, e.target.value)}
                                autoFocus
                                className="w-20 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                            />
                            <button
                                type="button"
                                aria-label={t('clearSearch')}
                                onClick={() => {
                                    setSearchTerm(toolbarItem, '');
                                    setExpandedSearchItem(null);
                                }}
                                className="p-0.5 rounded shrink-0 text-muted-foreground hover:text-foreground transition-colors"
                            >
                                <X className="size-3.5" />
                            </button>
                        </motion.div>
                    )}
                </div>

                {/* 日志页面的筛选按钮 */}
                {isLogToolbar && (
                    <Suspense fallback={<div className="size-9" />}>
                        <LogFilterPopover />
                    </Suspense>
                )}

                {/* 设置按钮 - 始终可见（除了日志页面） */}
                {!isLogToolbar && (
                    <Popover open={viewOptionsOpen} onOpenChange={setViewOptionsOpen}>
                        <PopoverTrigger asChild>
                            <button
                                type="button"
                                aria-label={t('popover.ariaLabel')}
                                className={buttonVariants({
                                    variant: 'ghost',
                                    size: 'icon',
                                    className:
                                        'rounded-xl transition-none hover:bg-transparent text-muted-foreground hover:text-foreground',
                                })}
                            >
                                <SlidersHorizontal className="size-4 transition-colors duration-300" />
                            </button>
                        </PopoverTrigger>
                        <PopoverContent
                            align="center"
                            side="bottom"
                            sideOffset={8}
                            className="w-64 rounded-2xl border border-border/60 bg-card p-3 shadow-xl"
                        >
                            <div className="grid gap-3">
                                {showLayoutOptions && (
                                    <div className="grid gap-2">
                                        <p className="text-xs font-medium text-muted-foreground">
                                            {t('popover.layout')}
                                        </p>
                                        <div className="grid grid-cols-2 gap-2">
                                            <button
                                                type="button"
                                                onClick={() => setLayout(toolbarItem, 'grid')}
                                                className={cn(
                                                    'h-8 rounded-lg border text-xs font-medium inline-flex items-center justify-center gap-1.5 transition-colors',
                                                    layout === 'grid'
                                                        ? 'border-primary/30 bg-primary text-primary-foreground'
                                                        : 'border-border bg-muted/20 text-foreground hover:bg-muted/30'
                                                )}
                                            >
                                                <LayoutGrid className="size-3.5" />
                                                {t('popover.grid')}
                                            </button>
                                            <button
                                                type="button"
                                                onClick={() => setLayout(toolbarItem, 'list')}
                                                className={cn(
                                                    'h-8 rounded-lg border text-xs font-medium inline-flex items-center justify-center gap-1.5 transition-colors',
                                                    layout === 'list'
                                                        ? 'border-primary/30 bg-primary text-primary-foreground'
                                                        : 'border-border bg-muted/20 text-foreground hover:bg-muted/30'
                                                )}
                                            >
                                                <List className="size-3.5" />
                                                {t('popover.list')}
                                            </button>
                                        </div>
                                    </div>
                                )}

                                {showSortOptions && (
                                    <div className="grid gap-2">
                                        <p className="text-xs font-medium text-muted-foreground">
                                            {t('popover.sort')}
                                        </p>
                                        {showCombinedSortOptions ? (
                                            <div className="grid grid-cols-2 gap-2">
                                                {COMBINED_SORT_OPTIONS.map((option) => (
                                                    <button
                                                        key={option.value}
                                                        type="button"
                                                        onClick={() => {
                                                            if (
                                                                toolbarItem === 'channel' ||
                                                                toolbarItem === 'group'
                                                            ) {
                                                                setSortConfig(
                                                                    toolbarItem,
                                                                    option.field,
                                                                    option.order
                                                                );
                                                            }
                                                        }}
                                                        className={cn(
                                                            'h-8 rounded-lg border text-xs font-medium inline-flex items-center justify-center gap-1.5 transition-colors',
                                                            sortField === option.field &&
                                                                sortOrder === option.order
                                                                ? 'border-primary/30 bg-primary text-primary-foreground'
                                                                : 'border-border bg-muted/20 text-foreground hover:bg-muted/30'
                                                        )}
                                                    >
                                                        {option.field === 'name' ? (
                                                            option.order === 'desc' ? (
                                                                <ArrowDownZA className="size-3.5" />
                                                            ) : (
                                                                <ArrowUpAZ className="size-3.5" />
                                                            )
                                                        ) : (
                                                            <Clock3 className="size-3.5" />
                                                        )}
                                                        {t(option.labelKey)}
                                                    </button>
                                                ))}
                                            </div>
                                        ) : (
                                            <div className="grid grid-cols-2 gap-2">
                                                <button
                                                    type="button"
                                                    onClick={() => setSortOrder(toolbarItem, 'asc')}
                                                    className={cn(
                                                        'h-8 rounded-lg border text-xs font-medium inline-flex items-center justify-center gap-1.5 transition-colors',
                                                        sortOrder === 'asc'
                                                            ? 'border-primary/30 bg-primary text-primary-foreground'
                                                            : 'border-border bg-muted/20 text-foreground hover:bg-muted/30'
                                                    )}
                                                >
                                                    <ArrowUpAZ className="size-3.5" />
                                                    {t('popover.nameAsc')}
                                                </button>
                                                <button
                                                    type="button"
                                                    onClick={() => setSortOrder(toolbarItem, 'desc')}
                                                    className={cn(
                                                        'h-8 rounded-lg border text-xs font-medium inline-flex items-center justify-center gap-1.5 transition-colors',
                                                        sortOrder === 'desc'
                                                            ? 'border-primary/30 bg-primary text-primary-foreground'
                                                            : 'border-border bg-muted/20 text-foreground hover:bg-muted/30'
                                                    )}
                                                >
                                                    <ArrowDownZA className="size-3.5" />
                                                    {t('popover.nameDesc')}
                                                </button>
                                            </div>
                                        )}
                                    </div>
                                )}

                            </div>
                        </PopoverContent>
                    </Popover>
                )}

                {/* 统一的工具按钮菜单（新增 + 按钮位于最右侧） */}
                <ToolbarMenu actions={actions} />
            </motion.div>
        </AnimatePresence>

            {/* 对话框通过 portal 渲染，统一包在隐藏容器中（display:none 不参与 flex 布局），
                避免其触发器外层 div 作为 flex 子项在工具栏右侧产生逐页不同的间隔 */}
            <div className="hidden">
                {/* 创建对话框 (channel/group/model) */}
                {toolbarItem !== 'log' && (
                    <MorphingDialog open={createDialogOpen} onOpenChange={setCreateDialogOpen}>
                        <MorphingDialogTrigger>
                            <button type="button" className="hidden">
                                Hidden trigger
                            </button>
                        </MorphingDialogTrigger>
                        <MorphingDialogContainer>
                            <MorphingDialogContent className={cn(
                                'max-w-full bg-card text-card-foreground flex flex-col overflow-hidden',
                                toolbarItem === 'group'
                                    ? 'h-[calc(100dvh-2rem)] w-[min(100vw-2rem,92rem)] rounded-xl border border-border/35 px-4 py-4 shadow-md md:h-[calc(100dvh-3rem)] md:px-6'
                                    : 'max-h-[calc(100vh-2rem)] w-fit rounded-xl px-6 py-4 shadow-sm'
                            )}>
                                {createDialogOpen ? (
                                    <Suspense fallback={<div className="grid min-h-32 min-w-64 place-items-center"><LoaderCircle className="size-5 animate-spin text-muted-foreground" /></div>}>
                                        <CreateDialogContent activeItem={toolbarItem} />
                                    </Suspense>
                                ) : null}
                            </MorphingDialogContent>
                        </MorphingDialogContainer>
                    </MorphingDialog>
                )}

                {/* 自动分组对话框 */}
                {toolbarItem === 'group' && (
                    <MorphingDialog open={autoGroupDialogOpen} onOpenChange={setAutoGroupDialogOpen}>
                        <MorphingDialogTrigger>
                            <button type="button" className="hidden">
                                Hidden trigger
                            </button>
                        </MorphingDialogTrigger>
                        <MorphingDialogContainer>
                            <MorphingDialogContent className="flex h-[calc(100dvh-2rem)] w-[min(100vw-2rem,52rem)] max-w-full flex-col overflow-hidden rounded-xl border border-border/35 bg-card px-4 py-4 text-card-foreground shadow-md md:h-[calc(100dvh-3rem)] md:px-6">
                                {autoGroupDialogOpen ? (
                                    <Suspense fallback={<div className="grid min-h-32 place-items-center"><LoaderCircle className="size-5 animate-spin text-muted-foreground" /></div>}>
                                        <GroupAutoGroupDialogContent />
                                    </Suspense>
                                ) : null}
                            </MorphingDialogContent>
                        </MorphingDialogContainer>
                    </MorphingDialog>
                )}
            </div>
        </>
    );
}

export { useSearchStore } from './search-store';
export { useToolbarViewOptionsStore } from './view-options-store';
