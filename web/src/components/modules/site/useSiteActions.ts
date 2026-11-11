"use client";

import {
  SiteAccount,
  Site as SiteRecord,
  useArchiveSite,
  useCheckinSiteAccount,
  useCheckinSelectedSites,
  useDeleteSite,
  useDeleteSiteAccount,
  useEnableSite,
  useEnableSiteAccount,
  useSiteBatchAction,
  useSyncSiteAccount,
  useUpdateSite,
} from "@/api/endpoints/site";
import { toast } from "@/components/common/Toast";
import { useJumpStore } from "@/stores/jump";
import { useSettingStore } from "@/stores/setting";
import { useTranslations } from "next-intl";
import { useState } from "react";
import { getErrorMessage, getSiteErrorMessage, statusLabel } from "./site-display";
import { translateSiteMessage } from "./site-message";

import type { Dispatch, SetStateAction } from "react";
export function useSiteActions(setExpandedSiteIds: Dispatch<SetStateAction<Set<number>>>) {
  const t = useTranslations();
  const locale = useSettingStore((state) => state.locale);
  const updateSite = useUpdateSite();

  const enableSite = useEnableSite();

  const deleteSite = useDeleteSite();

  const archiveSite = useArchiveSite();

  const enableSiteAccount = useEnableSiteAccount();

  const deleteSiteAccount = useDeleteSiteAccount();

  const syncSiteAccount = useSyncSiteAccount();

  const checkinSiteAccount = useCheckinSiteAccount();

  const batchAction = useSiteBatchAction();
  const checkinSelectedSites = useCheckinSelectedSites();

  // Batch selection
  const [selectedSiteIds, setSelectedSiteIds] = useState<number[]>([]);

  // Delete confirmation
  const [deleteConfirm, setDeleteConfirm] = useState<{
    type: "site" | "account" | "archive-site" | "batch-site";
    id: number;
    name: string;
  } | null>(null);

  const [syncingAccountIds, setSyncingAccountIds] = useState<Set<number>>(() => new Set());

  const [checkinAccountIds, setCheckinAccountIds] = useState<Set<number>>(() => new Set());

  const requestJump = useJumpStore((state) => state.requestJump);

  async function handleToggleSite(site: SiteRecord) {
    try {
      await enableSite.mutateAsync({ id: site.id, enabled: !site.enabled });
      toast.success(site.enabled ? "站点已停用" : "站点已启用");
    } catch (toggleError) {
      toast.error(getSiteErrorMessage(locale, toggleError, t));
    }
  }

  async function handleDeleteSite(site: SiteRecord) {
    setDeleteConfirm({ type: "site", id: site.id, name: site.name });
  }

  async function handleArchiveSite(site: SiteRecord) {
    setDeleteConfirm({ type: "archive-site", id: site.id, name: site.name });
  }

  async function handleToggleAccount(account: SiteAccount) {
    try {
      await enableSiteAccount.mutateAsync({
        id: account.id,
        enabled: !account.enabled,
      });
      toast.success(account.enabled ? "站点账号已停用" : "站点账号已启用");
    } catch (toggleError) {
      toast.error(getSiteErrorMessage(locale, toggleError, t));
    }
  }

  async function handleDeleteAccount(account: SiteAccount) {
    setDeleteConfirm({ type: "account", id: account.id, name: account.name });
  }

  async function handleSyncAccount(account: SiteAccount) {
    setSyncingAccountIds((current) => new Set(current).add(account.id));
    try {
      const result = await syncSiteAccount.mutateAsync(account.id);
      const summary = `${result.message}（${result.group_count} 个分组，${result.token_count} 个 Key，${result.model_count} 个模型）`;
      if (result.status === "failed") {
        toast.error(summary);
      } else if (result.status === "partial") {
        toast.warning(summary);
      } else if (result.status === "success") {
        toast.success(summary);
      } else {
        console.warn(`Unexpected site sync status: ${result.status}`);
        toast.error(summary);
      }
    } catch (syncError) {
      toast.error(translateSiteMessage(locale, getErrorMessage(syncError), t));
    } finally {
      setSyncingAccountIds((current) => {
        const next = new Set(current);
        next.delete(account.id);
        return next;
      });
    }
  }

  async function handleCheckinAccount(account: SiteAccount) {
    setCheckinAccountIds((current) => new Set(current).add(account.id));
    try {
      const result = await checkinSiteAccount.mutateAsync(account.id);
      const suffix = result.reward ? `，奖励：${result.reward}` : "";
      const message = `${statusLabel(result.status)}：${result.message}${suffix}`;
      if (result.status === "failed") {
        toast.error(message);
      } else if (result.status === "skipped" || result.status === "pending") {
        toast.warning(message);
      } else if (result.checkin_capability?.support === "unknown") {
        toast.warning(t('siteCheckinCapability.inconclusive'));
      } else {
        toast.success(message);
      }
    } catch (checkinError) {
      toast.error(getSiteErrorMessage(locale, checkinError, t));
    } finally {
      setCheckinAccountIds((current) => {
        const next = new Set(current);
        next.delete(account.id);
        return next;
      });
    }
  }

  async function confirmDelete() {
    if (!deleteConfirm) return;
    if (deleteConfirm.type === "batch-site") {
      await handleBatchAction("delete");
      setDeleteConfirm(null);
      return;
    }
    try {
      if (deleteConfirm.type === "site") {
        await deleteSite.mutateAsync(deleteConfirm.id);
        toast.success("站点已删除");
        setSelectedSiteIds((prev) => prev.filter((id) => id !== deleteConfirm.id));
        setExpandedSiteIds((current) => {
          const next = new Set(current);
          next.delete(deleteConfirm.id);
          return next;
        });
      } else if (deleteConfirm.type === "archive-site") {
        await archiveSite.mutateAsync(deleteConfirm.id);
        toast.success("站点已归档，可在『归档站点』中恢复");
        setSelectedSiteIds((prev) => prev.filter((id) => id !== deleteConfirm.id));
        setExpandedSiteIds((current) => {
          const next = new Set(current);
          next.delete(deleteConfirm.id);
          return next;
        });
      } else {
        await deleteSiteAccount.mutateAsync(deleteConfirm.id);
        toast.success("站点账号已删除");
      }
    } catch (deleteError) {
      toast.error(getSiteErrorMessage(locale, deleteError, t));
    }
    setDeleteConfirm(null);
  }

  function toggleSiteSelection(siteId: number) {
    setSelectedSiteIds((prev) =>
      prev.includes(siteId) ? prev.filter((id) => id !== siteId) : [...prev, siteId],
    );
  }

  async function handleBatchAction(action: string) {
    if (selectedSiteIds.length === 0) {
      toast.error("请先选择站点");
      return;
    }
    try {
      if (action === "checkin") {
        const job = await checkinSelectedSites.mutateAsync(selectedSiteIds);
        toast.success(`已启动批量签到任务 #${job.id}`);
        return;
      }
      const result = await batchAction.mutateAsync({
        ids: selectedSiteIds,
        action,
      });
      const successCount = result.success_ids.length;
      const failedCount = result.failed_items.length;
      toast.success(`操作完成：成功 ${successCount}，失败 ${failedCount}`);
      if (action === "delete") {
        setSelectedSiteIds([]);
      }
    } catch (batchError) {
      toast.error(getSiteErrorMessage(locale, batchError, t));
    }
  }

  async function handleTogglePin(site: SiteRecord) {
    try {
      await updateSite.mutateAsync({ id: site.id, is_pinned: !site.is_pinned });
      toast.success(site.is_pinned ? "已取消置顶" : "已置顶");
    } catch (pinError) {
      toast.error(getSiteErrorMessage(locale, pinError, t));
    }
  }

  function jumpToSiteChannel(siteId: number) {
    requestJump({ kind: "site-channel-card", siteId });
  }

  function jumpToSiteChannelAccount(siteId: number, accountId: number) {
    requestJump({ kind: "site-channel-account", siteId, accountId });
  }

  return {
    selectedSiteIds,
    setSelectedSiteIds,
    deleteConfirm,
    setDeleteConfirm,
    syncingAccountIds,
    checkinAccountIds,
    enableSiteAccount,
    deleteSite,
    deleteSiteAccount,
    archiveSite,
    batchAction,
    checkinSelectedSites,
    handleToggleSite,
    handleDeleteSite,
    handleArchiveSite,
    handleToggleAccount,
    handleDeleteAccount,
    handleSyncAccount,
    handleCheckinAccount,
    confirmDelete,
    toggleSiteSelection,
    handleBatchAction,
    handleTogglePin,
    jumpToSiteChannel,
    jumpToSiteChannelAccount,
  };
}

export type SiteActions = ReturnType<typeof useSiteActions>;
