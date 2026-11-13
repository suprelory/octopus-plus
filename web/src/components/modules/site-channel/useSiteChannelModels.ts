'use client';

import { useEnableSiteAccount } from '@/api/endpoints/site';
import {
    type SiteChannelAccount,
    type SiteChannelGroup,
    type SiteModelDisableUpdateRequest,
    type SiteModelRouteType,
    type SiteModelRouteUpdateRequest,
    useDeleteSiteManualModel,
    useResetSiteChannelModelRoutes,
    useUpdateSiteChannelModelDisabled,
    useUpdateSiteChannelModelRoutes,
    useUpdateSiteGroupProjection,
} from '@/api/endpoints/site-channel';
import { toast } from '@/components/common/Toast';
import { getErrorMessage } from '@/lib/errors';
import { useSettingStore } from '@/stores/setting';
import { useTranslations } from 'next-intl';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { translateSiteMessage } from '../site/site-message';
import { isSupportedRouteType } from './constants';
import {
    QUICK_FILTER_OPTIONS,
    SITE_GROUP_FILTER_ALL_VALUE,
    STALE_MODEL_SYNC_STATUSES,
    addPendingKeys,
    getBaseGroupKey,
    makeModelKey,
    matchesQuickFilters,
    removeKeys,
    removePendingKeys,
    sortModels,
} from './model-view';
import { SiteChannelTableHandle } from './SiteChannelTable';
import { SiteChannelPendingJump } from './types';
import {
    DEFAULT_SITE_CHANNEL_PANEL_PREFERENCES,
    type SiteChannelQuickFilter,
    type SiteChannelTableSort,
    type SiteChannelTableSortField,
    useSiteChannelPanelViewStore,
} from './ui-store';
import {
    SITE_GROUP_FILTER_ALL,
    type SiteChannelGroupFilter,
    type SiteModelView,
    createGroupFilter,
    filterGroups,
    flattenAccountModels,
    isSameGroupFilter,
} from './utils';

