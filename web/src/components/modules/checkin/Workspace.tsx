'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Plus, RefreshCw, Search } from 'lucide-react';
import {
    checkinAPI,
    checkinKeys,
    useCheckinAccounts,
    useCheckinCredentials,
    useCheckinMutation,
    useCheckinSites,
} from '@/api/endpoints/checkin';
import type {
    CheckinAccount,
    CheckinBalance,
    CheckinBatchSelection,
    CheckinCredential,
    CheckinResult,
    CheckinSite,
} from '@/api/endpoints/checkin-types';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { useProxyPoolDialogStore } from '@/components/modules/proxy-pool/dialog-store';
import { AccountEditor } from './AccountEditor';
import { CredentialEditor } from './CredentialEditor';
import { SiteEditor } from './SiteEditor';
import { HistoryPanel } from './HistoryPanel';
import { BatchesPanel, NotificationsPanel } from './ActivityPanels';
import { ConfirmDialog, ErrorNotice, TimeText, Toggle, selectClass } from './shared';
import { canConfirm, externalLink } from './forms';
import { isCheckinJumpTarget, useJumpStore } from '@/stores/jump';

type Tab = 'sites' | 'credentials' | 'history' | 'batches' | 'notifications';
type Editor =
    | { kind: 'site'; site?: CheckinSite }
    | { kind: 'account'; site: CheckinSite; account?: CheckinAccount; focusBalance?: boolean }
    | { kind: 'credential'; credential?: CheckinCredential };
type Confirmation = { title: string; description: string; action: () => Promise<unknown> };

