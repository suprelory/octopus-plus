'use client';

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { useTestCheckinRewardExtractor } from '@/api/endpoints/site-checkin';
import { getErrorMessage } from '@/lib/errors';
import type { SiteFormFieldsProps } from './site-form';

const templates = {
    generic: {
        code: 'return response.data?.reward ?? response.reward ?? null;',
        sample: '{\n  "success": true,\n  "data": { "reward": 0.1 }\n}',
    },
    newapi: {
        code: 'const quota = response.data?.quota_awarded;\nreturn quota == null ? null : Number(quota) / 500000;',
        sample: '{\n  "success": true,\n  "data": { "quota_awarded": 50000 }\n}',
    },
};

export function CheckinRewardExtractorField({ siteForm, setSiteForm }: SiteFormFieldsProps) {
    const testExtractor = useTestCheckinRewardExtractor();
    const [sample, setSample] = useState(templates.generic.sample);
    const [statusSample, setStatusSample] = useState('{"data":{"checked_in":true,"reward":0.1}}');
    const [result, setResult] = useState<{ code: string; sample: string; message: string; failed: boolean } | null>(null);
    const code = siteForm.checkin_reward_extractor;
    const sampleKey = siteForm.checkin_status ? sample + '\n' + statusSample : sample;
    const visibleResult = result?.code === code && result.sample === sampleKey ? result : null;

    function applyTemplate(template: keyof typeof templates) {
        setSiteForm((current) => ({ ...current, checkin_reward_extractor: templates[template].code }));
        setSample(templates[template].sample);
        setResult(null);
    }

    async function test() {
        setResult(null);
        let response: unknown;
        try {
            if (new TextEncoder().encode(sample).length > 1024 * 1024) {
                throw new Error('响应样例不能超过 1 MiB');
            }
            response = JSON.parse(sample);
        } catch (error) {
            setResult({ code, sample: sampleKey, failed: true, message: error instanceof SyntaxError ? '请输入有效的 JSON 响应样例' : getErrorMessage(error, '操作失败') });
            return;
        }
        try {
            const output = await testExtractor.mutateAsync({ code, response, ...(siteForm.checkin_status ? { status_response: JSON.parse(statusSample) } : {}) });
            setResult({ code, sample: sampleKey, failed: false, message: output.found
                ? `提取结果：$${output.reward}`
                : '未提取到奖励；实际签到时将继续尝试 data.reward 和关联账号余额差。' });
        } catch (error) {
            setResult({ code, sample: sampleKey, failed: true, message: getErrorMessage(error, '操作失败') });
        }
    }

    return (
        <div className="grid gap-2 border-t border-border/60 pt-4 text-sm">
            <label htmlFor="checkin-reward-extractor" className="font-medium">奖励提取代码（可选 JavaScript）</label>
            <div className="flex flex-wrap gap-2">
                <Button type="button" variant="outline" size="sm" onClick={() => applyTemplate('generic')}>通用模板</Button>
                <Button type="button" variant="outline" size="sm" onClick={() => applyTemplate('newapi')}>New API 模板</Button>
            </div>
            <textarea id="checkin-reward-extractor" value={code} rows={4} spellCheck={false}
                onChange={(event) => setSiteForm((current) => ({ ...current, checkin_reward_extractor: event.target.value }))}
                placeholder="return response.data?.amount ?? null;"
                className="w-full min-w-0 resize-y rounded-xl border border-input bg-background px-3 py-2 font-mono text-xs" />
            <p className="text-xs text-muted-foreground">response 为签到返回的 JSON。返回以美元计的非负数值或数字字符串；没有奖励时返回 null。留空时使用默认规则。</p>
            {siteForm.checkin_status ? <p className="text-xs text-muted-foreground">也可通过 responses.status 读取状态响应、responses.submit 读取签到响应。恢复查询时签到响应为 null，请使用可选链，例如 return responses.status?.data?.reward ?? null。</p> : null}
            <p className="text-xs text-muted-foreground">优先使用代码提取结果，其次为 data.reward；仍无奖励时，使用签到账号关联的订阅账号余额差。</p>
            {code.trim() ? (
                <>
                    <label htmlFor="checkin-reward-sample" className="mt-2 font-medium">签到响应样例（JSON）</label>
                    <textarea id="checkin-reward-sample" value={sample} onChange={(event) => setSample(event.target.value)} rows={4} spellCheck={false}
                        className="w-full min-w-0 resize-y rounded-xl border border-input bg-background px-3 py-2 font-mono text-xs" />
                    {siteForm.checkin_status ? <label className="grid gap-2">奖励提取的状态响应样例<textarea value={statusSample} onChange={event => setStatusSample(event.target.value)} rows={3} className="w-full rounded-xl border border-input bg-background p-3 font-mono text-xs" /></label> : null}
                    <div className="flex flex-wrap items-center gap-2">
                        <Button type="button" variant="outline" size="sm" disabled={testExtractor.isPending} onClick={test}>
                            {testExtractor.isPending ? '测试中…' : '测试提取'}
                        </Button>
                        <span className="text-xs text-muted-foreground">仅测试样例，不发送签到请求。</span>
                    </div>
                    {visibleResult ? <p role="status" className={`break-words text-xs ${visibleResult.failed ? 'text-destructive' : 'text-muted-foreground'}`}>{visibleResult.message}</p> : null}
                </>
            ) : null}
        </div>
    );
}
