import { SiteAccount, Site as SiteRecord } from "@/api/endpoints/site";
import { deriveCheckinStatus, siteHasCheckinEnabled } from "./checkin-status";
import { normalizedStatus } from "./site-display";
import { siteAccountHasActiveSyncFailure } from "./sync-health";
import { SiteSummary, VisibleSite } from "./types";

function accountHasCheckinFailure(site: SiteRecord, account: SiteAccount, now = new Date()) {
  return deriveCheckinStatus(site, account, now) === "failed";
}

export function accountHasHealthFailure(site: SiteRecord, account: SiteAccount, now = new Date()) {
  return site.kind === 'checkin'
    ? accountHasCheckinFailure(site, account, now)
    : siteAccountHasActiveSyncFailure(site, account);
}

export function buildSiteSummary(site: SiteRecord, now = new Date()): SiteSummary {
  let keyCount = 0;
  let modelCount = 0;
  let groupCount = 0;
  let balance = 0;
  let todayIncome = 0;
  let failedAccountCount = 0;
  let disabledAccountCount = 0;
  let enabledAccountCount = 0;

  for (const account of site.accounts) {
    keyCount += account.tokens.length;
    modelCount += account.models.length;
    groupCount += account.user_groups.length;
    balance += account.balance;
    todayIncome += typeof account.today_income === "number" ? account.today_income : 0;

    if (account.enabled) enabledAccountCount += 1;
    else disabledAccountCount += 1;

    if (accountHasHealthFailure(site, account, now)) {
      failedAccountCount += 1;
    }
  }

  if (!site.enabled) {
    return {
      accountCount: site.accounts.length,
      keyCount,
      modelCount,
      groupCount,
      balance,
      todayIncome,
      failedAccountCount,
      disabledAccountCount,
      enabledAccountCount,
      healthLabel: "站点停用",
      healthTone: "muted",
    };
  }

  if (failedAccountCount > 0) {
    return {
      accountCount: site.accounts.length,
      keyCount,
      modelCount,
      groupCount,
      balance,
      todayIncome,
      failedAccountCount,
      disabledAccountCount,
      enabledAccountCount,
      healthLabel: `${failedAccountCount} 异常`,
      healthTone: "danger",
    };
  }

  if (disabledAccountCount > 0) {
    return {
      accountCount: site.accounts.length,
      keyCount,
      modelCount,
      groupCount,
      balance,
      todayIncome,
      failedAccountCount,
      disabledAccountCount,
      enabledAccountCount,
      healthLabel: `${disabledAccountCount} 已停用`,
      healthTone: "muted",
    };
  }

  if (site.accounts.length === 0) {
    return {
      accountCount: site.accounts.length,
      keyCount,
      modelCount,
      groupCount,
      balance,
      todayIncome,
      failedAccountCount,
      disabledAccountCount,
      enabledAccountCount,
      healthLabel: "待配置",
      healthTone: "warning",
    };
  }

  const checkinInactive = site.kind === 'checkin' && !siteHasCheckinEnabled(site);
  const allIdle = site.accounts.every(
    (account) => {
      if (!account.enabled) return false;
      if (site.kind === 'checkin') {
        const status = deriveCheckinStatus(site, account, now);
        return status === null || status === "idle";
      }
      return normalizedStatus(account.last_sync_status) === "idle";
    },
  );

  return {
    accountCount: site.accounts.length,
    keyCount,
    modelCount,
    groupCount,
    balance,
    todayIncome,
    failedAccountCount,
    disabledAccountCount,
    enabledAccountCount,
    healthLabel: checkinInactive
      ? (site.checkin_mode === 'disabled' ? "签到已禁用" : "签到未启用")
      : allIdle ? "未执行" : "正常",
    healthTone: checkinInactive ? "muted" : allIdle ? "warning" : "default",
  };
}

export function estimateVisibleSiteCardHeight(item: VisibleSite, expanded: boolean) {
  const tagRow = item.site.tags.length > 0 ? 30 : 0;
  if (item.forceExpanded || expanded) {
    return 360 + tagRow + item.visibleAccounts.length * 190;
  }
  if (item.site.accounts.length === 0) {
    return 280 + tagRow;
  }
  return 310 + tagRow;
}
