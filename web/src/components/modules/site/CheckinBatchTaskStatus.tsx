'use client';

import { useMemo, useState } from 'react';
import { Activity, History, RefreshCw } from 'lucide-react';
import { useLatestSiteCheckinBatch, useSiteList } from '@/api/endpoints/site';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Progress } from '@/components/ui/progress';
import { useTranslations } from 'next-intl';
import { CheckinHistoryDialog } from './CheckinHistoryDialog';
import { formatDateTime } from './site-display';

function statusTone(status: string): 'secondary' | 'outline' | 'destructive' {
  if (status === 'running' || status === 'queued') return 'outline';
  if (status === 'failed' || status === 'completed_with_errors') return 'destructive';
  return 'secondary';
}

export function CheckinBatchTaskStatus({ compact = false }: { compact?: boolean }) {
  const t = useTranslations('siteCheckinBatch');
  const { data: task, refetch, isFetching } = useLatestSiteCheckinBatch();
  const { data: allSites } = useSiteList();
  const sites = useMemo(() => (allSites ?? []).filter((site) => site.kind === 'checkin'), [allSites]);
  const [historyOpen, setHistoryOpen] = useState(false);

  if (!task) return null;

  const processed = Math.min(task.total, task.attempted + task.skipped);
  const progress = task.total > 0 ? Math.round((processed / task.total) * 100) : 0;
  const active = task.status === 'queued' || task.status === 'running';
  const statusKey = `statuses.${task.status}`;
  const status = t.has(statusKey) ? t(statusKey) : task.status;
  const counts = [
    { label: t('processed'), value: `${processed}/${task.total}` },
    { label: t('success'), value: String(task.success) },
    { label: t('failed'), value: String(task.failed + task.partial) },
    { label: t('skipped'), value: String(task.skipped) },
    ...(task.pending ? [{ label: '本轮待确认', value: String(task.pending) }] : []),
  ];

  return (
    <section
      aria-live="polite"
      className={`rounded-lg border border-border/70 bg-card px-4 py-3 ${compact ? 'mt-2' : ''}`}
      data-testid="site-checkin-batch-status"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <Activity aria-hidden className={`size-4 shrink-0 ${active ? 'text-primary' : 'text-muted-foreground'}`} />
          <h3 className="truncate text-sm font-semibold">{t(task.site_ids?.length ? 'selectedTitle' : 'title')}</h3>
          <Badge variant={statusTone(task.status)}>{status}</Badge>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button type="button" variant="ghost" size="icon" aria-label={t('refresh')} title={t('refresh')} onClick={() => void refetch()} disabled={isFetching}>
            <RefreshCw className={`size-4 ${isFetching ? 'animate-spin' : ''}`} />
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => setHistoryOpen(true)}>
            <History className="size-4" />
            {t('viewLogs')}
          </Button>
        </div>
      </div>

      <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-4">
        {counts.map((count) => (
          <div key={count.label} className="min-w-0">
            <div className="text-xs text-muted-foreground">{count.label}</div>
            <div className="mt-0.5 truncate text-sm font-medium tabular-nums">{count.value}</div>
          </div>
        ))}
      </div>

      <div className="mt-3 flex items-center gap-3">
        <Progress aria-label={t('progress', { value: progress })} value={progress} className="h-2" />
        <span className="w-10 shrink-0 text-right text-xs tabular-nums text-muted-foreground">{progress}%</span>
      </div>

      {active && task.current_account_name ? (
        <p className="mt-2 truncate text-xs text-muted-foreground" title={`${task.current_site_name ?? ''} / ${task.current_account_name}`}>
          {t('current', { site: task.current_site_name ?? '', account: task.current_account_name })}
        </p>
      ) : null}
      {task.error_message ? <p role="alert" className="mt-2 break-words text-xs text-destructive">{task.error_message}</p> : null}
      <div className="mt-2 flex flex-wrap justify-between gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span>{t('taskID', { id: task.id })}</span>
        <span>{task.finished_at ? formatDateTime(task.finished_at) : formatDateTime(task.started_at)}</span>
      </div>

      {historyOpen ? <CheckinHistoryDialog key={task.id} sites={sites} batchJobID={task.id} onOpenChange={setHistoryOpen} /> : null}
    </section>
  );
}
