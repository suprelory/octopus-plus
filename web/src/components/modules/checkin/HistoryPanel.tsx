'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { useCheckinLogs, useCheckinStats } from '@/api/endpoints/checkin';
import type { CheckinAccount, CheckinHistoryFilter, CheckinSite } from '@/api/endpoints/checkin-types';
import { Button } from '@/components/ui/button';
import { ErrorNotice, Field, TextField, TimeText, selectClass } from './shared';

export function HistoryPanel({ sites, accounts }: { sites: CheckinSite[]; accounts: CheckinAccount[] }) {
    const t = useTranslations('checkinV2');
    const [input, setInput] = useState({
        site: '',
        account: '',
        status: '',
        source: '',
        batch: '',
        from: '',
        until: '',
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai',
    });
    const [applied, setApplied] = useState<{ filter: CheckinHistoryFilter; timezone: string }>({
        filter: {},
        timezone: input.timezone,
    });
    const [error, setError] = useState<unknown>();
    const logs = useCheckinLogs(applied.filter);
    const stats = useCheckinStats(applied.filter, applied.timezone);
    const set = (key: keyof typeof input, value: string) =>
        setInput((current) => ({ ...current, [key]: value, ...(key === 'site' ? { account: '' } : {}) }));
    const rows = logs.data?.pages.flatMap((page) => page.items) ?? [];
    return (
        <div className="grid gap-4">
            <form
                className="grid gap-3 rounded-xl border p-4 sm:grid-cols-3"
                onSubmit={(e) => {
                    e.preventDefault();
                    setError(undefined);
                    try {
                        new Intl.DateTimeFormat('en', { timeZone: input.timezone });
                        const filter: CheckinHistoryFilter = {};
                        if (input.site) filter.site_id = Number(input.site);
                        if (input.account) filter.account_id = Number(input.account);
                        if (input.status) filter.status = input.status as CheckinHistoryFilter['status'];
                        if (input.source) filter.source = input.source as CheckinHistoryFilter['source'];
                        if (input.batch) {
                            if (!/^[1-9]\d*$/.test(input.batch) || BigInt(input.batch) > BigInt('9223372036854775807'))
                                throw new Error(t('invalidID'));
                            filter.batch_id = input.batch;
                        }
                        if (input.from) filter.from = new Date(input.from).toISOString();
                        if (input.until) filter.until = new Date(input.until).toISOString();
                        if (filter.from && filter.until && filter.from >= filter.until)
                            throw new Error(t('invalidTimeRange'));
                        setApplied({ filter, timezone: input.timezone });
                    } catch (err) {
                        setError(err instanceof RangeError ? new Error(t('invalidTimezone')) : err);
                    }
                }}
            >
                <Field label={t('filterSite')}>
                    <select className={selectClass} value={input.site} onChange={(e) => set('site', e.target.value)}>
                        <option value="">{t('allSites')}</option>
                        {sites.map((site) => (
                            <option key={site.id} value={site.id}>
                                {site.name}
                            </option>
                        ))}
                    </select>
                </Field>
                <Field label={t('filterAccount')}>
                    <select
                        className={selectClass}
                        value={input.account}
                        onChange={(e) => set('account', e.target.value)}
                    >
                        <option value="">{t('allAccounts')}</option>
                        {accounts
                            .filter((account) => !input.site || account.site_id === Number(input.site))
                            .map((account) => (
                                <option key={account.id} value={account.id}>
                                    {account.name} · #{account.site_id}
                                </option>
                            ))}
                    </select>
                </Field>
                <Field label={t('status')}>
                    <select
                        className={selectClass}
                        value={input.status}
                        onChange={(e) => set('status', e.target.value)}
                    >
                        <option value="">{t('allStatuses')}</option>
                        {['success', 'failed', 'skipped', 'pending'].map((status) => (
                            <option key={status} value={status}>
                                {t(`statuses.${status}`)}
                            </option>
                        ))}
                    </select>
                </Field>
                <Field label={t('source')}>
                    <select
                        className={selectClass}
                        value={input.source}
                        onChange={(e) => set('source', e.target.value)}
                    >
                        <option value="">{t('allSources')}</option>
                        {['manual', 'scheduled', 'batch', 'import', 'auto'].map((source) => (
                            <option key={source} value={source}>
                                {t(`sources.${source}`)}
                            </option>
                        ))}
                    </select>
                </Field>
                <TextField
                    label={t('batchID')}
                    value={input.batch}
                    onChange={(e) => set('batch', e.target.value)}
                    inputMode="numeric"
                />
                <TextField
                    label={t('statsTimezone')}
                    required
                    value={input.timezone}
                    onChange={(e) => set('timezone', e.target.value)}
                />
                <TextField
                    label={t('from')}
                    type="datetime-local"
                    value={input.from}
                    onChange={(e) => set('from', e.target.value)}
                />
                <TextField
                    label={t('until')}
                    type="datetime-local"
                    value={input.until}
                    onChange={(e) => set('until', e.target.value)}
                />
                <Button type="submit" className="self-end">
                    {t('applyFilters')}
                </Button>
            </form>
            <ErrorNotice
                error={error || logs.error || stats.error}
                retry={() => {
                    void logs.refetch();
                    void stats.refetch();
                }}
            />
            {stats.data && (
                <section aria-label={t('rewards')} className="grid gap-3 sm:grid-cols-4">
                    {(['today_reward', 'recent_7_days_reward', 'recent_30_days_reward', 'total_reward'] as const).map(
                        (key) => (
                            <div className="rounded-xl border bg-card p-4" key={key}>
                                <p className="text-xs text-muted-foreground">{t(`stats.${key}`)}</p>
                                <p className="mt-2 text-xl font-semibold tabular-nums">
                                    {stats.data[key].toLocaleString(undefined, { maximumFractionDigits: 6 })}
                                </p>
                            </div>
                        ),
                    )}
                    <p className="text-xs text-muted-foreground sm:col-span-4">
                        {t('statsSummary', {
                            total: stats.data.total_count,
                            unknown: stats.data.unknown_reward_count,
                            invalid: stats.data.invalid_reward_count,
                            timezone: stats.data.timezone,
                        })}
                    </p>
                    {stats.data.by_site.length > 0 && (
                        <details className="rounded-lg border p-3 sm:col-span-4">
                            <summary className="cursor-pointer text-sm">{t('rewardsBySite')}</summary>
                            {stats.data.by_site.map((site) => (
                                <p key={site.site_id} className="mt-2 flex justify-between gap-3 text-sm">
                                    <span>{site.site_name || `#${site.site_id}`}</span>
                                    <span>
                                        {site.reward.toLocaleString(undefined, { maximumFractionDigits: 6 })} ·{' '}
                                        {t('count', { count: site.total_count })}
                                    </span>
                                </p>
                            ))}
                        </details>
                    )}
                </section>
            )}
            {logs.isPending ? (
                <p role="status">{t('loading')}</p>
            ) : rows.length === 0 ? (
                <p className="p-8 text-center text-muted-foreground">{t('emptyHistory')}</p>
            ) : (
                <div className="overflow-x-auto rounded-xl border">
                    <table className="w-full text-left text-sm">
                        <thead className="bg-muted/50">
                            <tr>
                                {['finishedAt', 'siteAccount', 'status', 'source', 'reward', 'logID'].map((key) => (
                                    <th className="p-3 font-medium" key={key}>
                                        {t(key)}
                                    </th>
                                ))}
                            </tr>
                        </thead>
                        <tbody>
                            {rows.map((row) => (
                                <tr key={row.id} className="border-t">
                                    <td className="whitespace-nowrap p-3">
                                        <TimeText value={row.finished_at} />
                                    </td>
                                    <td className="p-3">
                                        {row.site_name}
                                        <br />
                                        <span className="text-muted-foreground">{row.account_name}</span>
                                    </td>
                                    <td className="max-w-sm p-3">
                                        <span>{t(`statuses.${row.status}`)}</span>
                                        <p className="mt-1 break-words text-xs text-muted-foreground">{row.message}</p>
                                    </td>
                                    <td className="p-3">{t(`sources.${row.source}`)}</td>
                                    <td className="p-3 tabular-nums">{row.reward || '—'}</td>
                                    <td className="p-3 font-mono text-xs">
                                        {row.id}
                                        {row.batch_job_id && (
                                            <p className="mt-1">
                                                {t('batchID')}: {row.batch_job_id}
                                            </p>
                                        )}
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </div>
            )}
            {logs.hasNextPage && (
                <Button variant="outline" disabled={logs.isFetchingNextPage} onClick={() => void logs.fetchNextPage()}>
                    {t('loadMore')}
                </Button>
            )}
        </div>
    );
}
