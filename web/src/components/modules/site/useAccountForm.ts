'use client';

import { useCallback, useMemo, useState, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';
import { toast } from '@/components/common/Toast';
import { useSettingStore } from '@/stores/setting';
import {
    Site as SiteRecord,
    SiteAccount,
    SiteCredentialType,
    SitePlatform,
    useCreateSiteAccount,
    useUpdateSiteAccount,
    useSiteList,
} from '@/api/endpoints/site';
import { translateSiteMessage } from './site-message';
import {
    type SiteAccountFormState,
    createAccountForm,
    createEmptyAccountForm,
    credentialOptions,
    parseTokenExpiresAtInput,
    getErrorMessage,
} from './account-form';

export function useAccountForm({ site, account, onOpenChange }: {
    site: SiteRecord | null;
    account: SiteAccount | null;
    onOpenChange: (open: boolean) => void;
}) {
    const t = useTranslations();
    const tProxy = useTranslations('proxyPool');
    const locale = useSettingStore((state) => state.locale);
    const createSiteAccount = useCreateSiteAccount();
    const updateSiteAccount = useUpdateSiteAccount();
    const { data: sites } = useSiteList();
    const [accountForm, setAccountForm] = useState<SiteAccountFormState | null>(() => {
        if (account) {
            const form = createAccountForm(account);
            if (site?.kind === 'checkin') {
                if (site.checkin_http_enabled) form.credential_type = site.checkin_use_subscription_token ? SiteCredentialType.LinkedAccount : SiteCredentialType.Cookie;
                else if (site.linked_site_id || account.linked_account_id || account.credential_type === SiteCredentialType.LinkedAccount) form.credential_type = SiteCredentialType.LinkedAccount;
            }
            return form;
        }
        if (site) return createEmptyAccountForm(site);
        return null;
    });

    const currentPlatform = site?.platform ?? SitePlatform.NewAPI;
    const useSubscriptionToken = site?.kind === 'checkin' && !!site.checkin_http_enabled && !!site.checkin_use_subscription_token;
    const currentCredentialOptions = useMemo(
        () => site?.kind === 'checkin'
            ? site.checkin_http_enabled ? [useSubscriptionToken ? SiteCredentialType.LinkedAccount : SiteCredentialType.Cookie]
                : site.linked_site_id || !account || account.credential_type === SiteCredentialType.LinkedAccount
                    ? [SiteCredentialType.LinkedAccount]
                    : credentialOptions(currentPlatform).filter((type) => type !== SiteCredentialType.APIKey)
            : credentialOptions(currentPlatform),
        [currentPlatform, site, account, useSubscriptionToken],
    );
    const linkedAccounts = useMemo(() => {
        const linkedSite = sites?.find((item) => item.id === site?.linked_site_id);
        if (useSubscriptionToken) {
            return linkedSite?.platform === SitePlatform.Sub2API
                ? (linkedSite.accounts ?? []).filter((item) => item.credential_type === SiteCredentialType.AccessToken)
                : [];
        }
        if (site?.checkin_http_enabled && linkedSite?.platform === SitePlatform.API) return [];
        return (linkedSite?.accounts ?? []).filter((item) => {
            if (site?.checkin_http_enabled && linkedSite?.platform === SitePlatform.Sub2API) {
                return item.credential_type === SiteCredentialType.AccessToken || item.credential_type === SiteCredentialType.APIKey;
            }
            return item.credential_type === SiteCredentialType.AccessToken || item.credential_type === SiteCredentialType.UsernamePassword;
        });
    }, [sites, site?.linked_site_id, site?.checkin_http_enabled, useSubscriptionToken]);

    const handleSubmit = useCallback(
        async (event: FormEvent<HTMLFormElement>) => {
            event.preventDefault();
            if (!site || !accountForm) {
                toast.error('站点上下文不存在');
                return;
            }
            if (!accountForm.name.trim()) {
                toast.error('请输入账号名称');
                return;
            }

            if (accountForm.credential_type === SiteCredentialType.Cookie) {
                const cookie = accountForm.cookie.trim();
                if (!cookie || /[\r\n\0]/.test(cookie) || new TextEncoder().encode(cookie).length > 16 * 1024) {
                    toast.error('请输入签到站的 Cookie，须为不超过 16 KiB 的单行内容');
                    return;
                }
            }
            if (accountForm.credential_type === SiteCredentialType.LinkedAccount &&
                (!site.linked_site_id || !linkedAccounts.some((item) => item.id === accountForm.linked_account_id))) {
                toast.error(useSubscriptionToken ? '请选择关联 Sub2API 订阅站的 Access Token 账号' : '请先关联订阅站，再选择用于平台签到的订阅账号');
                return;
            }
            if (accountForm.credential_type === SiteCredentialType.Cookie && accountForm.linked_account_id !== null &&
                !linkedAccounts.some((item) => item.id === accountForm.linked_account_id)) {
                toast.error('请选择关联订阅站中用于查询余额的账号，或取消余额账号关联');
                return;
            }

            if (accountForm.credential_type === SiteCredentialType.UsernamePassword) {
                if (!accountForm.username.trim() || !accountForm.password.trim()) {
                    toast.error('用户名和密码不能为空');
                    return;
                }
            }
            if (
                accountForm.credential_type === SiteCredentialType.AccessToken &&
                !accountForm.access_token.trim()
            ) {
                toast.error('请输入 Access Token');
                return;
            }
            if (
                accountForm.credential_type === SiteCredentialType.APIKey &&
                !accountForm.api_key.trim()
            ) {
                toast.error('请输入 API Key');
                return;
            }
            if (accountForm.auto_checkin) {
                if (
                    !Number.isFinite(accountForm.checkin_interval_hours) ||
                    accountForm.checkin_interval_hours < 1 ||
                    accountForm.checkin_interval_hours > 720
                ) {
                    toast.error('签到间隔必须在 1 到 720 小时之间');
                    return;
                }
                if (accountForm.random_checkin && (
                    !Number.isFinite(accountForm.checkin_random_window_minutes) ||
                    accountForm.checkin_random_window_minutes < 0 ||
                    accountForm.checkin_random_window_minutes > 1440
                )) {
                    toast.error('随机延迟窗口必须在 0 到 1440 分钟之间');
                    return;
                }
            }

            const shouldIncludePlatformUserID =
                currentPlatform === SitePlatform.NewAPI &&
                accountForm.credential_type === SiteCredentialType.AccessToken;
            const platformUserIDInput = shouldIncludePlatformUserID
                ? accountForm.platform_user_id.trim()
                : '';
            if (shouldIncludePlatformUserID && !platformUserIDInput) {
                toast.error('请输入 Platform User ID');
                return;
            }

            const parsedPlatformUserID = platformUserIDInput
                ? Number(platformUserIDInput)
                : null;
            if (
                shouldIncludePlatformUserID &&
                parsedPlatformUserID !== null &&
                (!Number.isInteger(parsedPlatformUserID) || parsedPlatformUserID <= 0)
            ) {
                toast.error('Platform User ID 必须是大于 0 的整数');
                return;
            }

            let parsedTokenExpiresAt = 0;
            try {
                parsedTokenExpiresAt = parseTokenExpiresAtInput(accountForm.token_expires_at);
            } catch (error) {
                toast.error(translateSiteMessage(locale, getErrorMessage(error), t));
                return;
            }

            const trimmedAccessToken =
                accountForm.credential_type === SiteCredentialType.AccessToken
                    ? accountForm.access_token.trim()
                    : '';
            const trimmedAPIKey =
                accountForm.credential_type === SiteCredentialType.APIKey
                    ? accountForm.api_key.trim()
                    : '';
            const isUsernamePassword =
                accountForm.credential_type === SiteCredentialType.UsernamePassword;
            const isAccessToken =
                accountForm.credential_type === SiteCredentialType.AccessToken;

            if (accountForm.proxy_mode === 'pool' && !accountForm.proxy_config_id) {
                toast.error(tProxy('selectRequired'));
                return;
            }

            const payload = {
                site_id: accountForm.site_id,
                name: accountForm.name.trim(),
                credential_type: accountForm.credential_type,
                username: isUsernamePassword ? accountForm.username.trim() : '',
                password: isUsernamePassword ? accountForm.password.trim() : '',
                access_token: trimmedAccessToken,
                api_key: trimmedAPIKey,
                refresh_token: isAccessToken ? accountForm.refresh_token.trim() : '',
                cookie: accountForm.credential_type === SiteCredentialType.Cookie ? accountForm.cookie.trim() : '',
                linked_account_id: accountForm.credential_type === SiteCredentialType.LinkedAccount || accountForm.credential_type === SiteCredentialType.Cookie
                    ? accountForm.linked_account_id : null,
                token_expires_at: isAccessToken ? parsedTokenExpiresAt : 0,
                platform_user_id: shouldIncludePlatformUserID ? parsedPlatformUserID : null,
                proxy_mode: accountForm.proxy_mode,
                proxy_config_id:
                    accountForm.proxy_mode === 'pool' ? accountForm.proxy_config_id : null,
                enabled: accountForm.enabled,
                auto_sync: site.kind !== 'checkin' && accountForm.auto_sync,
                auto_checkin: site.kind === 'checkin' && accountForm.auto_checkin,
                random_checkin: accountForm.random_checkin,
                checkin_interval_hours: Math.max(
                    1,
                    Math.trunc(accountForm.checkin_interval_hours || 24),
                ),
                checkin_random_window_minutes: Math.max(
                    0,
                    Math.trunc(accountForm.checkin_random_window_minutes || 0),
                ),
            };

            try {
                if (account) {
                    await updateSiteAccount.mutateAsync({ id: account.id, ...payload });
                    toast.success('站点账号已更新');
                } else {
                    await createSiteAccount.mutateAsync(payload);
                    toast.success('站点账号已创建');
                }
                onOpenChange(false);
            } catch (submitError) {
                toast.error(translateSiteMessage(locale, getErrorMessage(submitError), t));
            }
        },
        [
            site,
            account,
            accountForm,
            currentPlatform,
            linkedAccounts,
            useSubscriptionToken,
            tProxy,
            updateSiteAccount,
            createSiteAccount,
            onOpenChange,
            locale,
            t,
        ],
    );

    const isPending = createSiteAccount.isPending || updateSiteAccount.isPending;
    return { accountForm, setAccountForm, currentPlatform, currentCredentialOptions, linkedAccounts, useSubscriptionToken, handleSubmit, isPending };
}
