import type { ProxyMode } from "./proxy-pool";

export enum SitePlatform {
  NewAPI = "new-api",
  AnyRouter = "anyrouter",
  OneAPI = "one-api",
  OneHub = "one-hub",
  DoneHub = "done-hub",
  Sub2API = "sub2api",
  API = "api",
}

export enum SiteCredentialType {
  UsernamePassword = "username_password",
  AccessToken = "access_token",
  APIKey = "api_key",
  Cookie = "cookie",
  LinkedAccount = "linked_account",
}

export type CustomHeader = {
  header_key: string;
  header_value: string;
};

export type SiteRouteBaseURL = {
  route_type: string;
  base_url: string;
};

export type SiteToken = {
  id: number;
  site_account_id: number;
  name: string;
  token: string;
  group_key: string;
  group_name: string;
  enabled: boolean;
  source: string;
  is_default: boolean;
  last_sync_at?: string | null;
};

export type SiteUserGroup = {
  id: number;
  site_account_id: number;
  group_key: string;
  name: string;
  raw_payload?: string | null;
  projection_disabled?: boolean;
  projection_suspended?: boolean;
  projection_suspend_reason?: string;
  projection_suspended_at?: string | null;
  model_sync_status?: 'idle' | 'synced' | 'empty' | 'stale' | 'failed' | 'unresolved' | 'missing_key' | 'removed';
  model_sync_message?: string;
  model_sync_authoritative?: boolean;
  model_sync_model_count?: number;
  last_model_sync_at?: string | null;
  last_model_sync_success_at?: string | null;
  model_sync_failure_count?: number;
};

export type SiteModel = {
  id: number;
  site_account_id: number;
  model_name: string;
  source: string;
};

export type SiteChannelBinding = {
  id: number;
  site_id: number;
  site_account_id: number;
  site_user_group_id?: number | null;
  group_key: string;
  channel_id: number;
};

export type SiteAccount = {
  id: number;
  site_id: number;
  name: string;
  credential_type: SiteCredentialType;
  username: string;
  password: string;
  access_token: string;
  api_key: string;
  refresh_token: string;
  cookie?: string;
  linked_account_id?: number | null;
  token_expires_at: number;
  platform_user_id?: number | null;
  proxy_mode: ProxyMode;
  proxy_config_id?: number | null;
  enabled: boolean;
  auto_sync: boolean;
  auto_checkin: boolean;
  random_checkin: boolean;
  checkin_interval_hours: number;
  checkin_random_window_minutes: number;
  next_auto_checkin_at?: string | null;
  last_sync_at?: string | null;
  last_checkin_at?: string | null;
  last_checkin_success_at?: string | null;
  checkin_failure_count: number;
  last_sync_status: string;
  last_checkin_status: string;
  last_sync_message: string;
  last_checkin_message: string;
  balance: number;
  balance_used: number;
  today_income: number;
  tokens: SiteToken[];
  user_groups: SiteUserGroup[];
  models: SiteModel[];
  channel_bindings: SiteChannelBinding[];
};

export type SiteCheckinMode = "auto" | "enabled" | "disabled";
export type SiteKind = "relay" | "checkin";

export type SiteCheckinCapability = {
  enabled: boolean;
  can_verify: boolean;
  default_enabled: boolean;
  source: "platform_default" | "verified" | "custom_http" | "disabled" | "instance";
  support: "unknown" | "supported" | "unsupported";
  verified_at?: string | null;
};

export type SiteCheckinDefaults = { enabled: boolean; has_builtin: boolean };

export type Site = {
  id: number;
  name: string;
  kind?: SiteKind;
  linked_site_id?: number | null;
  platform: SitePlatform;
  base_url: string;
  enabled: boolean;
  proxy_mode: Exclude<ProxyMode, "inherit">;
  proxy_config_id?: number | null;
  external_checkin_url?: string | null;
  checkin_mode?: SiteCheckinMode;
  checkin_capability?: SiteCheckinCapability;
  checkin_http_enabled?: boolean;
  checkin_use_subscription_token?: boolean;
  checkin_http_method?: "GET" | "POST";
  checkin_http_path?: string;
  checkin_http_body?: string;
  checkin_http_headers?: CustomHeader[] | null;
  checkin_reward_extractor?: string;
  checkin_status?: SiteCheckinStatusConfig | null;
  checkin_timezone: string;
  checkin_window_start: string;
  checkin_window_end: string;
  is_pinned: boolean;
  sort_order: number;
  global_weight: number;
  custom_header: CustomHeader[];
  route_base_urls: SiteRouteBaseURL[];
  tags: string[];
  default_route_type?: string;
  archived: boolean;
  archived_at?: string | null;
  accounts: SiteAccount[];
};

