'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import {
    checkinAPI,
    isActiveBatch,
    useCheckinBatches,
    useCheckinBatch,
    useCheckinMutation,
    useCheckinNotifications,
} from '@/api/endpoints/checkin';
import type { CheckinBatch, CheckinNotification } from '@/api/endpoints/checkin-types';
import { Button } from '@/components/ui/button';
import { ConfirmDialog, ErrorNotice, TimeText } from './shared';

function BatchCard({ job }: { job: CheckinBatch }) {
    const t = useTranslations('checkinV2');
    return (
        <article className="grid gap-3 rounded-xl border bg-card p-4" aria-label={t('batchWithID', { id: job.id })}>
            <div className="flex flex-wrap items-center justify-between gap-2">
                <code className="text-xs">{job.id}</code>
                <span className="rounded-full bg-muted px-3 py-1 text-xs">{t(`batchStatuses.${job.status}`)}</span>
            </div>
            <p className="text-sm">
                {job.site_ids?.length ? t('selectedScope', { ids: job.site_ids.join(', ') }) : t('allSites')}
            </p>
            <p className="text-sm tabular-nums">
                {t('batchProgress', {
                    total: job.total,
                    attempted: job.attempted,
                    success: job.success,
                    failed: job.failed,
                    pending: job.pending,
                    skipped: job.skipped,
                })}
            </p>
            {isActiveBatch(job) && (
                <>
                    <progress
                        className="h-2 w-full accent-primary"
                        aria-label={t('progress')}
                        max={job.total || 1}
                        value={job.attempted + job.skipped}
                    />
                    <p className="text-xs text-muted-foreground">
                        {job.current_site_name} {job.current_account_name}
                    </p>
                </>
            )}
            <p className="text-xs text-muted-foreground">
                <TimeText value={job.started_at} />
                {job.finished_at && (
                    <>
                        {' '}
                        · <TimeText value={job.finished_at} />
                    </>
                )}
            </p>
            {job.error_message && (
                <p role="alert" className="text-sm text-destructive">
                    {job.error_message}
                </p>
            )}
        </article>
    );
}

export function BatchesPanel({ activeID }: { activeID: string | null }) {
    const t = useTranslations('checkinV2');
    const batches = useCheckinBatches();
    const active = useCheckinBatch(activeID);
    const stored = batches.data?.pages.flatMap((page) => page.items) ?? [];
    const jobs = active.data ? [active.data, ...stored.filter((job) => job.id !== active.data.id)] : stored;
    return (
        <div className="grid gap-3">
            <div className="flex justify-between gap-3">
                <p className="text-sm text-muted-foreground">{t('batchHint')}</p>
                <Button
                    variant="outline"
                    disabled={batches.isFetching}
                    onClick={() => {
                        void batches.refetch();
                        if (activeID) void active.refetch();
                    }}
                >
                    {t('reload')}
                </Button>
            </div>
            <ErrorNotice error={batches.error || active.error} />
            {batches.isPending ? (
                <p role="status">{t('loading')}</p>
            ) : jobs.length === 0 ? (
                <p className="p-8 text-center text-muted-foreground">{t('emptyBatches')}</p>
            ) : (
                jobs.map((job) => <BatchCard key={job.id} job={job} />)
            )}
            {batches.hasNextPage && (
                <Button
                    variant="outline"
                    disabled={batches.isFetchingNextPage}
                    onClick={() => void batches.fetchNextPage()}
                >
                    {t('loadMore')}
                </Button>
            )}
        </div>
    );
}

export function NotificationsPanel() {
    const t = useTranslations('checkinV2');
    const notifications = useCheckinNotifications();
    const retry = useCheckinMutation(checkinAPI.retryNotification);
    const [selected, setSelected] = useState<CheckinNotification | null>(null);
    const items = notifications.data?.pages.flatMap((page) => page.items) ?? [];
    return (
        <div className="grid gap-3">
            <div className="flex justify-between gap-3">
                <p className="text-sm text-muted-foreground">{t('notificationsHint')}</p>
                <Button
                    variant="outline"
                    disabled={notifications.isFetching}
                    onClick={() => void notifications.refetch()}
                >
                    {t('reload')}
                </Button>
            </div>
            <ErrorNotice error={notifications.error} />
            {notifications.isPending ? (
                <p role="status">{t('loading')}</p>
            ) : items.length === 0 ? (
                <p className="p-8 text-center text-muted-foreground">{t('emptyNotifications')}</p>
            ) : (
                items.map((item) => (
                    <article
                        key={item.id}
                        className="grid gap-3 rounded-xl border bg-card p-4"
                        aria-label={t('notificationWithID', { id: item.id })}
                    >
                        <div className="flex flex-wrap items-center justify-between gap-2">
                            <span className="text-sm font-medium">{t(`events.${item.event}`)}</span>
                            <span className="rounded-full bg-muted px-3 py-1 text-xs">
                                {t(`deliveryStates.${item.state}`)}
                            </span>
                        </div>
                        <dl className="grid gap-2 text-xs sm:grid-cols-2">
                            <div>
                                <dt className="text-muted-foreground">{t('deliveryID')}</dt>
                                <dd className="font-mono">{item.id}</dd>
                            </div>
                            <div>
                                <dt className="text-muted-foreground">{t('completionID')}</dt>
                                <dd className="break-all font-mono">{item.completion_id}</dd>
                            </div>
                            <div>
                                <dt className="text-muted-foreground">{t('destination')}</dt>
                                <dd className="break-all font-mono">{item.destination}</dd>
                            </div>
                            <div>
                                <dt className="text-muted-foreground">{t('attempts')}</dt>
                                <dd>
                                    {item.attempts} · <TimeText value={item.finished_at || item.started_at} />
                                </dd>
                            </div>
                        </dl>
                        {item.state === 'uncertain' && (
                            <Button className="justify-self-start" variant="outline" onClick={() => setSelected(item)}>
                                {t('retryNotification')}
                            </Button>
                        )}
                    </article>
                ))
            )}
            {notifications.hasNextPage && (
                <Button
                    variant="outline"
                    disabled={notifications.isFetchingNextPage}
                    onClick={() => void notifications.fetchNextPage()}
                >
                    {t('loadMore')}
                </Button>
            )}
            {selected && (
                <ConfirmDialog
                    title={t('retryNotification')}
                    description={t('retryWarning')}
                    label={t('retryNotification')}
                    onClose={() => setSelected(null)}
                    action={() => retry.mutateAsync(selected.id)}
                />
            )}
        </div>
    );
}
