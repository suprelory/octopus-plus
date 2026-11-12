"use client";

import { SiteAccount, Site as SiteRecord } from "@/api/endpoints/site";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/animate-ui/components/animate/tooltip";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import { useSettingStore } from "@/stores/setting";
import {
  CalendarCheck2,
  FileJson,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Trash2,
  Waypoints,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { accountHasCheckinEnabled, siteCanVerifyCheckin, siteHasCheckinEnabled } from "./checkin-status";
import { translateSiteMessage } from "./site-message";
import { siteAccountHasActivePartialSync } from "./sync-health";

import {
  CREDENTIAL_LABELS,
  MENU_BUTTON_CLASS,
  cardToneClass,
  formatBalance,
  formatDateTime,
  normalizedStatus,
} from "./site-display";

import { accountHasHealthFailure } from "./site-summary";
import { CompactMetric, ExecutionSummary, IconActionButton, StaticSummary } from "./SiteStatus";

import { HealthTone, SiteEditorActions } from "./types";
import { SiteActions } from "./useSiteActions";
import { SiteLayout } from "./useSiteLayout";

export function SiteAccountRow({
  site,
  account,
  statusNow,
  actions,
  editors,
  layout,
}: {
  site: SiteRecord;
  account: SiteAccount;
  statusNow: Date;
  actions: SiteActions;
  editors: SiteEditorActions;
  layout: SiteLayout;
}) {
  const t = useTranslations();
  const isCheckin = site.kind === 'checkin';
  const checkinT = useTranslations('siteCheckinCapability');
  const locale = useSettingStore((state) => state.locale);
  const tProxy = useTranslations("proxyPool");
  const {
    enableSiteAccount,
    syncingAccountIds,
    checkinAccountIds,
    handleToggleAccount,
    handleSyncAccount,
    handleCheckinAccount,
    handleDeleteAccount,
    jumpToSiteChannelAccount,
  } = actions;
  const { openEditAccountDialog, openManualSyncDialog } = editors;
  const { setAccountElementRef, highlightedAccountId } = layout;
  const accountFailed = accountHasHealthFailure(site, account, statusNow);
  const accountPartial = siteAccountHasActivePartialSync(site, account);
  const accountTone: HealthTone = accountFailed
    ? "danger"
    : accountPartial
      ? "warning"
      : account.enabled
        ? "default"
        : "muted";
  const checkinEnabled = siteHasCheckinEnabled(site);
  const canShowManualCheckin = siteCanVerifyCheckin(site);
  const checkinSupport = site.checkin_capability?.support ?? 'unknown';
  const checkinPolicyLabel = site.checkin_mode === 'disabled'
    ? 'disabledForSite'
    : !canShowManualCheckin ? 'requiresCustom'
      : checkinEnabled ? 'enabledForSite'
        : checkinSupport === 'unsupported' ? 'unavailableForSite' : 'defaultDisabled';

  return (
    <article
      key={account.id}
      ref={(node) => setAccountElementRef(account.id, node)}
      className={cn(
        "rounded-2xl border px-4 py-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.04)] transition-colors",
        cardToneClass(accountTone),
        highlightedAccountId === account.id &&
          "ring-2 ring-primary/35 ring-offset-2 ring-offset-background",
      )}
    >
      <div className="space-y-3">
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <div className="text-sm font-semibold">{account.name}</div>
              <Badge variant="outline">{CREDENTIAL_LABELS[account.credential_type]}</Badge>
              <Badge
                variant="outline"
                className={account.enabled ? "text-emerald-600" : "text-muted-foreground"}
              >
                {account.enabled ? "启用中" : "已停用"}
              </Badge>
            </div>

            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {!isCheckin ? <><CompactMetric label="分组" value={account.user_groups.length} />
              <CompactMetric label="模型" value={account.models.length} /></> : null}
              <CompactMetric label="余额" value={formatBalance(account.balance)} />
              <CompactMetric label="今日收入" value={formatBalance(account.today_income)} />
            </div>

            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              {!isCheckin ? <span>{account.auto_sync ? "自动同步" : "手动同步"}</span> : null}
              {isCheckin ? <span>
                {account.auto_checkin
                  ? account.random_checkin
                    ? "随机签到"
                    : "自动签到"
                  : "手动签到"}
              </span> : null}
              <span>
                {account.proxy_mode === "inherit"
                  ? tProxy("site.inherit")
                  : account.proxy_mode === "pool"
                    ? tProxy("mode.pool")
                    : account.proxy_mode === "system"
                      ? tProxy("mode.system")
                      : tProxy("mode.direct")}
              </span>
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-2 self-start">
            <Tooltip>
              <TooltipTrigger asChild>
                <span>
                  <Switch
                    checked={account.enabled}
                    disabled={enableSiteAccount.isPending}
                    onCheckedChange={() => handleToggleAccount(account)}
                  />
                </span>
              </TooltipTrigger>
              <TooltipContent>{account.enabled ? "停用账号" : "启用账号"}</TooltipContent>
            </Tooltip>

            {!isCheckin ? <IconActionButton
              label="同步账号"
              disabled={syncingAccountIds.has(account.id)}
              onClick={() => handleSyncAccount(account)}
            >
              <RefreshCw
                className={cn("size-4", syncingAccountIds.has(account.id) && "animate-spin")}
              />
            </IconActionButton> : canShowManualCheckin ? <IconActionButton
              label={checkinT(checkinSupport === 'supported' ? 'checkinNow' : 'verifyNow')}
              disabled={!site.enabled || !account.enabled || checkinAccountIds.has(account.id)}
              onClick={() => handleCheckinAccount(account)}
            ><CalendarCheck2 className={cn('size-4', checkinAccountIds.has(account.id) && 'animate-pulse')} /></IconActionButton> : null}

            <Popover>
              <PopoverTrigger asChild>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="outline"
                  className="rounded-xl"
                  aria-label="更多账号操作"
                  title="更多账号操作"
                >
                  <MoreHorizontal className="size-4" />
                </Button>
              </PopoverTrigger>
              <PopoverContent
                align="end"
                className="w-44 rounded-2xl border border-border/60 bg-card p-2"
              >
                <div className="grid gap-1">
                  {!isCheckin ? <button
                    type="button"
                    className={MENU_BUTTON_CLASS}
                    onClick={() => jumpToSiteChannelAccount(site.id, account.id)}
                  >
                    <Waypoints className="size-4" />
                    <span>查看站点渠道</span>
                  </button> : null}
                  <button
                    type="button"
                    className={MENU_BUTTON_CLASS}
                    onClick={() => openEditAccountDialog(site, account)}
                  >
                    <Pencil className="size-4" />
                    <span>编辑账号</span>
                  </button>
                  {!isCheckin ? <button
                    type="button"
                    className={MENU_BUTTON_CLASS}
                    onClick={() => openManualSyncDialog(site, account)}
                  >
                    <FileJson className="size-4" />
                    <span>手动导入同步数据</span>
                  </button> : null}
                  <button
                    type="button"
                    className={cn(MENU_BUTTON_CLASS, "text-destructive")}
                    onClick={() => handleDeleteAccount(account)}
                  >
                    <Trash2 className="size-4" />
                    <span>删除账号</span>
                  </button>
                </div>
              </PopoverContent>
            </Popover>
          </div>
        </div>

        <div className="space-y-1">
          {!isCheckin ? <ExecutionSummary
            label="同步"
            status={normalizedStatus(account.last_sync_status)}
            at={account.last_sync_at}
            message={translateSiteMessage(locale, account.last_sync_message, t) || "等待首次同步"}
          /> : null}
          {isCheckin && (checkinEnabled || account.last_checkin_at) ? (
            <ExecutionSummary
              label="签到"
              status={normalizedStatus(account.last_checkin_status)}
              at={account.last_checkin_at}
              message={account.last_checkin_message || "等待首次签到"}
            />
          ) : null}
          {isCheckin ? <><StaticSummary tone={checkinSupport === 'unsupported' ? 'warning' : undefined} text={checkinT(checkinPolicyLabel)} />
          <div className="pl-4 text-xs text-muted-foreground">
            {checkinT(checkinSupport)}
            {site.checkin_capability?.verified_at ? ` · ${formatDateTime(site.checkin_capability.verified_at)}` : ''}
          </div></> : null}
          {isCheckin && accountHasCheckinEnabled(account, site) ? (
            <div className="pl-4 text-xs text-muted-foreground">
              下次自动签到{" "}
              {account.next_auto_checkin_at
                ? formatDateTime(account.next_auto_checkin_at)
                : "待调度"}{" "}
              · 每天一次
              {account.random_checkin
                ? ` · 随机延迟 0-${account.checkin_random_window_minutes} 分钟`
                : ""}
              {account.last_checkin_success_at
                ? ` · 上次成功 ${formatDateTime(account.last_checkin_success_at)}`
                : ""}
              {account.checkin_failure_count > 0
                ? ` · 连续失败 ${account.checkin_failure_count} 次`
                : ""}
            </div>
          ) : null}
        </div>
      </div>
    </article>
  );
}
