export type CheckinSummaryStatus =
  | "success"
  | "failed"
  | "pending"
  | "idle"
  | "disabled";

export type CheckinSummary = {
  total: number;
  success: number;
  failed: number;
  pending: number;
  sync_failed: number;
  idle: number;
  disabled: number;
};

export function createEmptyCheckinSummary(): CheckinSummary {
  return {
    total: 0,
    success: 0,
    failed: 0,
    pending: 0,
    sync_failed: 0,
    idle: 0,
    disabled: 0,
  };
}

export function recordCheckinSummaryAccount(
  summary: CheckinSummary,
  checkinStatus: CheckinSummaryStatus | null,
  hasSyncFailure: boolean,
) {
  summary.total += 1;
  if (checkinStatus) {
    summary[checkinStatus] += 1;
  }
  if (hasSyncFailure) {
    summary.sync_failed += 1;
  }
}
