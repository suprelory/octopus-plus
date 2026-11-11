'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { RefreshCw } from 'lucide-react';
import type { Site } from '@/api/endpoints/site';
import { useSiteCheckinLogs } from '@/api/endpoints/site-checkin';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { useSettingStore } from '@/stores/setting';
import { formatDateTime, getSiteErrorMessage } from './site-display';
import { translateSiteMessage } from './site-message';

function HistorySelect({ label, value, onChange, options }: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: Array<{ value: string; label: string }>;
}) {
  return (
    <div className="grid min-w-0 gap-1.5 text-sm">
      <span>{label}</span>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger aria-label={label} className="w-full"><SelectValue /></SelectTrigger>
        <SelectContent>
          {options.map(option => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
    </div>
  );
}

export function CheckinHistoryDialog({ sites, onOpenChange, batchJobID }: {
  sites: Site[];
  onOpenChange: (open: boolean) => void;
  batchJobID?: string;
}) {
  const t = useTranslations('siteCheckinHistory');
  const allTranslations = useTranslations();
  const locale = useSettingStore(state => state.locale);
  const [siteID, setSiteID] = useState('all');
  const [accountID, setAccountID] = useState('all');
  const [status, setStatus] = useState('all');
  const [source, setSource] = useState('all');
  const [from, setFrom] = useState('');
  const [until, setUntil] = useState('');
  const [cursors, setCursors] = useState<number[]>([0]);
  const invalidDates = Boolean(from && until && from > until);
  const filters: Record<string, string | number> = { limit: 20 };
  if (batchJobID) filters.batch_id = batchJobID;
  if (siteID !== 'all') filters.site_id = siteID;
  if (accountID !== 'all') filters.account_id = accountID;
  if (status !== 'all') filters.status = status;
  if (source !== 'all') filters.source = source;
  if (cursors.at(-1)) filters.before_id = cursors.at(-1)!;
  if (from) filters.from = new Date(from + 'T00:00:00').toISOString();
  if (until) {
    const end = new Date(until + 'T00:00:00');
    end.setDate(end.getDate() + 1);
    filters.until = end.toISOString();
  }
  const logs = useSiteCheckinLogs(filters, !invalidDates);
  const items = logs.data?.items ?? [];
  const accounts = sites.filter(site => siteID === 'all' || String(site.id) === siteID)
    .flatMap(site => site.accounts.map(account => ({ value: String(account.id), label: site.name + ' / ' + account.name })));
  const resetPage = () => setCursors([0]);

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[min(88vh,48rem)] max-w-5xl flex-col overflow-hidden rounded-3xl p-0 sm:max-w-5xl">
        <DialogHeader className="shrink-0 border-b border-border/60 px-5 py-4">
          <DialogTitle>{t('title')}</DialogTitle>
          <DialogDescription>{t('description')}</DialogDescription>
        </DialogHeader>
        <div className="grid shrink-0 grid-cols-2 gap-3 border-b border-border/60 px-5 py-3 sm:grid-cols-3">
          <HistorySelect label={t('site')} value={siteID} onChange={value => { setSiteID(value); setAccountID('all'); resetPage(); }}
            options={[{ value: 'all', label: t('allSites') }, ...sites.map(site => ({ value: String(site.id), label: site.name }))]} />
          <HistorySelect label={t('account')} value={accountID} onChange={value => { setAccountID(value); resetPage(); }}
            options={[{ value: 'all', label: t('allAccounts') }, ...accounts]} />
          <HistorySelect label={t('status')} value={status} onChange={value => { setStatus(value); resetPage(); }}
            options={[{ value: 'all', label: t('allStatuses') }, { value: 'pending', label: '待确认' }, ...['success', 'failed', 'skipped'].map(value => ({ value, label: t('statuses.' + value) }))]} />
          <HistorySelect label={t('source')} value={source} onChange={value => { setSource(value); resetPage(); }}
            options={[{ value: 'all', label: t('allSources') }, ...['manual', 'scheduled'].map(value => ({ value, label: t('sources.' + value) }))]} />
          <label className="grid min-w-0 gap-1.5 text-sm">
            <span>{t('from')}</span>
            <Input type="date" value={from} onChange={event => { setFrom(event.target.value); resetPage(); }} />
          </label>
          <label className="grid min-w-0 gap-1.5 text-sm">
            <span>{t('until')}</span>
            <Input type="date" value={until} onChange={event => { setUntil(event.target.value); resetPage(); }} />
          </label>
        </div>
        <div className="min-h-0 flex-1 overflow-auto px-5 py-3" aria-busy={logs.isFetching}>
          {invalidDates ? <p role="alert" className="text-sm text-destructive">{t('invalidDates')}</p>
            : logs.isLoading ? <p role="status" className="py-10 text-center text-sm text-muted-foreground">{t('loading')}</p>
            : logs.error ? <p role="alert" className="text-sm text-destructive">{t('loadFailed')} {getSiteErrorMessage(locale, logs.error, allTranslations)}</p>
            : items.length === 0 ? <p className="py-10 text-center text-sm text-muted-foreground">{t('empty')}</p>
            : <table className="w-full min-w-[760px] text-left text-sm">
              <thead className="text-xs text-muted-foreground">
                <tr>{['time', 'account', 'status', 'source', 'detail', 'reward', 'latency'].map(key => <th key={key} scope="col" className="px-2 py-2 font-medium">{t(key)}</th>)}</tr>
              </thead>
              <tbody>
                {items.map(entry => (
                  <tr key={entry.id} className="border-t border-border/60 align-top">
                    <td className="whitespace-nowrap px-2 py-3 text-xs">{formatDateTime(entry.finished_at)}</td>
                    <td className="max-w-40 break-words px-2 py-3"><span className="font-medium">{entry.site_name}</span><span className="block text-xs text-muted-foreground">{entry.account_name}</span></td>
                    <td className="px-2 py-3"><Badge variant={entry.status === 'failed' ? 'destructive' : 'outline'}>{entry.status === 'pending' ? '待确认' : t('statuses.' + entry.status)}</Badge></td>
                    <td className="whitespace-nowrap px-2 py-3 text-xs">{t.has('sources.' + entry.source) ? t('sources.' + entry.source) : entry.source}</td>
                    <td className="min-w-44 max-w-80 break-words px-2 py-3">
                      {t.has('reasons.' + entry.reason) ? <span className="block font-medium">{t('reasons.' + entry.reason)}</span> : null}
                      <span className="text-xs text-muted-foreground">{translateSiteMessage(locale, entry.message, allTranslations)}</span>
                    </td>
                    <td className="max-w-36 break-words px-2 py-3">{entry.reward || (entry.reason === 'checked_in' ? t('unknownReward') : '—')}</td>
                    <td className="whitespace-nowrap px-2 py-3 text-xs">{t('duration', { value: entry.duration_ms })}</td>
                  </tr>
                ))}
              </tbody>
            </table>}
        </div>
        <DialogFooter className="shrink-0 flex-wrap gap-2 border-t border-border/60 px-5 py-3 sm:justify-between">
          <Button variant="outline" size="sm" disabled={logs.isFetching || invalidDates} onClick={() => {
            if (cursors.length > 1) resetPage(); else void logs.refetch();
          }}><RefreshCw className="size-4" />{t('refresh')}</Button>
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Button variant="outline" size="sm" disabled={cursors.length === 1 || logs.isFetching || invalidDates} onClick={() => setCursors(current => current.slice(0, -1))}>{t('previous')}</Button>
            <span className="text-xs text-muted-foreground">{t('page', { page: cursors.length })}</span>
            <Button variant="outline" size="sm" disabled={!logs.data?.next_before_id || logs.isFetching || Boolean(logs.error) || invalidDates} onClick={() => {
              if (logs.data?.next_before_id) setCursors(current => [...current, logs.data!.next_before_id!]);
            }}>{t('next')}</Button>
            <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>{t('close')}</Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
