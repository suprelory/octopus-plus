"use client";

import { useRef, useState } from "react";
import {
  CheckCircle2,
  FileJson,
  Loader2,
  Plus,
  Trash2,
  TriangleAlert,
  XIcon,
} from "lucide-react";
import {
  SitePlatform,
  type Site,
  type SiteAccount,
  type SiteManualSyncFormat,
  type SiteManualSyncPreview,
  type SiteManualSyncRequest,
  useApplyManualSiteSync,
  usePreviewManualSiteSync,
} from "@/api/endpoints/site";
import { toast } from "@/components/common/Toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { getErrorMessage } from "@/lib/errors";

interface ManualSyncDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  site: Site | null;
  account: SiteAccount | null;
}

type ModelResponseRow = {
  id: number;
  groupKey: string;
  response: string;
};

type ManualSyncEndpointHints = {
  token: string;
  group: string;
  model: string;
  account: string;
};

const TEXTAREA_CLASS =
  "w-full resize-y rounded-xl border border-input bg-transparent px-3 py-2 font-mono text-xs leading-5 shadow-xs outline-none transition-[color,box-shadow] placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/20 disabled:cursor-not-allowed disabled:opacity-50";

const ROUTE_LABELS: Record<string, string> = {
  openai_chat: "Chat",
  openai_response: "Responses",
  anthropic: "Claude",
  gemini: "Gemini",
  openai_embedding: "Embedding",
  unknown: "Unknown",
};

function getManualSyncEndpointHints(
  platform?: Site["platform"],
): ManualSyncEndpointHints {
  const managementHints: ManualSyncEndpointHints = {
    token: "GET /api/token/?p=0&size=100",
    group:
      "GET /api/user/self/groups；备用：GET /api/user_group_map",
    model: "GET /models；备用：GET /v1/models（使用对应分组 API Key）",
    account: "GET /api/user/self",
  };

  switch (platform) {
    case SitePlatform.NewAPI:
    case SitePlatform.AnyRouter:
    case SitePlatform.AgentRouter:
      return {
        ...managementHints,
        model:
          "GET /models；备用：GET /v1/models、GET /api/user/models（使用对应分组 API Key）",
      };
    case SitePlatform.OneHub:
    case SitePlatform.DoneHub:
      return {
        ...managementHints,
        model:
          "GET /models；备用：GET /v1/models、GET /api/available_model（使用对应分组 API Key）",
      };
    case SitePlatform.Sub2API:
      return {
        token:
          "GET /api/v1/keys?page=1&page_size=100；备用：GET /api/v1/api-keys?page=1&page_size=100、GET /api/v1/keys、GET /api/v1/api-keys",
        group:
          "GET /api/v1/groups/available；备用：GET /api/v1/groups、GET /api/v1/group",
        model:
          "GET /v1/models；备用：GET /api/v1/models、GET /v1beta/models、GET /models（使用对应分组 API Key）",
        account: "GET /api/v1/auth/me",
      };
    case SitePlatform.API:
      return {
        token: "无对应的 Token 列表接口，可留空",
        group: "无对应的分组接口，可留空并使用 default 分组",
        model: "GET {已配置 BaseURL}/models（分组标识填写 default）",
        account: "无兼容的账户余额接口，可留空",
      };
    default:
      return managementHints;
  }
}

function emptyModelRow(id: number): ModelResponseRow {
  return { id, groupKey: "default", response: "" };
}

function parseJSONField(value: string, label: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    throw new Error(`${label}不是有效 JSON`);
  }
}

function modelActionLabel(action: string) {
  switch (action) {
    case "replace":
      return "替换模型";
    default:
      return "保留模型";
  }
}

function formatBalance(value: number) {
  return new Intl.NumberFormat("zh-CN", {
    minimumFractionDigits: 0,
    maximumFractionDigits: 4,
  }).format(value);
}

