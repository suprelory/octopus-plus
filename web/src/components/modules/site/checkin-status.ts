import type { Site, SiteAccount } from "@/api/endpoints/site";
import {
  createEmptyCheckinSummary,
  recordCheckinSummaryAccount,
  type CheckinSummary,
} from "./checkin-summary";
import { siteAccountHasActiveSyncFailure } from "./sync-health";

export { createEmptyCheckinSummary } from "./checkin-summary";
export type { CheckinSummary } from "./checkin-summary";

export type CheckinFilterStatus =
  | "all"
  | "success"
  | "failed"
  | "pending"
  | "sync_failed"
  | "idle"
  | "disabled";

export type CheckinActiveFilterStatus = Exclude<CheckinFilterStatus, "all">;
export type DerivedCheckinStatus = Exclude<
  CheckinActiveFilterStatus,
  "sync_failed"
>;

function normalizeExecutionStatus(status?: string | null) {
  return status || "idle";
}

export type SiteCheckinPolicy = Pick<Site, "checkin_capability" | "checkin_mode" | "checkin_http_enabled">;

export function siteHasCheckinEnabled(site: SiteCheckinPolicy) {
  if (site.checkin_mode === "disabled") return false;
  return site.checkin_capability?.enabled ?? (site.checkin_mode === "enabled" || !!site.checkin_http_enabled);
}

export function siteCanVerifyCheckin(site: SiteCheckinPolicy) {
  if (site.checkin_mode === "disabled") return false;
  return site.checkin_capability?.can_verify ?? true;
}

export function accountHasCheckinEnabled(
  account: Pick<SiteAccount, "auto_checkin">,
  site: SiteCheckinPolicy,
) {
  return siteHasCheckinEnabled(site) && account.auto_checkin;
}

export function accountIsDisabled(
  site: Pick<Site, "enabled">,
  account: Pick<SiteAccount, "enabled">,
) {
  return !site.enabled || !account.enabled;
}

function localDateKey(value: Date, timeZone: string) {
  try {
    return new Intl.DateTimeFormat("en-CA", {
      timeZone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).format(value);
  } catch {
    return new Intl.DateTimeFormat("en-CA", {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).format(value);
  }
}

function happenedToday(value: string | null | undefined, now: Date, timeZone: string) {
  if (!value) return false;
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) {
    return false;
  }

  return localDateKey(date, timeZone) === localDateKey(now, timeZone);
}

export function deriveCheckinStatus(
  site: Pick<Site, "enabled" | "checkin_timezone"> & SiteCheckinPolicy,
  account: Pick<
    SiteAccount,
    "enabled" | "auto_checkin" | "last_checkin_at" | "last_checkin_success_at" | "last_checkin_status"
  >,
  now = new Date(),
): DerivedCheckinStatus | null {
  if (accountIsDisabled(site, account)) {
    return "disabled";
  }

  if (!siteHasCheckinEnabled(site)) {
    return null;
  }

  // Manual results count too; auto_checkin only controls pending membership.
  const pendingStatus = account.auto_checkin ? "idle" : null;

  if (account.last_checkin_status === "pending") return "pending";

  if (happenedToday(account.last_checkin_success_at, now, site.checkin_timezone)) {
    return "success";
  }

  if (!happenedToday(account.last_checkin_at, now, site.checkin_timezone)) {
    return pendingStatus;
  }

  switch (normalizeExecutionStatus(account.last_checkin_status)) {
    case "success":
      // A confirmation received today can belong to an earlier check-in date.
      return account.last_checkin_success_at ? pendingStatus : "success";
    case "failed":
    case "skipped":
      return "failed";
    default:
      return pendingStatus;
  }
}

export function accountMatchesCheckinFilters(
  site: Pick<Site, "enabled" | "checkin_timezone"> & SiteCheckinPolicy,
  account: Pick<
    SiteAccount,
    | "enabled"
    | "auto_checkin"
    | "last_sync_status"
    | "last_checkin_at"
    | "last_checkin_success_at"
    | "last_checkin_status"
  >,
  filterStatuses: CheckinActiveFilterStatus[],
  now = new Date(),
) {
  if (filterStatuses.length === 0) {
    return true;
  }

  const checkinStatus = deriveCheckinStatus(site, account, now);
  return filterStatuses.some((status) => {
    if (status === "sync_failed") {
      return siteAccountHasActiveSyncFailure(site, account);
    }
    return checkinStatus === status;
  });
}

export function buildCheckinSummary(
  sites: Site[] | undefined,
  now = new Date(),
): CheckinSummary {
  const summary = createEmptyCheckinSummary();

  for (const site of sites ?? []) {
    for (const account of site.accounts ?? []) {
      const status = deriveCheckinStatus(site, account, now);
      recordCheckinSummaryAccount(
        summary,
        status,
        siteAccountHasActiveSyncFailure(site, account),
      );
    }
  }

  return summary;
}
