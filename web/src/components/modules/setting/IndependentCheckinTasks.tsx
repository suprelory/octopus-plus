'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { CalendarCheck2 } from 'lucide-react';
import {
    checkinAPI,
    isActiveBatch,
    useCheckinBatches,
    useCheckinBatch,
    useCheckinMutation,
} from '@/api/endpoints/checkin';
import { Button } from '@/components/ui/button';
import { ConfirmDialog, ErrorNotice, TimeText } from '@/components/modules/checkin/shared';
import { useNavStore } from '@/components/modules/navbar';

export function IndependentCheckinTasks() {
    const t = useTranslations('checkinV2');
    const [confirming, setConfirming] = useState(false);
    const [activeID, setActiveID] = useState<string | null>(null);
    const batches = useCheckinBatches();
    const latestID = batches.data?.pages[0]?.items[0]?.id ?? activeID;
    const active = useCheckinBatch(latestID);
    const run = useCheckinMutation(() => checkinAPI.startBatch({ scope: 'all' }));
    const latest = active.data ?? batches.data?.pages[0]?.items[0];
    const busy = run.isPending || !!(latest && isActiveBatch(latest));
    const navigate = useNavStore((state) => state.setActiveItem);
    return (
        <div className="grid gap-3" data-testid="independent-checkin-tasks">
            <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex min-w-0 items-center gap-3">
                    <CalendarCheck2 className="size-5 shrink-0 text-muted-foreground" />
                    <div className="grid gap-1">
                        <span className="text-sm font-medium">{t('runAll')}</span>
                        <p className="text-xs text-muted-foreground">{t('scheduleHint')}</p>
                    </div>
                </div>
                <div className="flex flex-wrap gap-2">
                    <Button variant="outline" size="sm" onClick={() => navigate('checkin')}>
                        {t('navigation')}
                    </Button>
                    <Button
                        variant="outline"
                        size="sm"
                        disabled={busy || batches.isPending || !!batches.error}
                        onClick={() => setConfirming(true)}
                    >
                        {t('runAll')}
                    </Button>
                </div>
            </div>
            <ErrorNotice
                error={batches.error || active.error}
                retry={() => {
                    void batches.refetch();
                    if (latestID) void active.refetch();
                }}
            />
            {latest && (
                <div className="grid gap-1 pl-8 text-xs" role="status">
                    <span>
                        {t(`batchStatuses.${latest.status}`)} · {t('batchWithID', { id: latest.id })}
                    </span>
                    <span>
                        {t('batchProgress', {
                            total: latest.total,
                            attempted: latest.attempted,
                            success: latest.success,
                            failed: latest.failed,
                            pending: latest.pending,
                            skipped: latest.skipped,
                        })}
                    </span>
                    <TimeText value={latest.finished_at || latest.started_at} />
                </div>
            )}
            {confirming && (
                <ConfirmDialog
                    title={t('runAll')}
                    description={t('batchAllConfirm')}
                    onClose={() => setConfirming(false)}
                    action={async () => {
                        const job = await run.mutateAsync(undefined);
                        setActiveID(job.id);
                    }}
                />
            )}
        </div>
    );
}