export function ManualSyncDialog({
  open,
  onOpenChange,
  site,
  account,
}: ManualSyncDialogProps) {
  const previewMutation = usePreviewManualSiteSync();
  const applyMutation = useApplyManualSiteSync();
  const nextRowID = useRef(2);

  const [format, setFormat] =
    useState<SiteManualSyncFormat>("responses");
  const [tokenResponse, setTokenResponse] = useState("");
  const [groupResponse, setGroupResponse] = useState("");
  const [accountResponse, setAccountResponse] = useState("");
  const [modelRows, setModelRows] = useState<ModelResponseRow[]>(() => [
    emptyModelRow(1),
  ]);
  const [snapshotText, setSnapshotText] = useState("");
  const [preview, setPreview] = useState<SiteManualSyncPreview | null>(null);

  const isBusy = previewMutation.isPending || applyMutation.isPending;
  const endpointHints = getManualSyncEndpointHints(site?.platform);

  function reset() {
    setFormat("responses");
    setTokenResponse("");
    setGroupResponse("");
    setAccountResponse("");
    setModelRows([emptyModelRow(1)]);
    setSnapshotText("");
    setPreview(null);
    nextRowID.current = 2;
    previewMutation.reset();
    applyMutation.reset();
  }

  function handleOpenChange(next: boolean) {
    if (!next) reset();
    onOpenChange(next);
  }

  function invalidatePreview() {
    setPreview(null);
    previewMutation.reset();
  }

  function buildRequest(): SiteManualSyncRequest {
    if (!account) throw new Error("未选择站点账号");

    if (format === "snapshot") {
      const raw = snapshotText.trim();
      if (!raw) throw new Error("请填写统一快照 JSON");
      const snapshot = parseJSONField(raw, "统一快照");
      if (
        typeof snapshot !== "object" ||
        snapshot === null ||
        Array.isArray(snapshot)
      ) {
        throw new Error("统一快照必须是 JSON 对象");
      }
      return {
        mode: "replace",
        format,
        snapshot: snapshot as NonNullable<SiteManualSyncRequest["snapshot"]>,
      };
    }

    const request: SiteManualSyncRequest = { mode: "replace", format };
    let hasSection = false;
    if (tokenResponse.trim()) {
      request.token_response = parseJSONField(tokenResponse, "Token 响应");
      hasSection = true;
    }
    if (groupResponse.trim()) {
      request.group_responses = [
        parseJSONField(groupResponse, "分组响应"),
      ];
      hasSection = true;
    }
    if (accountResponse.trim()) {
      request.account_response = parseJSONField(
        accountResponse,
        "账户响应",
      );
      hasSection = true;
    }

    const modelResponses = modelRows.flatMap((row, index) => {
      const groupKey = row.groupKey.trim();
      const response = row.response.trim();
      if (!groupKey && !response) return [];
      if (!groupKey) throw new Error(`第 ${index + 1} 个模型响应缺少分组标识`);
      if (!response) throw new Error(`分组「${groupKey}」的模型响应为空`);
      return [
        {
          group_key: groupKey,
          response: parseJSONField(response, `分组「${groupKey}」的模型响应`),
        },
      ];
    });
    if (modelResponses.length > 0) {
      request.model_responses = modelResponses;
      hasSection = true;
    }
    if (!hasSection) throw new Error("请至少填写一个接口响应");
    return request;
  }

  async function handlePreview() {
    if (!account) return;
    try {
      const request = buildRequest();
      const result = await previewMutation.mutateAsync({
        id: account.id,
        request,
      });
      setPreview(result);
    } catch (error) {
      setPreview(null);
      toast.error(getErrorMessage(error, "手动同步失败"));
    }
  }

  async function handleApply() {
    if (!account || !preview) return;
    try {
      const request = buildRequest();
      request.preview_fingerprint = preview.preview_fingerprint;
      const result = await applyMutation.mutateAsync({
        id: account.id,
        request,
      });
      toast.success(result.sync_result.message || "手动同步数据已应用");
      handleOpenChange(false);
    } catch (error) {
      toast.error(getErrorMessage(error, "手动同步失败"));
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="flex max-h-[min(calc(100vh-2rem),58rem)] w-screen max-w-full flex-col gap-0 overflow-hidden rounded-3xl border-0 bg-card px-6 py-4 text-card-foreground sm:max-w-4xl"
      >
        <header className="mb-4 flex shrink-0 items-start justify-between gap-4">
          <div className="min-w-0 flex-1">
            <h2 className="flex items-center gap-2 truncate text-2xl font-bold">
              <FileJson className="size-5 shrink-0" />
              手动导入同步数据
            </h2>
            <p className="mt-1 truncate text-sm text-muted-foreground">
              {site?.name ?? "站点"} · {account?.name ?? "账号"}
            </p>
          </div>
          <button
            type="button"
            onClick={() => handleOpenChange(false)}
            aria-label="关闭"
            className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <XIcon className="size-5" />
          </button>
        </header>

        <div className="flex min-h-0 flex-1 flex-col">
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-1 pb-4">
            <div className="grid gap-4">
              <div className="space-y-1.5">
                <label className="text-sm font-medium">输入格式</label>
                <Select
                  value={format}
                  disabled={isBusy}
                  onValueChange={(value) => {
                    setFormat(value as SiteManualSyncFormat);
                    invalidatePreview();
                  }}
                >
                  <SelectTrigger className="rounded-xl">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="rounded-xl">
                    <SelectItem value="responses" className="rounded-xl">
                      原始接口响应
                    </SelectItem>
                    <SelectItem value="snapshot" className="rounded-xl">
                      统一快照
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="border-t border-border/60" />

            {format === "responses" ? (
              <div className="space-y-4">
                <p className="rounded-xl bg-muted/50 px-3 py-2 text-xs leading-5 text-muted-foreground">
                  以下路径相对于站点地址
                  {site?.base_url ? (
                    <code className="mx-1 break-all text-foreground">
                      {site.base_url}
                    </code>
                  ) : null}
                  ，请粘贴接口返回的完整 JSON 响应体。
                </p>
                <div className="grid gap-4 lg:grid-cols-2">
                  <ResponseField
                    label="Token 响应"
                    hint={endpointHints.token}
                    value={tokenResponse}
                    minHeight="min-h-32"
                    disabled={isBusy}
                    onChange={(value) => {
                      setTokenResponse(value);
                      invalidatePreview();
                    }}
                  />
                  <ResponseField
                    label="分组响应"
                    hint={endpointHints.group}
                    value={groupResponse}
                    minHeight="min-h-32"
                    disabled={isBusy}
                    onChange={(value) => {
                      setGroupResponse(value);
                      invalidatePreview();
                    }}
                  />
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-3">
                    <div className="space-y-1">
                      <label className="text-sm font-medium">分组模型响应</label>
                      <EndpointHint text={endpointHints.model} />
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={isBusy}
                      className="h-7 rounded-xl px-2 text-xs text-muted-foreground"
                      onClick={() => {
                        setModelRows((current) => [
                          ...current,
                          emptyModelRow(nextRowID.current++),
                        ]);
                        invalidatePreview();
                      }}
                    >
                      <Plus className="mr-1 size-3.5" />
                      添加分组
                    </Button>
                  </div>
                  <div className="space-y-3">
                    {modelRows.map((row, index) => (
                      <div
                        key={row.id}
                        className="grid gap-2 border-b border-border/50 pb-3 last:border-b-0 last:pb-0 sm:grid-cols-[11rem_minmax(0,1fr)_2rem]"
                      >
                        <Input
                          value={row.groupKey}
                          disabled={isBusy}
                          aria-label={`模型响应 ${index + 1} 的分组标识`}
                          placeholder="分组标识"
                          className="rounded-xl"
                          onChange={(event) => {
                            const value = event.target.value;
                            setModelRows((current) =>
                              current.map((item) =>
                                item.id === row.id
                                  ? { ...item, groupKey: value }
                                  : item,
                              ),
                            );
                            invalidatePreview();
                          }}
                        />
                        <textarea
                          value={row.response}
                          disabled={isBusy}
                          aria-label={`分组 ${row.groupKey || index + 1} 的模型响应`}
                          placeholder='{"data":[{"id":"gpt-5"}]}'
                          className={cn(TEXTAREA_CLASS, "min-h-24")}
                          onChange={(event) => {
                            const value = event.target.value;
                            setModelRows((current) =>
                              current.map((item) =>
                                item.id === row.id
                                  ? { ...item, response: value }
                                  : item,
                              ),
                            );
                            invalidatePreview();
                          }}
                        />
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          disabled={isBusy || modelRows.length === 1}
                          aria-label={`删除第 ${index + 1} 个模型响应`}
                          className="rounded-xl text-muted-foreground hover:text-destructive"
                          onClick={() => {
                            setModelRows((current) =>
                              current.filter((item) => item.id !== row.id),
                            );
                            invalidatePreview();
                          }}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    ))}
                  </div>
                </div>

                <ResponseField
                  label="账户响应"
                  hint={endpointHints.account}
                  value={accountResponse}
                  minHeight="min-h-28"
                  disabled={isBusy}
                  onChange={(value) => {
                    setAccountResponse(value);
                    invalidatePreview();
                  }}
                />
              </div>
            ) : (
              <ResponseField
                label="统一快照 JSON"
                hint="自定义统一数据结构，不对应单个上游接口"
                value={snapshotText}
                minHeight="min-h-72"
                disabled={isBusy}
                placeholder={'{"tokens":[],"groups":[],"models":{"default":["gpt-5"]},"balance":10.5}'}
                onChange={(value) => {
                  setSnapshotText(value);
                  invalidatePreview();
                }}
              />
            )}

            {preview ? <PreviewResult preview={preview} /> : null}
          </div>

          <footer className="flex shrink-0 flex-col-reverse gap-2 border-t border-border/60 pt-4 sm:flex-row sm:justify-end">
            <Button
              type="button"
              variant="outline"
              className="rounded-xl"
              disabled={isBusy}
              onClick={() => handleOpenChange(false)}
            >
              取消
            </Button>
            <Button
              type="button"
              variant="outline"
              className="rounded-xl"
              disabled={isBusy}
              onClick={handlePreview}
            >
              {previewMutation.isPending ? (
                <Loader2 className="mr-2 size-4 animate-spin" />
              ) : (
                <FileJson className="mr-2 size-4" />
              )}
              预览解析
            </Button>
            <Button
              type="button"
              className="rounded-xl"
              disabled={!preview?.can_apply || isBusy}
              onClick={handleApply}
            >
              {applyMutation.isPending ? (
                <Loader2 className="mr-2 size-4 animate-spin" />
              ) : (
                <CheckCircle2 className="mr-2 size-4" />
              )}
              应用并刷新渠道
            </Button>
          </footer>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ResponseField({
  label,
  hint,
  value,
  onChange,
  disabled,
  minHeight,
  placeholder = "粘贴 JSON 响应",
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  minHeight: string;
  placeholder?: string;
}) {
  return (
    <div className="space-y-1.5">
      <label className="text-sm font-medium">{label}</label>
      {hint ? <EndpointHint text={hint} /> : null}
      <textarea
        value={value}
        disabled={disabled}
        aria-label={label}
        placeholder={placeholder}
        className={cn(TEXTAREA_CLASS, minHeight)}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  );
}

function EndpointHint({ text }: { text: string }) {
  return (
    <p className="font-mono text-[11px] leading-4 text-muted-foreground">
      接口：{text}
    </p>
  );
}

function PreviewResult({ preview }: { preview: SiteManualSyncPreview }) {
  const stats = [
    ["Key", preview.token_count],
    ["分组", preview.group_count],
    ["模型", preview.model_count],
    ["预计渠道", preview.channel_count_estimate],
  ] as const;
  const hasBalanceChange =
    preview.balance_provided ||
    preview.balance_used_provided ||
    preview.today_income_provided;

  return (
    <div className="space-y-4 border-t border-border/60 pt-5">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">解析预览</h3>
        <Badge variant="outline" className="rounded-full">
          按区段替换
        </Badge>
      </div>

      <div className="grid grid-cols-2 divide-x divide-y divide-border/60 overflow-hidden rounded-2xl border border-border/60 sm:grid-cols-4 sm:divide-y-0">
        {stats.map(([label, value]) => (
          <div key={label} className="px-3 py-3 text-center">
            <div className="text-lg font-semibold tabular-nums">{value}</div>
            <div className="text-xs text-muted-foreground">{label}</div>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
        <span>本次解析 Key {preview.imported_token_count}</span>
        <span>·</span>
        <span>分组 {preview.imported_group_count}</span>
        <span>·</span>
        <span>模型 {preview.imported_model_count}</span>
        {preview.masked_token_count > 0 ? (
          <>
            <span>·</span>
            <span className="text-amber-600 dark:text-amber-400">
              脱敏 Key {preview.masked_token_count}
            </span>
          </>
        ) : null}
      </div>

      {hasBalanceChange ? (
        <div className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
          {preview.balance_provided ? (
            <span>余额 {formatBalance(preview.balance)}</span>
          ) : null}
          {preview.balance_used_provided ? (
            <span>已用 {formatBalance(preview.balance_used)}</span>
          ) : null}
          {preview.today_income_provided ? (
            <span>今日收入 {formatBalance(preview.today_income)}</span>
          ) : null}
        </div>
      ) : null}

      <div className="divide-y divide-border/50 rounded-2xl border border-border/60">
        {preview.groups.map((group) => (
          <div
            key={group.group_key}
            className="flex flex-col gap-2 px-3 py-3 sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-sm font-medium">
                  {group.group_name || group.group_key}
                </span>
                <Badge variant="secondary" className="rounded-full text-[10px]">
                  {modelActionLabel(group.model_action)}
                </Badge>
                <Badge
                  variant="outline"
                  className={cn(
                    "rounded-full text-[10px]",
                    group.will_project
                      ? "border-emerald-500/40 text-emerald-600 dark:text-emerald-400"
                      : "text-muted-foreground",
                  )}
                >
                  {group.will_project ? "可投影" : "不投影"}
                </Badge>
              </div>
              <div className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                {group.group_key}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground sm:justify-end">
              <span>Key {group.usable_token_count}/{group.token_count}</span>
              <span>模型 {group.model_count}</span>
              {group.route_types.map((routeType) => (
                <span key={routeType}>
                  {ROUTE_LABELS[routeType] ?? routeType}
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>

      {preview.warnings.length > 0 ? (
        <div className="space-y-2 rounded-2xl bg-amber-500/10 px-3 py-3 text-sm text-amber-800 dark:text-amber-200">
          {preview.warnings.map((warning) => (
            <div key={warning} className="flex items-start gap-2">
              <TriangleAlert className="mt-0.5 size-4 shrink-0" />
              <span>{warning}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
