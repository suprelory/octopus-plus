'use client';

import { useState } from 'react';
import { ChartNoAxesCombined, RefreshCw } from 'lucide-react';
import { useFormatter, useTranslations } from 'next-intl';
import type { Site } from '@/api/endpoints/site';
import { useSiteCheckinStats } from '@/api/endpoints/site-checkin';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';

export function CheckinStatsPanel({ sites }: { sites: Site[] }) {
    const t = useTranslations('siteCheckinStats');
    const format = useFormatter();
    const [siteID, setSiteID] = useState('all');
    const [accountID, setAccountID] = useState('all');
    const [timezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone);
    const filters: Record<string, string | number> = { timezone };
    if (siteID !== 'all') filters.site_id = siteID;
    if (accountID !== 'all') filters.account_id = accountID;
    const query = useSiteCheckinStats(filters);
    const stats = query.data;
    const accounts = sites.filter(site => siteID === 'all' || String(site.id) === siteID)
        .flatMap(site => site.accounts.map(account => ({ id: account.id, name: `${site.name} / ${account.name}` })));
    const number = (value: number) => format.number(value, { maximumFractionDigits: 6 });
    const metrics = stats ? [
        { key: 'today', value: stats.today_reward },
        { key: 'recent7', value: stats.recent_7_days_reward },
        { key: 'recent30', value: stats.recent_30_days_reward },
        { key: 'total', value: stats.total_reward },
    ] : [];

    return (
        <section aria-label={t('title')} aria-busy={query.isFetching} className="page-card min-w-0 space-y-4 p-4 sm:p-5">
            <div className="flex items-center justify-between gap-3">
                <h3 className="flex items-center gap-2 text-sm font-semibold"><ChartNoAxesCombined aria-hidden className="size-4 text-primary" />{t('title')}</h3>
                <Button type="button" variant="ghost" size="icon" aria-label={t('refresh')} title={t('refresh')}
                    disabled={query.isFetching} onClick={() => void query.refetch()}>
                    <RefreshCw aria-hidden className={`size-4 ${query.isFetching ? 'animate-spin' : ''}`} />
                </Button>
            </div>
            <p className="text-xs text-muted-foreground">{t('description')}</p>
            <div className="grid grid-cols-2 gap-3">
                <Select value={siteID} onValueChange={value => { setSiteID(value); setAccountID('all'); }}>
                    <SelectTrigger aria-label={t('site')} className="w-full min-w-0"><SelectValue /></SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">{t('allSites')}</SelectItem>
                        {sites.map(site => <SelectItem key={site.id} value={String(site.id)}>{site.name}</SelectItem>)}
                    </SelectContent>
                </Select>
                <Select value={accountID} onValueChange={setAccountID}>
                    <SelectTrigger aria-label={t('account')} className="w-full min-w-0"><SelectValue /></SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">{t('allAccounts')}</SelectItem>
                        {accounts.map(account => <SelectItem key={account.id} value={String(account.id)}>{account.name}</SelectItem>)}
                    </SelectContent>
                </Select>
            </div>
            {query.isLoading ? <p role="status" className="text-sm text-muted-foreground">{t('loading')}</p> : null}
            {query.error ? <p role="alert" className="text-sm text-destructive">{t('loadFailed')}</p> : null}
            {stats ? <>
                <dl className="grid grid-cols-2 gap-3 lg:grid-cols-4">
                    {metrics.map(metric => <div key={metric.key} className="min-w-0 rounded-xl bg-muted/40 p-3">
                        <dt className="text-xs text-muted-foreground">{t(metric.key)}</dt>
                        <dd className="mt-2 truncate text-xl font-semibold tabular-nums" title={number(metric.value)}>{number(metric.value)}</dd>
                    </div>)}
                </dl>
                <p className="text-xs text-muted-foreground">{t('counts', { total: stats.total_count, success: stats.success_count, failed: stats.failed_count, skipped: stats.skipped_count })}</p>
                {stats.pending_count ? <p className="text-xs text-muted-foreground">待确认 {stats.pending_count} 次，尚未计入收益</p> : null}
                {stats.total_count === 0 ? <p className="text-sm text-muted-foreground">{t('empty')}</p> : null}
                {stats.invalid_reward_count > 0 || stats.unknown_reward_count > 0
                    ? <p className="text-xs text-amber-700 dark:text-amber-300">{t('excluded', { invalid: stats.invalid_reward_count, unknown: stats.unknown_reward_count })}</p> : null}
                <p className="text-xs text-muted-foreground">{t('timezone', { timezone: stats.timezone })}</p>
                {stats.by_site.length > 0 ? <details className="min-w-0 border-t border-border/60 pt-3">
                    <summary className="cursor-pointer text-sm font-medium">{t('bySite')}</summary>
                    <div className="mt-3 overflow-x-auto">
                        <table className="w-full min-w-[440px] text-left text-xs">
                            <thead className="text-muted-foreground"><tr>{['siteName', 'reward', 'success', 'failed', 'skipped'].map(key => <th key={key} scope="col" className="px-2 py-2 font-medium">{t(key)}</th>)}</tr></thead>
                            <tbody>{stats.by_site.map(site => <tr key={site.site_id} className="border-t border-border/60">
                                <th scope="row" className="max-w-60 break-words px-2 py-2 font-medium">{site.site_name}</th>
                                <td className="px-2 py-2 tabular-nums">{number(site.reward)}</td>
                                <td className="px-2 py-2 tabular-nums">{site.success_count}</td>
                                <td className="px-2 py-2 tabular-nums">{site.failed_count}</td>
                                <td className="px-2 py-2 tabular-nums">{site.skipped_count}</td>
                            </tr>)}</tbody>
                        </table>
                    </div>
                </details> : null}
            </> : null}
        </section>
    );
}
