import { useQueryClient } from "@tanstack/react-query";
import { Site, SiteServer } from './site-types';

export function normalizeSiteServerList(data: SiteServer[]): Site[] {
  return data.map((site) => ({
    ...site,
    kind: site.kind ?? 'relay',
    linked_site_id: site.linked_site_id ?? null,
    custom_header: site.custom_header ?? [],
    checkin_http_headers: site.checkin_http_headers ?? [],
    route_base_urls: site.route_base_urls ?? [],
    tags: site.tags ?? [],
    default_route_type: site.default_route_type ?? undefined,
    proxy_mode: site.proxy_mode ?? "direct",
    proxy_config_id: site.proxy_config_id ?? null,
    external_checkin_url: site.external_checkin_url ?? null,
    checkin_mode: site.checkin_mode ?? 'auto',
    checkin_use_subscription_token: site.checkin_use_subscription_token ?? false,
    checkin_timezone: site.checkin_timezone || "Asia/Shanghai",
    checkin_window_start: site.checkin_window_start || "00:00",
    checkin_window_end: site.checkin_window_end || "23:59",
    is_pinned: site.is_pinned ?? false,
    sort_order: typeof site.sort_order === "number" ? site.sort_order : 0,
    global_weight:
      typeof site.global_weight === "number" && site.global_weight > 0
        ? site.global_weight
        : 1,
    archived: site.archived ?? false,
    archived_at: site.archived_at ?? null,
    accounts: (site.accounts ?? []).map((account) => ({
      ...account,
      credential_version: typeof account.credential_version === "number" ? account.credential_version : 0,
      refresh_token:
        typeof account.refresh_token === "string"
          ? account.refresh_token
          : "",
      token_expires_at:
        typeof account.token_expires_at === "number" &&
        account.token_expires_at > 0
          ? account.token_expires_at
          : 0,
      platform_user_id: account.platform_user_id ?? null,
      proxy_mode: account.proxy_mode ?? "inherit",
      proxy_config_id: account.proxy_config_id ?? null,
      random_checkin: account.random_checkin ?? false,
      checkin_interval_hours:
        typeof account.checkin_interval_hours === "number" &&
        account.checkin_interval_hours > 0
          ? account.checkin_interval_hours
          : 24,
      checkin_random_window_minutes:
        typeof account.checkin_random_window_minutes === "number" &&
        account.checkin_random_window_minutes >= 0
          ? account.checkin_random_window_minutes
          : 120,
      last_checkin_success_at: account.last_checkin_success_at ?? null,
      checkin_failure_count:
        typeof account.checkin_failure_count === "number" &&
        account.checkin_failure_count > 0
          ? account.checkin_failure_count
          : 0,
      balance: typeof account.balance === "number" ? account.balance : 0,
      balance_used:
        typeof account.balance_used === "number" ? account.balance_used : 0,
      today_income:
        typeof account.today_income === "number" ? account.today_income : 0,
      tokens: account.tokens ?? [],
      user_groups: account.user_groups ?? [],
      models: account.models ?? [],
      channel_bindings: account.channel_bindings ?? [],
    })),
  })) as Site[];
}

export function invalidateSiteQueries(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: ["sites", "list"] });
  queryClient.invalidateQueries({ queryKey: ["sites", "archived"] });
  queryClient.invalidateQueries({ queryKey: ["sites", "checkin-logs"] });
  queryClient.invalidateQueries({ queryKey: ["sites", "checkin-stats"] });
  queryClient.invalidateQueries({ queryKey: ["site-channel", "list"] });
  queryClient.invalidateQueries({ queryKey: ["channels", "list"] });
  queryClient.invalidateQueries({ queryKey: ["models", "channel"] });
  queryClient.invalidateQueries({ queryKey: ["proxy-pool"] });
}