export type SiteServer = Omit<
  Site,
  "accounts" | "custom_header" | "route_base_urls" | "tags" | "checkin_http_headers"
> & {
  accounts: Array<
    Omit<
      SiteAccount,
      "tokens" | "user_groups" | "models" | "channel_bindings"
    > & {
      tokens: SiteToken[] | null;
      user_groups: SiteUserGroup[] | null;
      models: SiteModel[] | null;
      channel_bindings: SiteChannelBinding[] | null;
    }
  > | null;
  custom_header: CustomHeader[] | null;
  checkin_http_headers?: CustomHeader[] | null;
  route_base_urls: SiteRouteBaseURL[] | null;
  tags: string[] | null;
  default_route_type?: string | null;
};

export type SiteSyncResult = {
  account_id: number;
  site_id: number;
  status: string;
  channel_count: number;
  group_count: number;
  token_count: number;
  model_count: number;
  managed_channels: number[];
  models: string[];
  group_results: Array<{
    group_key: string;
    group_name: string;
    has_key: boolean;
    status: string;
    authoritative: boolean;
    model_count: number;
    message?: string;
    projection_suspended?: boolean;
    projection_suspend_reason?: string;
  }>;
  message: string;
};

export type SiteManualSyncMode = "replace";

export type SiteManualSyncFormat = "responses" | "snapshot";

export type SiteManualSyncRequest = {
  mode: SiteManualSyncMode;
  format: SiteManualSyncFormat;
  token_response?: unknown;
  group_responses?: unknown[];
  model_responses?: Array<{
    group_key: string;
    response: unknown;
  }>;
  account_response?: unknown;
  snapshot?: {
    access_token?: string;
    tokens?: Array<{
      name?: string;
      token: string;
      group_key?: string;
      group_name?: string;
      enabled?: boolean;
      is_default?: boolean;
    }>;
    groups?: Array<{
      group_key: string;
      name?: string;
    }>;
    models?: Record<
      string,
      Array<
        | string
        | {
            model_name: string;
            route_type?: string;
          }
      >
    >;
    balance?: number;
    balance_used?: number;
    today_income?: number;
  };
  preview_fingerprint?: string;
};

export type SiteManualSyncPreviewGroup = {
  group_key: string;
  group_name: string;
  token_count: number;
  usable_token_count: number;
  masked_token_count: number;
  model_count: number;
  model_action: "replace" | "preserve";
  route_types: string[];
  will_project: boolean;
};

export type SiteManualSyncPreview = {
  account_id: number;
  site_id: number;
  mode: SiteManualSyncMode;
  format: SiteManualSyncFormat;
  imported_token_count: number;
  imported_group_count: number;
  imported_model_count: number;
  token_count: number;
  usable_token_count: number;
  masked_token_count: number;
  group_count: number;
  model_count: number;
  channel_count_estimate: number;
  balance_provided: boolean;
  balance: number;
  balance_used_provided: boolean;
  balance_used: number;
  today_income_provided: boolean;
  today_income: number;
  groups: SiteManualSyncPreviewGroup[];
  warnings: string[];
  can_apply: boolean;
  preview_fingerprint: string;
};

export type SiteManualSyncApplyResult = {
  preview: SiteManualSyncPreview;
  sync_result: SiteSyncResult;
};

export type SiteCheckinStatusConfig = {
  method: 'GET' | 'POST';
  path: string;
  body: string;
  headers: CustomHeader[];
  skip_precheck: boolean;
  checked_path: string;
  checked_value: boolean | number | string;
  unchecked_value: boolean | number | string;
  date_path: string;
  code: string;
  initial_delay_ms: number;
  poll_interval_ms: number;
  max_attempts: number;
  timeout_seconds: number;
  confirm_timeout_minutes: number;
  reward_source: 'submit' | 'status';
};

export type SiteCheckinResult = {
  checkin_capability?: SiteCheckinCapability;
  log_id?: number;
	account_id: number;
  site_id: number;
	status: string;
  reason?: string;
  message: string;
  reward?: string;
};

export type AllAPIHubImportResult = {
  created_sites: number;
  reused_sites: number;
  created_accounts: number;
  updated_accounts: number;
  skipped_accounts: number;
  scheduled_sync_accounts: number;
  warnings: string[];
};

export type MetAPIImportResult = {
  created_sites: number;
  reused_sites: number;
  created_accounts: number;
  updated_accounts: number;
  skipped_accounts: number;
  imported_tokens: number;
  imported_groups: number;
  imported_models: number;
  disabled_models: number;
  warnings: string[];
};