export function useSiteChannelModels({
    siteId,
    account,
    jumpRequest,
    onJumpHandled,
    pendingGroupChanges,
}: {
    siteId: number;
    account: SiteChannelAccount;
    jumpRequest: SiteChannelPendingJump | null;
    onJumpHandled: (requestId: number) => void;
    pendingGroupChanges: boolean;
}) {
    const t = useTranslations();

    const locale = useSettingStore((state) => state.locale);

    const [activeFilter, setActiveFilter] = useState<SiteChannelGroupFilter>(SITE_GROUP_FILTER_ALL);

    const [pendingRouteOverrides, setPendingRouteOverrides] = useState<Record<string, SiteModelRouteType>>({});

    const [pendingDisabledOverrides, setPendingDisabledOverrides] = useState<Record<string, boolean>>({});

    const [pendingModelKeys, setPendingModelKeys] = useState<Set<string>>(new Set());

    const [selectedModelKeys, setSelectedModelKeys] = useState<Set<string>>(new Set());

    const [highlightedModelKey, setHighlightedModelKey] = useState<string | null>(null);

    const [modelSearchTerm, setModelSearchTerm] = useState('');

    const [bulkMoveTarget, setBulkMoveTarget] = useState<SiteModelRouteType>('openai_chat');

    const [deletingManualModelKey, setDeletingManualModelKey] = useState<string | null>(null);

    const tableHandleRef = useRef<SiteChannelTableHandle | null>(null);

    const panelKey = `${siteId}:${account.account_id}`;

    const panelPreferences = useSiteChannelPanelViewStore(
        (state) => state.panels[panelKey] ?? DEFAULT_SITE_CHANNEL_PANEL_PREFERENCES,
    );

    const setCompactMode = useSiteChannelPanelViewStore((state) => state.setCompactMode);

    const setQuickFilters = useSiteChannelPanelViewStore((state) => state.setQuickFilters);

    const setTableSort = useSiteChannelPanelViewStore((state) => state.setTableSort);

    const groupProjectionMutation = useUpdateSiteGroupProjection(siteId, account.account_id);

    const deleteManualModelMutation = useDeleteSiteManualModel(siteId, account.account_id);

    const routeMutation = useUpdateSiteChannelModelRoutes(siteId, account.account_id);

    const disabledMutation = useUpdateSiteChannelModelDisabled();

    const resetMutation = useResetSiteChannelModelRoutes(siteId, account.account_id);

    const enableSiteAccount = useEnableSiteAccount();

    const translateSiteError = useCallback(
        (error: unknown, fallback: string) => translateSiteMessage(locale, getErrorMessage(error, fallback), t),
        [locale, t],
    );

    const forcedModelKey =
        jumpRequest?.target.kind === 'site-channel-model' &&
        jumpRequest.target.siteId === siteId &&
        jumpRequest.target.accountId === account.account_id
            ? makeModelKey(getBaseGroupKey(jumpRequest.target.groupKey), jumpRequest.target.modelName)
            : null;

    const visibleGroups = useMemo(() => filterGroups(account.groups, activeFilter), [account.groups, activeFilter]);

    const scopedModels = useMemo(() => {
        return flattenAccountModels(account, activeFilter).map((model) => {
            const modelKey = makeModelKey(model.group_key, model.model_name);
            const nextRouteType = pendingRouteOverrides[modelKey];
            const nextDisabled = pendingDisabledOverrides[modelKey];

            return {
                ...model,
                route_type: nextRouteType ?? model.route_type,
                route_source: nextRouteType ? 'manual_override' : model.route_source,
                manual_override: nextRouteType ? true : model.manual_override,
                disabled: nextDisabled ?? model.disabled,
            };
        });
    }, [account, activeFilter, pendingRouteOverrides, pendingDisabledOverrides]);

    const filteredModels = useMemo(() => {
        const normalizedSearch = modelSearchTerm.trim().toLowerCase();

        return scopedModels.filter((model) => {
            const modelKey = makeModelKey(model.group_key, model.model_name);
            // Pin the jump target across the whole highlight window: forcedModelKey holds it
            // while jumpRequest is live, then highlightedModelKey keeps it pinned after the
            // request is cleared until the ring fades (~1.8s). Without this the row would be
            // dropped the instant onJumpHandled clears jumpRequest when an active search /
            // quick-filter excludes it, leaving the highlight on an unmounted row.
            if (forcedModelKey === modelKey || highlightedModelKey === modelKey) return true;

            const matchesSearch =
                !normalizedSearch ||
                model.model_name.toLowerCase().includes(normalizedSearch) ||
                (model.group_name || model.group_key).toLowerCase().includes(normalizedSearch);

            if (!matchesSearch) return false;

            return matchesQuickFilters(model, panelPreferences.quickFilters);
        });
    }, [scopedModels, modelSearchTerm, panelPreferences.quickFilters, forcedModelKey, highlightedModelKey]);

    const visibleModels = useMemo(
        () => sortModels(filteredModels, panelPreferences.tableSort),
        [filteredModels, panelPreferences.tableSort],
    );

    const visibleModelMap = useMemo(
        () => new Map(visibleModels.map((model) => [makeModelKey(model.group_key, model.model_name), model] as const)),
        [visibleModels],
    );

    // Scope key for the models list; changing filter / search / quick-filters tells the
    // virtualized table to scroll back to the top (see SiteChannelTableView resetKey).
    const modelsScopeKey = `${account.account_id}|${activeFilter.kind}|${activeFilter.kind === 'group' ? activeFilter.groupKey : ''}|${modelSearchTerm}|${panelPreferences.quickFilters.join(',')}`;

    const selectedModels = useMemo(
        () =>
            Array.from(selectedModelKeys)
                .map((key) => visibleModelMap.get(key))
                .filter((model): model is SiteModelView => !!model),
        [selectedModelKeys, visibleModelMap],
    );

    const hasPendingChanges =
        pendingModelKeys.size > 0 ||
        routeMutation.isPending ||
        disabledMutation.isPending ||
        pendingGroupChanges ||
        deleteManualModelMutation.isPending;

    useEffect(() => {
        if (!jumpRequest || jumpRequest.target.kind !== 'site-channel-model') return;
        const target = jumpRequest.target;
        if (target.siteId !== siteId || target.accountId !== account.account_id) return;

        const targetGroupKey = getBaseGroupKey(target.groupKey);
        const targetFilter = createGroupFilter(targetGroupKey);
        if (!isSameGroupFilter(activeFilter, targetFilter)) {
            const frameId = window.requestAnimationFrame(() => {
                setActiveFilter(targetFilter);
            });
            return () => window.cancelAnimationFrame(frameId);
        }

        const modelKey = makeModelKey(targetGroupKey, target.modelName);

        const timer = window.setTimeout(() => {
            // forcedModelKey keeps the target in visibleModels even when it doesn't match
            // the active search / quick-filters, so the virtualizer can always find it.
            tableHandleRef.current?.scrollToModelKey(modelKey);
            setHighlightedModelKey(modelKey);
            window.setTimeout(() => {
                setHighlightedModelKey((current) => (current === modelKey ? null : current));
            }, 1800);
            onJumpHandled(jumpRequest.requestId);
        }, 80);

        return () => window.clearTimeout(timer);
    }, [jumpRequest, siteId, account.account_id, activeFilter, onJumpHandled]);

    const setSelectionForKeys = useCallback((modelKeys: string[], checked: boolean) => {
        if (modelKeys.length === 0) return;

        setSelectedModelKeys((current) => {
            const next = new Set(current);
            for (const modelKey of modelKeys) {
                if (checked) {
                    next.add(modelKey);
                } else {
                    next.delete(modelKey);
                }
            }
            return next;
        });
    }, []);

    const handleToggleModelSelection = useCallback(
        (modelKey: string, checked: boolean) => {
            setSelectionForKeys([modelKey], checked);
        },
        [setSelectionForKeys],
    );

    const handleToggleAllVisible = useCallback(
        (checked: boolean) => {
            setSelectionForKeys(
                visibleModels.map((model) => makeModelKey(model.group_key, model.model_name)),
                checked,
            );
        },
        [visibleModels, setSelectionForKeys],
    );

    const allVisibleSelected = useMemo(
        () =>
            visibleModels.length > 0 &&
            visibleModels.every((model) => selectedModelKeys.has(makeModelKey(model.group_key, model.model_name))),
        [visibleModels, selectedModelKeys],
    );

    const applyRouteChange = useCallback(
        (models: SiteModelView[], nextRouteType: SiteModelRouteType) => {
            const eligibleModels = models.filter((model) => {
                const modelKey = makeModelKey(model.group_key, model.model_name);
                return !pendingModelKeys.has(modelKey) && !model.disabled && model.route_type !== nextRouteType;
            });

            if (eligibleModels.length === 0) return;

            const modelKeys = eligibleModels.map((model) => makeModelKey(model.group_key, model.model_name));
            const payload: SiteModelRouteUpdateRequest[] = eligibleModels.map((model) => ({
                group_key: model.group_key,
                model_name: model.model_name,
                route_type: nextRouteType,
            }));

            setPendingRouteOverrides((current) => {
                const next = { ...current };
                for (const modelKey of modelKeys) {
                    next[modelKey] = nextRouteType;
                }
                return next;
            });
            setPendingModelKeys((current) => addPendingKeys(current, modelKeys));

            routeMutation.mutate(payload, {
                onSuccess: () => {
                    setPendingRouteOverrides((current) => removeKeys(current, modelKeys));
                    toast.success(
                        payload.length === 1
                            ? '模型请求端点格式已更新'
                            : `已更新 ${payload.length} 个模型的请求端点格式`,
                    );
                },
                onError: (error) => {
                    setPendingRouteOverrides((current) => removeKeys(current, modelKeys));
                    toast.error(translateSiteError(error, '更新模型请求端点格式失败'));
                },
                onSettled: () => {
                    setPendingModelKeys((current) => removePendingKeys(current, modelKeys));
                },
            });
        },
        [pendingModelKeys, routeMutation, translateSiteError],
    );

    const applyDisabledChange = useCallback(
        (models: SiteModelView[], nextDisabled: boolean) => {
            const eligibleModels = models.filter((model) => {
                const modelKey = makeModelKey(model.group_key, model.model_name);
                return !pendingModelKeys.has(modelKey) && model.disabled !== nextDisabled;
            });

            if (eligibleModels.length === 0) return;

            const modelKeys = eligibleModels.map((model) => makeModelKey(model.group_key, model.model_name));
            const payload: SiteModelDisableUpdateRequest[] = eligibleModels.map((model) => ({
                group_key: model.group_key,
                model_name: model.model_name,
                disabled: nextDisabled,
            }));

            setPendingDisabledOverrides((current) => {
                const next = { ...current };
                for (const modelKey of modelKeys) {
                    next[modelKey] = nextDisabled;
                }
                return next;
            });
            setPendingModelKeys((current) => addPendingKeys(current, modelKeys));

            disabledMutation.mutate(
                { siteId, accountId: account.account_id, payload },
                {
                    onSuccess: () => {
                        setPendingDisabledOverrides((current) => removeKeys(current, modelKeys));
                        toast.success(
                            payload.length === 1
                                ? nextDisabled
                                    ? '模型已禁用'
                                    : '模型已启用'
                                : `${payload.length} 个模型已${nextDisabled ? '禁用' : '启用'}`,
                        );
                    },
                    onError: (error) => {
                        setPendingDisabledOverrides((current) => removeKeys(current, modelKeys));
                        toast.error(translateSiteError(error, '更新模型禁用状态失败'));
                    },
                    onSettled: () => {
                        setPendingModelKeys((current) => removePendingKeys(current, modelKeys));
                    },
                },
            );
        },
        [pendingModelKeys, disabledMutation, siteId, account.account_id, translateSiteError],
    );

    const handleToggleGroupProjection = (group: SiteChannelGroup) => {
        const nextDisabled = !group.projection_disabled;
        groupProjectionMutation.mutate(
            {
                group_key: group.group_key,
                projection_disabled: nextDisabled,
            },
            {
                onSuccess: () => {
                    toast.success(nextDisabled ? '已停止生成该分组的投影渠道' : '已恢复生成该分组的投影渠道');
                },
                onError: (error) => {
                    toast.error(translateSiteError(error, '更新分组投影状态失败'));
                },
            },
        );
    };

    const handleDeleteManualModel = (model: SiteModelView) => {
        if (model.source !== 'manual') return;
        const modelKey = makeModelKey(model.group_key, model.model_name);
        if (deletingManualModelKey === modelKey) return;
        setDeletingManualModelKey(modelKey);
        deleteManualModelMutation.mutate(
            { group_key: model.group_key, model_name: model.model_name },
            {
                onSuccess: () => toast.success('自定义模型已删除'),
                onError: (error) => toast.error(translateSiteError(error, '删除自定义模型失败')),
                onSettled: () => setDeletingManualModelKey((current) => (current === modelKey ? null : current)),
            },
        );
    };

    const handleToggleDisabled = (model: SiteModelView) => {
        applyDisabledChange([model], !model.disabled);
    };

    const handleResetRoutes = () => {
        resetMutation.mutate(undefined, {
            onSuccess: () => {
                setPendingRouteOverrides({});
                toast.success('模型请求端点格式已重置');
            },
            onError: (error) => {
                toast.error(translateSiteError(error, '重置模型端点格式失败'));
            },
        });
    };

    const toggleQuickFilter = (filter: SiteChannelQuickFilter) => {
        const next = panelPreferences.quickFilters.includes(filter)
            ? panelPreferences.quickFilters.filter((item) => item !== filter)
            : QUICK_FILTER_OPTIONS.map((item) => item.key).filter(
                  (key) => key === filter || panelPreferences.quickFilters.includes(key),
              );

        setQuickFilters(panelKey, next);
    };

    const handleSortChange = (field: SiteChannelTableSortField) => {
        const nextSort: SiteChannelTableSort = {
            field,
            order:
                panelPreferences.tableSort.field === field && panelPreferences.tableSort.order === 'asc'
                    ? 'desc'
                    : 'asc',
        };
        setTableSort(panelKey, nextSort);
    };

    const selectedVisibleCount = selectedModels.length;

    const activeGroupValue = activeFilter.kind === 'all' ? SITE_GROUP_FILTER_ALL_VALUE : activeFilter.groupKey;

    const activeGroup =
        activeFilter.kind === 'group'
            ? (account.groups.find((group) => group.group_key === activeFilter.groupKey) ?? null)
            : null;

    const activeGroupLabel = activeGroup ? activeGroup.group_name || activeGroup.group_key : '全部分组';

    const activeGroupProjectionSuspended = activeGroup?.projection_suspended === true;

    const activeGroupProjectionStale =
        activeGroup &&
        !activeGroupProjectionSuspended &&
        STALE_MODEL_SYNC_STATUSES.includes(activeGroup.model_sync_status);

    const activeGroupSuspensionReason = activeGroup?.projection_suspend_reason || activeGroup?.model_sync_message || '';

    const activeGroupStaleReason = activeGroup?.model_sync_message || '';

    const activeQuickFilterCount = panelPreferences.quickFilters.length;

    const pendingKeyGroups = useMemo(() => visibleGroups.filter((group) => !group.has_keys), [visibleGroups]);

    const projectedGroups = useMemo(
        () => visibleGroups.filter((group) => group.has_projected_channel),
        [visibleGroups],
    );

    const unsupportedRouteCount = useMemo(
        () => visibleModels.filter((model) => !isSupportedRouteType(model.route_type)).length,
        [visibleModels],
    );

    const handleGroupFilterChange = useCallback((value: string) => {
        setActiveFilter(value === SITE_GROUP_FILTER_ALL_VALUE ? SITE_GROUP_FILTER_ALL : createGroupFilter(value));
    }, []);

    const handleClearQuickFilters = useCallback(() => {
        setQuickFilters(panelKey, []);
    }, [panelKey, setQuickFilters]);

    const handleFocusAttention = useCallback(() => {
        if (panelPreferences.quickFilters.includes('attention')) return;
        const next = QUICK_FILTER_OPTIONS.map((item) => item.key).filter(
            (key) => key === 'attention' || panelPreferences.quickFilters.includes(key),
        );
        setQuickFilters(panelKey, next);
    }, [panelKey, panelPreferences.quickFilters, setQuickFilters]);
    return {
        pendingModelKeys,
        selectedModelKeys,
        setSelectedModelKeys,
        highlightedModelKey,
        modelSearchTerm,
        setModelSearchTerm,
        bulkMoveTarget,
        setBulkMoveTarget,
        tableHandleRef,
        panelKey,
        panelPreferences,
        setCompactMode,
        groupProjectionMutation,
        resetMutation,
        enableSiteAccount,
        visibleGroups,
        visibleModels,
        modelsScopeKey,
        selectedModels,
        hasPendingChanges,
        handleToggleModelSelection,
        handleToggleAllVisible,
        allVisibleSelected,
        applyRouteChange,
        applyDisabledChange,
        handleToggleGroupProjection,
        handleDeleteManualModel,
        handleToggleDisabled,
        handleResetRoutes,
        toggleQuickFilter,
        handleSortChange,
        selectedVisibleCount,
        activeGroupValue,
        activeGroup,
        activeGroupLabel,
        activeGroupProjectionSuspended,
        activeGroupProjectionStale,
        activeGroupSuspensionReason,
        activeGroupStaleReason,
        activeQuickFilterCount,
        pendingKeyGroups,
        projectedGroups,
        unsupportedRouteCount,
        handleGroupFilterChange,
        handleClearQuickFilters,
        handleFocusAttention,
    };
}

export type SiteChannelModelState = ReturnType<typeof useSiteChannelModels>;
