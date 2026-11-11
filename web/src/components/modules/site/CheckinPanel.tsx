"use client";

import { useCallback, useMemo, useState } from "react";
import {
  ExternalLink,
  FilterX,
  History,
  Tag,
} from "lucide-react";
import { type Site } from "@/api/endpoints/site";
import { PageOverview } from "@/components/common/PageOverview";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useTranslations } from 'next-intl';
import { CheckinHistoryDialog } from './CheckinHistoryDialog';
import { CheckinBatchTaskStatus } from './CheckinBatchTaskStatus';
import { CheckinStatsPanel } from './CheckinStatsPanel';
import {
  buildCheckinSummary,
  type CheckinActiveFilterStatus,
  type CheckinFilterStatus,
} from "./checkin-status";

const FILTERS: Array<{ key: CheckinFilterStatus; label: string }> = [
  { key: "all", label: "全部" },
  { key: "success", label: "成功" },
  { key: "failed", label: "签到失败" },
  { key: "pending", label: "待确认" },
  { key: "idle", label: "未执行" },
  { key: "disabled", label: "禁用" },
];

function filterTone(status: CheckinFilterStatus, active: boolean) {
  if (active) {
    switch (status) {
      case "success":
        return "border-emerald-500/30 bg-emerald-500 text-white";
      case "failed":
      case "sync_failed":
        return "border-destructive/30 bg-destructive text-white";
      case "idle":
        return "border-border bg-foreground text-background";
      case "disabled":
        return "border-slate-500/30 bg-slate-700 text-white dark:bg-slate-200 dark:text-slate-900";
      case "all":
      default:
        return "border-primary/30 bg-primary text-primary-foreground";
    }
  }

  switch (status) {
    case "success":
      return "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
    case "failed":
    case "sync_failed":
      return "border-destructive/20 bg-destructive/10 text-destructive";
    case "idle":
      return "border-border bg-muted/40 text-muted-foreground";
    case "disabled":
      return "border-slate-500/20 bg-slate-500/10 text-slate-700 dark:text-slate-300";
    case "all":
    default:
      return "border-border bg-background text-foreground";
  }
}

function formatCurrency(value: number) {
  const safe = Number.isFinite(value) ? value : 0;
  return `$${safe.toFixed(2)}`;
}

export function CheckinPanel({
  sites,
  inventory,
  statusNow,
  visibleSiteCount,
  visibleAccountCount,
  searchTerm,
  hasActiveFilters,
  onClearFilters,
  activeFilterStatuses,
  onFilterChange,
  allTags,
  activeTags,
  onTagFilterChange,
}: {
  sites: Site[] | undefined;
  inventory: {
    totalBalance: number;
    totalBalanceUsed: number;
    enabledAccounts: number;
    totalAccounts: number;
  };
  statusNow: Date;
  visibleSiteCount: number;
  visibleAccountCount: number;
  searchTerm: string;
  hasActiveFilters: boolean;
  onClearFilters: () => void;
  activeFilterStatuses: CheckinActiveFilterStatus[];
  onFilterChange: (status: CheckinFilterStatus) => void;
  allTags: Array<{ tag: string; count: number }>;
  activeTags: string[];
  onTagFilterChange: (tag: string) => void;
}) {
  const t = useTranslations('workspace.checkin');
  const historyT = useTranslations('siteCheckinHistory');
  const [historyOpen, setHistoryOpen] = useState(false);
  const summary = useMemo(
    () => buildCheckinSummary(sites, statusNow),
    [sites, statusNow],
  );
  const hasContextBadges = Boolean(searchTerm);

  const manualCheckinUrls = useMemo(
    () =>
      (sites ?? [])
        .filter((s) => !s.checkin_http_enabled && s.external_checkin_url?.trim())
        .map((s) => s.external_checkin_url!.trim()),
    [sites],
  );

  const openAllManualCheckin = useCallback(() => {
    for (const url of manualCheckinUrls) {
      window.open(url, "_blank", "noopener,noreferrer");
    }
  }, [manualCheckinUrls]);

  return (
    <div className="space-y-4">
      <PageOverview title={t('title')} description={t('description')} metrics={[
        { label: "当前余额", value: formatCurrency(inventory.totalBalance), accent: true },
        { label: "累计消耗", value: formatCurrency(inventory.totalBalanceUsed) },
        { label: "启用账号", value: `${inventory.enabledAccounts} / ${inventory.totalAccounts}` },
        {
          label: "今日签到异常",
          value: <span className={cn(summary.failed > 0 && "text-amber-600 dark:text-amber-400")}>{summary.failed}</span>,
        },
      ]}>
        <div className="flex w-full flex-wrap items-center gap-2 border-t border-border/60 pt-3">
          <span className="mr-auto text-xs text-muted-foreground">
            当前结果 <span className="font-medium tabular-nums text-foreground">
              {visibleSiteCount} 站点 / {visibleAccountCount} 账号
            </span>
          </span>
          {hasActiveFilters && hasContextBadges ? <Badge variant="outline">搜索：{searchTerm}</Badge> : null}
        </div>
      </PageOverview>

      <CheckinBatchTaskStatus />
      <CheckinStatsPanel sites={sites ?? []} />

      <div className="rounded-2xl border border-border/60 bg-card/60 px-4 py-4 sm:px-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-2">
            {FILTERS.map((filter) => {
              const count =
                filter.key === "all" ? summary.total : summary[filter.key];
              const active =
                filter.key === "all"
                  ? activeFilterStatuses.length === 0
                  : activeFilterStatuses.includes(filter.key);
              return (
                <button
                  key={filter.key}
                  type="button"
                  aria-pressed={active}
                  onClick={() => onFilterChange(filter.key)}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring",
                    filterTone(filter.key, active),
                  )}
                >
                  <span>{count}</span>
                  <span>{filter.label}</span>
                </button>
              );
            })}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" variant="ghost" size="sm" className="rounded-xl text-xs" onClick={() => setHistoryOpen(true)}>
              <History className="size-4" />
              {historyT('title')}
            </Button>
            {hasActiveFilters ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="rounded-xl text-xs"
                onClick={onClearFilters}
              >
                <FilterX className="size-4" />
                清空筛选
              </Button>
            ) : null}
            {manualCheckinUrls.length > 0 ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="rounded-xl text-xs"
                onClick={openAllManualCheckin}
              >
                <ExternalLink className="size-4" />
                打开手动签到 ({manualCheckinUrls.length})
              </Button>
            ) : null}
          </div>
        </div>

        {allTags.length > 0 ? (
          <div className="mt-3 flex flex-wrap gap-2">
            {allTags.map(({ tag, count }) => {
              const active = activeTags.includes(tag);
              return (
                <button
                  key={tag}
                  type="button"
                  aria-pressed={active}
                  onClick={() => onTagFilterChange(tag)}
                  title={active ? `取消按「${tag}」筛选` : `按「${tag}」筛选`}
                  className={cn(
                    "inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                    active
                      ? "border-primary/30 bg-primary text-primary-foreground"
                      : "border-border bg-background text-foreground hover:bg-muted/40",
                  )}
                >
                  <Tag className="size-3" />
                  <span>{tag}</span>
                  <span className="text-[10px] opacity-70">{count}</span>
                </button>
              );
            })}
          </div>
        ) : null}
      </div>
      {historyOpen ? <CheckinHistoryDialog sites={sites ?? []} onOpenChange={setHistoryOpen} /> : null}
    </div>
  );
}