export function CheckinWorkspace() {
    const t = useTranslations('checkinV2');
    const client = useQueryClient();
    const sitesQuery = useCheckinSites();
    const accountsQuery = useCheckinAccounts();
    const credentialsQuery = useCheckinCredentials();
    const jump = useJumpStore((state) => state.pending);
    const clearJump = useJumpStore((state) => state.clearPending);
    const sites = sitesQuery.data ?? [];
    const accounts = accountsQuery.data ?? [];
    const credentials = credentialsQuery.data ?? [];
    const [tab, setTab] = useState<Tab>('sites');
    const [editor, setEditor] = useState<Editor | null>(null);
    const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
    const [search, setSearch] = useState('');
    const [archived, setArchived] = useState(false);
    const [sort, setSort] = useState('default');
    const [selected, setSelected] = useState<number[]>([]);
    const [activeBatch, setActiveBatch] = useState<string | null>(null);
    const [balance, setBalance] = useState<CheckinBalance | null>(null);
    useEffect(() => {
        if (
            !jump ||
            !isCheckinJumpTarget(jump.target) ||
            !sitesQuery.data ||
            !accountsQuery.data ||
            !credentialsQuery.data
        )
            return;
        const target = jump.target;
        const site = 'siteId' in target ? sitesQuery.data.find((item) => item.id === target.siteId) : undefined;
        const account =
            'accountId' in target
                ? accountsQuery.data.find((item) => item.id === target.accountId && item.site_id === target.siteId)
                : undefined;
        const credential =
            target.kind === 'checkin-credential'
                ? credentialsQuery.data.find((item) => item.id === target.credentialId)
                : undefined;
        let canceled = false;
        queueMicrotask(() => {
            if (canceled || useJumpStore.getState().pending?.requestId !== jump.requestId) return;
            setSearch('');
            setSelected([]);
            setTab(target.kind === 'checkin-credential' ? 'credentials' : 'sites');
            if (credential) setEditor({ kind: 'credential', credential });
            else if (site) {
                setArchived(site.archived);
                if (target.kind === 'checkin-site') setEditor({ kind: 'site', site });
                else if (account)
                    setEditor({ kind: 'account', site, account, focusBalance: target.kind === 'checkin-balance' });
                else toast.error(t('referenceUnavailable'));
            } else toast.error(t('referenceUnavailable'));
            clearJump(jump.requestId);
        });
        return () => {
            canceled = true;
        };
    }, [jump, clearJump, sitesQuery.data, accountsQuery.data, credentialsQuery.data, t]);
    const action = useCheckinMutation((run: () => Promise<unknown>) => run());
    const openProxies = useProxyPoolDialogStore((state) => state.open);
    const refresh = () => {
        void client.invalidateQueries({ queryKey: checkinKeys.all });
    };
    const pending = action.isPending;
    const loading = sitesQuery.isPending || accountsQuery.isPending || credentialsQuery.isPending;
    const query = search.trim().toLocaleLowerCase();
    const visible = sites
        .filter(
            (site) =>
                site.archived === archived &&
                (!query ||
                    [
                        site.name,
                        site.base_url,
                        ...(site.tags ?? []),
                        ...accounts.filter((account) => account.site_id === site.id).map((account) => account.name),
                    ].some((value) => value.toLocaleLowerCase().includes(query))),
        )
        .sort((a, b) =>
            sort === 'name'
                ? a.name.localeCompare(b.name)
                : Number(b.is_pinned) - Number(a.is_pinned) || a.sort_order - b.sort_order || a.id - b.id,
        );
    const selection = selected.filter((id) => visible.some((site) => site.id === id && !site.archived));
    const requestDelete = (name: string, run: () => Promise<unknown>) =>
        setConfirmation({
            title: t('deleteNamed', { name }),
            description: t('deleteHint'),
            action: () => action.mutateAsync(run),
        });
    function requestBatch(scope: CheckinBatchSelection) {
        if (scope.scope === 'selected' && !scope.site_ids.length) return;
        setConfirmation({
            title: t(scope.scope === 'all' ? 'runAll' : 'runSelected'),
            description: t('batchConfirm', {
                count: scope.scope === 'all' ? sites.filter((site) => !site.archived).length : scope.site_ids.length,
            }),
            action: async () => {
                await action.mutateAsync(async () => {
                    const job = await checkinAPI.startBatch(scope);
                    setActiveBatch(job.id);
                    setTab('batches');
                    return job;
                });
            },
        });
    }
    async function execute(account: CheckinAccount, confirm: boolean) {
        try {
            await action.mutateAsync(async () => {
                const result: CheckinResult = await (confirm
                    ? checkinAPI.confirm(account.id)
                    : checkinAPI.execute(account.id));
                const message = t('executionResult', {
                    status: t(`statuses.${result.status}`),
                    reward: result.reward ?? '—',
                });
                if (result.status === 'failed') toast.error(message);
                else if (result.status === 'pending') toast.info(t('submittedPending'));
                else toast.success(result.replayed ? t('replayedResult', { result: message }) : message);
                return result;
            });
        } catch {
            /* The mutation error is rendered below and configuration is refreshed. */
        }
    }
    return (
        <div className="h-full overflow-y-auto px-2 pb-8" data-testid="independent-checkin">
            <div className="mb-5 flex flex-wrap items-center gap-2">
                <nav className="flex flex-1 flex-wrap gap-1" aria-label={t('navigation')}>
                    {(['sites', 'credentials', 'history', 'batches', 'notifications'] as const).map((item) => (
                        <Button
                            key={item}
                            variant={tab === item ? 'default' : 'ghost'}
                            size="sm"
                            aria-pressed={tab === item}
                            onClick={() => setTab(item)}
                        >
                            {t(`tabs.${item}`)}
                        </Button>
                    ))}
                </nav>
                <Button variant="outline" size="sm" onClick={() => openProxies()}>
                    {t('proxies')}
                </Button>
                <Button
                    variant="outline"
                    size="icon-sm"
                    aria-label={t('reload')}
                    disabled={sitesQuery.isFetching || accountsQuery.isFetching || credentialsQuery.isFetching}
                    onClick={refresh}
                >
                    <RefreshCw className="size-4" />
                </Button>
            </div>
            <div className="mb-4">
                <ErrorNotice
                    error={sitesQuery.error || accountsQuery.error || credentialsQuery.error}
                    retry={refresh}
                />
                <ErrorNotice error={action.error} />
            </div>
            {loading ? (
                <p role="status" className="p-8 text-center">
                    {t('loading')}
                </p>
            ) : (
                <>
                    {tab === 'sites' && (
                        <div className="grid gap-4">
                            <div className="flex flex-wrap items-center gap-3">
                                <div className="relative min-w-40 flex-1">
                                    <Search className="absolute left-3 top-2.5 size-4 text-muted-foreground" />
                                    <Input
                                        aria-label={t('searchSites')}
                                        placeholder={t('searchSites')}
                                        className="pl-9"
                                        value={search}
                                        onChange={(e) => {
                                            setSearch(e.target.value);
                                            setSelected([]);
                                        }}
                                    />
                                </div>
                                <select
                                    aria-label={t('sort')}
                                    className={`${selectClass} sm:w-auto`}
                                    value={sort}
                                    onChange={(e) => setSort(e.target.value)}
                                >
                                    <option value="default">{t('defaultSort')}</option>
                                    <option value="name">{t('nameSort')}</option>
                                </select>
                                <Toggle
                                    label={t('archivedSites')}
                                    checked={archived}
                                    onChange={(value) => {
                                        setArchived(value);
                                        setSelected([]);
                                    }}
                                />
                                <Button onClick={() => setEditor({ kind: 'site' })}>
                                    <Plus className="mr-1 size-4" />
                                    {t('createSite')}
                                </Button>
                            </div>
                            {!archived && (
                                <div className="flex flex-wrap items-center gap-3">
                                    <Toggle
                                        label={t('selectVisible')}
                                        checked={visible.length > 0 && selection.length === visible.length}
                                        onChange={(checked) =>
                                            setSelected(checked ? visible.map((site) => site.id) : [])
                                        }
                                        disabled={visible.length === 0}
                                    />
                                    <span className="text-xs text-muted-foreground">
                                        {t('selectedCount', { count: selection.length })}
                                    </span>
                                    <Button
                                        size="sm"
                                        variant="outline"
                                        disabled={!selection.length || pending}
                                        onClick={() => requestBatch({ scope: 'selected', site_ids: selection })}
                                    >
                                        {t('runSelected')}
                                    </Button>
                                    <Button
                                        size="sm"
                                        variant="outline"
                                        disabled={!sites.some((site) => !site.archived) || pending}
                                        onClick={() => requestBatch({ scope: 'all' })}
                                    >
                                        {t('runAll')}
                                    </Button>
                                </div>
                            )}
                            {balance && (
                                <div
                                    role="status"
                                    className="flex flex-wrap items-center gap-4 rounded-xl border bg-muted/30 p-3 text-sm"
                                >
                                    <span>
                                        {t('balance')}: {balance.balance}
                                    </span>
                                    <span>
                                        {t('balanceUsed')}: {balance.balance_used ?? '—'}
                                    </span>
                                    <span>
                                        {t('todayIncome')}: {balance.today_income ?? '—'}
                                    </span>
                                    <Button size="sm" variant="ghost" onClick={() => setBalance(null)}>
                                        {t('dismiss')}
                                    </Button>
                                </div>
                            )}
                            {visible.length === 0 && (
                                <div className="rounded-xl border border-dashed p-10 text-center">
                                    <h2 className="font-medium">
                                        {t(archived ? 'emptyArchive' : query ? 'noMatches' : 'emptySites')}
                                    </h2>
                                    <p className="mt-2 text-sm text-muted-foreground">{t('emptySitesHint')}</p>
                                </div>
                            )}
                            {visible.map((site) => {
                                const siteAccounts = accounts.filter((account) => account.site_id === site.id);
                                const href = externalLink(site.external_checkin_url);
                                return (
                                    <article
                                        key={site.id}
                                        className="overflow-hidden rounded-xl border bg-card"
                                        aria-label={site.name}
                                    >
                                        <header className="flex flex-wrap items-start justify-between gap-3 p-4">
                                            <div className="flex min-w-0 items-start gap-3">
                                                {!site.archived && (
                                                    <input
                                                        className="mt-1 size-4 accent-primary"
                                                        type="checkbox"
                                                        aria-label={t('selectSite', { name: site.name })}
                                                        checked={selection.includes(site.id)}
                                                        onChange={(e) =>
                                                            setSelected(
                                                                e.target.checked
                                                                    ? [...selected, site.id]
                                                                    : selected.filter((id) => id !== site.id),
                                                            )
                                                        }
                                                    />
                                                )}
                                                <div className="min-w-0">
                                                    <h2 className="break-words font-semibold">
                                                        {site.is_pinned ? '★ ' : ''}
                                                        {site.name}
                                                    </h2>
                                                    <p className="mt-1 break-all text-xs text-muted-foreground">
                                                        {site.platform} · {site.base_url}
                                                    </p>
                                                    <p className="mt-1 text-xs text-muted-foreground">
                                                        {t(site.enabled ? 'enabled' : 'disabled')} ·{' '}
                                                        {t('capability', {
                                                            support: t(`support.${site.capability.support}`),
                                                        })}{' '}
                                                        · {site.checkin_timezone} {site.checkin_window_start}–
                                                        {site.checkin_window_end}
                                                    </p>
                                                    {site.tags?.length > 0 && (
                                                        <p className="mt-2 flex flex-wrap gap-1">
                                                            {site.tags.map((tag) => (
                                                                <span
                                                                    key={tag}
                                                                    className="rounded bg-muted px-2 py-0.5 text-xs"
                                                                >
                                                                    {tag}
                                                                </span>
                                                            ))}
                                                        </p>
                                                    )}
                                                    {href && (
                                                        <a
                                                            className="mt-2 inline-block text-xs text-primary underline"
                                                            href={href}
                                                            target="_blank"
                                                            rel="noopener noreferrer"
                                                        >
                                                            {t('openSite')}
                                                        </a>
                                                    )}
                                                </div>
                                            </div>
                                            <div className="flex flex-wrap gap-2">
                                                <Button
                                                    variant="outline"
                                                    size="sm"
                                                    onClick={() => setEditor({ kind: 'site', site })}
                                                >
                                                    {t('edit')}
                                                </Button>
                                                <Button
                                                    variant="outline"
                                                    size="sm"
                                                    disabled={pending}
                                                    onClick={() =>
                                                        setConfirmation({
                                                            title: t(site.archived ? 'restoreSite' : 'archiveSite'),
                                                            description: t(
                                                                site.archived ? 'restoreHint' : 'archiveHint',
                                                            ),
                                                            action: () =>
                                                                action.mutateAsync(() =>
                                                                    checkinAPI.archiveSite(site.id, !site.archived),
                                                                ),
                                                        })
                                                    }
                                                >
                                                    {t(site.archived ? 'restore' : 'archive')}
                                                </Button>
                                                <Button
                                                    variant="outline"
                                                    size="sm"
                                                    disabled={pending || siteAccounts.length > 0}
                                                    onClick={() =>
                                                        requestDelete(site.name, () => checkinAPI.deleteSite(site.id))
                                                    }
                                                >
                                                    {t('delete')}
                                                </Button>
                                                {!site.archived && (
                                                    <Button
                                                        size="sm"
                                                        onClick={() => setEditor({ kind: 'account', site })}
                                                    >
                                                        {t('createAccount')}
                                                    </Button>
                                                )}
                                            </div>
                                        </header>
                                        <div className="border-t">
                                            {siteAccounts.length === 0 ? (
                                                <p className="p-4 text-sm text-muted-foreground">
                                                    {t('emptyAccounts')}
                                                </p>
                                            ) : (
                                                siteAccounts.map((account) => {
                                                    const awaiting =
                                                        !!account.checkin_pending?.submitted &&
                                                        !account.checkin_pending.finished;
                                                    const confirmable = canConfirm(site, account);
                                                    return (
                                                        <section
                                                            key={account.id}
                                                            aria-label={account.name}
                                                            className="grid gap-3 border-b p-4 last:border-b-0 sm:grid-cols-[1fr_auto]"
                                                        >
                                                            <div className="min-w-0">
                                                                <h3 className="text-sm font-medium">
                                                                    {account.name}{' '}
                                                                    <span className="ml-2 text-xs text-muted-foreground">
                                                                        {t(account.enabled ? 'enabled' : 'disabled')} ·{' '}
                                                                        {t(
                                                                            account.auto_checkin
                                                                                ? 'automatic'
                                                                                : 'manualOnly',
                                                                        )}
                                                                    </span>
                                                                </h3>
                                                                <p className="mt-1 text-xs text-muted-foreground">
                                                                    {t('primaryCredential')}:{' '}
                                                                    {credentials.find(
                                                                        (item) => item.id === account.credential_id,
                                                                    )?.name ?? `#${account.credential_id}`}
                                                                </p>
                                                                <p className="mt-1 text-sm">
                                                                    {account.last_checkin_status
                                                                        ? t(`statuses.${account.last_checkin_status}`)
                                                                        : t('neverCheckedIn')}
                                                                    {account.last_checkin_at && (
                                                                        <>
                                                                            {' '}
                                                                            ·{' '}
                                                                            <TimeText value={account.last_checkin_at} />
                                                                        </>
                                                                    )}
                                                                </p>
                                                                {account.last_checkin_message && (
                                                                    <p className="mt-1 break-words text-xs text-muted-foreground">
                                                                        {account.last_checkin_message}
                                                                    </p>
                                                                )}
                                                                {account.checkin_pending && (
                                                                    <p className="mt-1 break-all text-xs text-muted-foreground">
                                                                        {t(
                                                                            account.checkin_pending.submitted
                                                                                ? 'submitted'
                                                                                : 'notSubmitted',
                                                                        )}{' '}
                                                                        · {account.checkin_pending.date} · {t('logID')}:{' '}
                                                                        {account.checkin_pending.log_id}
                                                                        {awaiting && (
                                                                            <>
                                                                                {' '}
                                                                                · {t('deadline')}:{' '}
                                                                                <TimeText
                                                                                    value={
                                                                                        account.checkin_pending.deadline
                                                                                    }
                                                                                />
                                                                            </>
                                                                        )}
                                                                    </p>
                                                                )}
                                                                {account.balance_config &&
                                                                    account.balance_config.mode !== 'disabled' && (
                                                                        <p className="mt-1 text-xs">
                                                                            {t('balance')}:{' '}
                                                                            {account.balance_updated_at
                                                                                ? account.balance
                                                                                : '—'}{' '}
                                                                            · {t('lastBalanceUpdate')}:{' '}
                                                                            <TimeText
                                                                                value={account.balance_updated_at}
                                                                            />
                                                                        </p>
                                                                    )}
                                                                {account.next_auto_checkin_at && (
                                                                    <p className="mt-1 text-xs text-muted-foreground">
                                                                        {t('nextCheckin')}:{' '}
                                                                        <TimeText
                                                                            value={account.next_auto_checkin_at}
                                                                        />
                                                                    </p>
                                                                )}
                                                            </div>
                                                            <div className="flex flex-wrap items-start gap-2 sm:max-w-xs sm:justify-end">
                                                                <Button
                                                                    size="sm"
                                                                    variant="outline"
                                                                    onClick={() =>
                                                                        setEditor({ kind: 'account', site, account })
                                                                    }
                                                                >
                                                                    {t('edit')}
                                                                </Button>
                                                                <Button
                                                                    size="sm"
                                                                    variant="outline"
                                                                    disabled={pending || awaiting}
                                                                    onClick={() =>
                                                                        requestDelete(account.name, () =>
                                                                            checkinAPI.deleteAccount(account.id),
                                                                        )
                                                                    }
                                                                >
                                                                    {t('delete')}
                                                                </Button>
                                                                <Button
                                                                    size="sm"
                                                                    variant="outline"
                                                                    disabled={
                                                                        pending ||
                                                                        !account.enabled ||
                                                                        !site.enabled ||
                                                                        site.archived ||
                                                                        !account.balance_config ||
                                                                        account.balance_config.mode === 'disabled'
                                                                    }
                                                                    onClick={async () => {
                                                                        try {
                                                                            await action.mutateAsync(async () => {
                                                                                const result = await checkinAPI.balance(
                                                                                    account.id,
                                                                                );
                                                                                setBalance(result);
                                                                                return result;
                                                                            });
                                                                        } catch {
                                                                            /* rendered by ErrorNotice */
                                                                        }
                                                                    }}
                                                                >
                                                                    {t('refreshBalance')}
                                                                </Button>
                                                                {(awaiting || confirmable) && (
                                                                    <Button
                                                                        size="sm"
                                                                        disabled={
                                                                            pending ||
                                                                            !account.enabled ||
                                                                            !site.enabled ||
                                                                            site.archived ||
                                                                            !confirmable
                                                                        }
                                                                        onClick={() => void execute(account, true)}
                                                                    >
                                                                        {t('confirmCheckin')}
                                                                    </Button>
                                                                )}
                                                                {!awaiting && (
                                                                    <Button
                                                                        size="sm"
                                                                        disabled={
                                                                            pending ||
                                                                            !account.enabled ||
                                                                            !site.enabled ||
                                                                            site.archived ||
                                                                            !site.capability.can_verify
                                                                        }
                                                                        onClick={() => void execute(account, false)}
                                                                    >
                                                                        {t('execute')}
                                                                    </Button>
                                                                )}
                                                            </div>
                                                        </section>
                                                    );
                                                })
                                            )}
                                        </div>
                                    </article>
                                );
                            })}
                        </div>
                    )}
                    {tab === 'credentials' && (
                        <div className="grid gap-4">
                            <div className="flex flex-wrap items-center justify-between gap-3">
                                <p className="text-sm text-muted-foreground">{t('credentialsHint')}</p>
                                <Button onClick={() => setEditor({ kind: 'credential' })}>
                                    {t('createCredential')}
                                </Button>
                            </div>
                            {credentials.length === 0 && (
                                <p className="p-8 text-center text-muted-foreground">{t('emptyCredentials')}</p>
                            )}
                            {credentials.map((credential) => (
                                <article
                                    key={credential.id}
                                    aria-label={credential.name}
                                    className="flex flex-wrap justify-between gap-4 rounded-xl border bg-card p-4"
                                >
                                    <div className="min-w-0 flex-1">
                                        <h2 className="font-medium">{credential.name}</h2>
                                        <p className="mt-1 break-all text-xs text-muted-foreground">
                                            {t(`types.${credential.type}`)} · {credential.platform} ·{' '}
                                            {credential.auth_base_url}
                                        </p>
                                        <p className="mt-2 text-sm">
                                            {t(credential.configured ? 'configured' : 'needsConfiguration')} ·{' '}
                                            {t(`sessionStates.${credential.session_state}`)}
                                        </p>
                                        <p className="mt-1 text-xs text-muted-foreground">
                                            {t('credentialReferences', {
                                                primary: credential.primary_reference_count,
                                                balance: credential.balance_reference_count,
                                            })}
                                        </p>
                                    </div>
                                    <div className="flex gap-2">
                                        <Button
                                            variant="outline"
                                            size="sm"
                                            onClick={() => setEditor({ kind: 'credential', credential })}
                                        >
                                            {t('edit')}
                                        </Button>
                                        <Button
                                            variant="outline"
                                            size="sm"
                                            disabled={pending || credential.reference_count > 0}
                                            onClick={() =>
                                                requestDelete(credential.name, () =>
                                                    checkinAPI.deleteCredential(credential.id),
                                                )
                                            }
                                        >
                                            {t('delete')}
                                        </Button>
                                    </div>
                                </article>
                            ))}
                        </div>
                    )}
                    {tab === 'history' && <HistoryPanel sites={sites} accounts={accounts} />}
                    {tab === 'batches' && <BatchesPanel activeID={activeBatch} />}
                    {tab === 'notifications' && <NotificationsPanel />}
                </>
            )}
            {editor?.kind === 'site' && <SiteEditor site={editor.site} onClose={() => setEditor(null)} />}
            {editor?.kind === 'account' && (
                <AccountEditor
                    site={editor.site}
                    account={editor.account}
                    focusBalance={editor.focusBalance}
                    credentials={credentials}
                    onClose={() => setEditor(null)}
                />
            )}
            {editor?.kind === 'credential' && (
                <CredentialEditor credential={editor.credential} onClose={() => setEditor(null)} />
            )}
            {confirmation && <ConfirmDialog {...confirmation} onClose={() => setConfirmation(null)} />}
        </div>
    );
}
