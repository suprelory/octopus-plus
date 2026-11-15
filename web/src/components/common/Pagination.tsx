'use client';

import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { cn } from '@/lib/utils';

const DEFAULT_PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;

type PageToken = number | 'ellipsis';

/**
 * 生成页码序列：首尾页常驻，中间用省略号收敛，最多渲染 4 个数字按钮。
 * 页数较少时全部平铺，避免出现"1 ... 2"这种反而更长的形态。
 */
function getPageNumbers(currentPage: number, totalPages: number): PageToken[] {
    const maxVisible = 4;
    if (totalPages <= maxVisible) {
        return Array.from({ length: totalPages }, (_, index) => index + 1);
    }
    if (currentPage <= 2) {
        return [1, 2, 'ellipsis', totalPages];
    }
    if (currentPage >= totalPages - 1) {
        return [1, 'ellipsis', totalPages - 1, totalPages];
    }
    return [1, 'ellipsis', currentPage, 'ellipsis', totalPages];
}

const navButtonClass =
    'inline-flex size-8 shrink-0 items-center justify-center rounded-lg border border-border bg-muted/20 text-muted-foreground transition-colors hover:bg-muted/40 hover:text-foreground disabled:pointer-events-none disabled:opacity-40';

interface PaginationProps {
    page: number;
    pageSize: number;
    total: number;
    /**
     * total 是否为精确值。后端对超大表做有界计数时只回下界（见
     * relayLogTotalMaxExact），此时显示 "10000+"，"下一页"由 hasMore 控制，
     * "跳到末页"因为末页未知而禁用。默认 true。
     */
    totalExact?: boolean;
    /** Whether another page exists when totalExact is false. */
    hasMore?: boolean;
    pageSizeOptions?: readonly number[];
    disabled?: boolean;
    onPageChange: (page: number) => void;
    onPageSizeChange: (pageSize: number) => void;
    className?: string;
}

export function Pagination({
    page,
    pageSize,
    total,
    totalExact = true,
    hasMore = false,
    pageSizeOptions = DEFAULT_PAGE_SIZE_OPTIONS,
    disabled = false,
    onPageChange,
    onPageSizeChange,
    className,
}: PaginationProps) {
    const t = useTranslations('common.pagination');
    const knownPages = Math.max(1, Math.ceil(total / pageSize));
    // total 是下界时页数也只是下界：不要把当前页夹回去，也不要禁用"下一页"。
    const currentPage = totalExact
        ? Math.min(Math.max(1, page), knownPages)
        : Math.max(1, page);
    const totalPages = Math.max(knownPages, currentPage);
    const canPrev = currentPage > 1 && !disabled;
    const canNext = (totalExact ? currentPage < knownPages : hasMore) && !disabled;
    const canJumpLast = totalExact && currentPage < knownPages && !disabled;
    const pageNumbers = getPageNumbers(currentPage, totalPages);

    return (
        <div className={cn('flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-2', className)}>
            <div className="flex items-baseline gap-1.5 text-xs">
                <span className="text-muted-foreground">{t('total')}</span>
                <span className="font-medium tabular-nums text-foreground">
                    {total.toLocaleString()}
                    {totalExact ? '' : '+'}
                </span>
            </div>

            <div className="flex items-center gap-2 sm:gap-3">
                <div className="hidden items-center gap-1.5 sm:flex">
                    <span className="text-xs text-muted-foreground">{t('rowsPerPage')}</span>
                    <Select
                        value={String(pageSize)}
                        onValueChange={(value) => onPageSizeChange(Number(value))}
                        disabled={disabled}
                    >
                        <SelectTrigger size="sm" className="w-[4.5rem] rounded-lg tabular-nums">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent align="end">
                            {pageSizeOptions.map((option) => (
                                <SelectItem key={option} value={String(option)} className="tabular-nums">
                                    {option}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </div>

                <div className="flex items-center gap-1">
                    <button
                        type="button"
                        aria-label={t('firstPage')}
                        title={t('firstPage')}
                        disabled={!canPrev}
                        onClick={() => onPageChange(1)}
                        className={cn(navButtonClass, 'hidden lg:inline-flex')}
                    >
                        <ChevronsLeft className="size-4" />
                    </button>
                    <button
                        type="button"
                        aria-label={t('prevPage')}
                        title={t('prevPage')}
                        disabled={!canPrev}
                        onClick={() => onPageChange(currentPage - 1)}
                        className={navButtonClass}
                    >
                        <ChevronLeft className="size-4" />
                    </button>

                    {/* 窄屏用"当前/总页数"替代页码按钮，避免工具条换行挤压列表 */}
                    <span className="px-1.5 text-xs tabular-nums text-muted-foreground sm:hidden">
                        {t('pageIndicator', { current: currentPage, total: totalPages })}
                    </span>

                    <div className="hidden items-center gap-1 sm:flex">
                        {pageNumbers.map((token, index) =>
                            token === 'ellipsis' ? (
                                <span
                                    key={`ellipsis-${index}`}
                                    className="px-0.5 text-xs text-muted-foreground/60"
                                >
                                    …
                                </span>
                            ) : (
                                <button
                                    key={token}
                                    type="button"
                                    aria-label={t('gotoPage', { page: token })}
                                    aria-current={token === currentPage ? 'page' : undefined}
                                    disabled={disabled}
                                    onClick={() => onPageChange(token)}
                                    className={cn(
                                        'inline-flex h-8 min-w-8 shrink-0 items-center justify-center rounded-lg border px-2 text-xs tabular-nums transition-colors disabled:pointer-events-none disabled:opacity-40',
                                        token === currentPage
                                            ? 'border-primary/30 bg-primary font-semibold text-primary-foreground'
                                            : 'border-border bg-muted/20 text-muted-foreground hover:bg-muted/40 hover:text-foreground',
                                    )}
                                >
                                    {token}
                                </button>
                            ),
                        )}
                    </div>

                    <button
                        type="button"
                        aria-label={t('nextPage')}
                        title={t('nextPage')}
                        disabled={!canNext}
                        onClick={() => onPageChange(currentPage + 1)}
                        className={navButtonClass}
                    >
                        <ChevronRight className="size-4" />
                    </button>
                    <button
                        type="button"
                        aria-label={t('lastPage')}
                        title={t('lastPage')}
                        disabled={!canJumpLast}
                        onClick={() => onPageChange(knownPages)}
                        className={cn(navButtonClass, 'hidden lg:inline-flex')}
                    >
                        <ChevronsRight className="size-4" />
                    </button>
                </div>
            </div>
        </div>
    );
}
