'use client';

import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { checkinAPI } from '@/api/endpoints/checkin';
import type { CheckinStatusRule } from '@/api/endpoints/checkin-types';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { ErrorNotice, Field, TextField } from './shared';

export function RuleTester({ code, config }: { code: string; config: CheckinStatusRule | null }) {
    const t = useTranslations('checkinV2');
    const [sample, setSample] = useState('{}');
    const [statusSample, setStatusSample] = useState('');
    const [httpStatus, setHTTPStatus] = useState(200);
    const [date, setDate] = useState(() => new Date().toISOString().slice(0, 10));
    const test = useMutation({
        retry: false,
        gcTime: 0,
        mutationFn: async (kind: 'reward' | 'status') => {
            let response: unknown, statusResponse: unknown;
            try {
                response = JSON.parse(sample);
                if (statusSample.trim()) statusResponse = JSON.parse(statusSample);
            } catch {
                throw new Error(t('invalidJSON'));
            }
            if (kind === 'reward') {
                const result = await checkinAPI.testReward({
                    code,
                    response,
                    ...(statusSample.trim() ? { status_response: statusResponse } : {}),
                });
                return result.found ? t('rewardFound', { reward: result.reward }) : t('rewardNotFound');
            }
            if (!config) throw new Error(t('statusRequired'));
            const result = await checkinAPI.testStatus({ config, response, http_status: httpStatus, date });
            return t(`ruleStates.${result.state}`);
        },
    });
    return (
        <details className="rounded-lg border p-3">
            <summary className="cursor-pointer text-sm font-medium">{t('testRules')}</summary>
            <div className="mt-4 grid gap-4">
                <p className="text-xs text-muted-foreground">{t('localRulesHint')}</p>
                <Field label={t('responseSample')}>
                    <Textarea
                        className="font-mono text-xs"
                        value={sample}
                        onChange={(e) => {
                            setSample(e.target.value);
                            test.reset();
                        }}
                    />
                </Field>
                <Field label={t('statusResponseSample')}>
                    <Textarea
                        className="font-mono text-xs"
                        value={statusSample}
                        onChange={(e) => {
                            setStatusSample(e.target.value);
                            test.reset();
                        }}
                    />
                </Field>
                <div className="grid gap-4 sm:grid-cols-2">
                    <TextField
                        label={t('httpStatus')}
                        type="number"
                        min={100}
                        max={599}
                        value={httpStatus}
                        onChange={(e) => setHTTPStatus(Number(e.target.value))}
                    />
                    <TextField
                        label={t('checkinDate')}
                        type="date"
                        value={date}
                        onChange={(e) => setDate(e.target.value)}
                    />
                </div>
                <div className="flex flex-wrap gap-2">
                    <Button
                        type="button"
                        variant="outline"
                        disabled={test.isPending || !code.trim()}
                        onClick={() => test.mutate('reward')}
                    >
                        {t('testReward')}
                    </Button>
                    <Button
                        type="button"
                        variant="outline"
                        disabled={test.isPending || !config}
                        onClick={() => test.mutate('status')}
                    >
                        {t('testStatus')}
                    </Button>
                </div>
                <ErrorNotice error={test.error} />
                {test.data && (
                    <p role="status" className="text-sm">
                        {test.data}
                    </p>
                )}
            </div>
        </details>
    );
}
