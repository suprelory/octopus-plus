'use client';

import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import type { SiteCheckinStatusConfig } from '@/api/endpoints/site-types';
import { useTestCheckinStatusRule } from '@/api/endpoints/site-checkin';
import { getErrorMessage } from '@/lib/errors';
import type { SiteFormFieldsProps } from './site-form';

const defaults: SiteCheckinStatusConfig = {
    method: 'GET', path: '/api/checkin/status', body: '', headers: [], skip_precheck: false,
    checked_path: 'data.checked_in', checked_value: true, unchecked_value: false, date_path: '', code: '',
    initial_delay_ms: 0, poll_interval_ms: 2000, max_attempts: 3, timeout_seconds: 30,
    confirm_timeout_minutes: 60, reward_source: 'submit',
};
const codeTemplate = `if (response.success === false) return "unknown";
const checked = response.data?.checked_in;
if (checked === true) return "checked_in";
if (checked === false) return "not_checked_in";
return "unknown";`;
const textareaClass = 'w-full min-w-0 resize-y rounded-xl border border-input bg-background px-3 py-2 font-mono text-xs';

function previewDate(timeZone: string) {
    try { return new Intl.DateTimeFormat('en-CA', { timeZone: timeZone || 'Asia/Shanghai' }).format(new Date()); }
    catch { return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(new Date()); }
}

function ScalarField({ label, value, onChange }: { label: string; value: string | number | boolean; onChange: (value: string | number | boolean) => void }) {
    const [draft, setDraft] = useState(JSON.stringify(value));
    return <label className="grid gap-2">{label}<Input value={draft} aria-label={label} onChange={event => {
        setDraft(event.target.value);
        try {
            const parsed: unknown = JSON.parse(event.target.value);
            if (!['boolean', 'string', 'number'].includes(typeof parsed)) throw new Error();
            event.target.setCustomValidity('');
            onChange(parsed as string | number | boolean);
        } catch { event.target.setCustomValidity('请输入 JSON 布尔值、数字或带双引号的字符串'); }
    }} /></label>;
}

export function CheckinStatusFields({ siteForm, setSiteForm }: SiteFormFieldsProps) {
    const config = siteForm.checkin_status;
    const preview = useTestCheckinStatusRule();
    const [sample, setSample] = useState('{"success":true,"data":{"checked_in":true}}');
    const [httpStatus, setHTTPStatus] = useState(200);
    const [date, setDate] = useState(() => previewDate(siteForm.checkin_timezone));
    const [outcome, setOutcome] = useState<{ key: string; text: string } | null>(null);
    const previewKey = JSON.stringify({ config, sample, httpStatus, date });
    function update(patch: Partial<SiteCheckinStatusConfig>) {
        setSiteForm(current => ({ ...current, checkin_status: { ...defaults, ...current.checkin_status, ...patch } }));
    }
    async function testRule() {
        if (!config) return;
        const key = previewKey;
        try {
            if (new TextEncoder().encode(sample).length > 1024 * 1024) throw new Error('样例不能超过 1 MiB');
            const output = await preview.mutateAsync({ config, http_status: httpStatus, date, response: JSON.parse(sample) });
            const labels = { checked_in: '已签到', not_checked_in: '未签到', unknown: '无法确认' };
            setOutcome({ key, text: `判定结果：${labels[output.state]}` });
        } catch (error) { setOutcome({ key, text: getErrorMessage(error, '操作失败') }); }
    }
    return <div className="grid gap-4 border-t border-border/60 pt-4 text-sm">
        <label className="flex items-center justify-between gap-4 font-medium">
            通过状态接口确认签到
            <Switch aria-label="通过状态接口确认签到" checked={!!config} onCheckedChange={enabled => setSiteForm(current => ({ ...current, checkin_status: enabled ? structuredClone(defaults) : null }))} />
        </label>
        {config ? <>
            <p className="text-xs text-muted-foreground">先检查状态，未签到时提交一次，再查询确认。提交超时或状态暂未更新时会继续查询，不重复提交。</p>
            <div className="grid gap-3 sm:grid-cols-[8rem_1fr]">
                <label className="grid gap-2">查询方法<Select value={config.method} onValueChange={(method: 'GET' | 'POST') => update({ method, body: method === 'GET' ? '' : config.body })}>
                    <SelectTrigger aria-label="状态查询方法"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="GET">GET</SelectItem><SelectItem value="POST">POST</SelectItem></SelectContent>
                </Select></label>
                <label className="grid gap-2">状态查询路径<Input aria-label="状态查询路径" value={config.path} onChange={event => update({ path: event.target.value })} placeholder="/api/checkin/status" required /></label>
            </div>
            <p className="text-xs text-muted-foreground">{siteForm.checkin_use_subscription_token ? '使用关联订阅账号的 Access Token、签到账号的代理和签到站点公共 Header。' : '使用该账号的 Cookie、代理和站点公共 Header。'}路径、JSON 请求体可使用 {'{{checkin_date}}'} 表示本次签到日期。</p>
            {config.method === 'POST' ? <label className="grid gap-2">状态查询 JSON 请求体<textarea className={textareaClass} value={config.body} rows={3} onChange={event => update({ body: event.target.value })} /></label> : null}
            <div className="grid gap-2">
                <Button type="button" variant="outline" size="sm" className="justify-self-start" onClick={() => update({ headers: [...(config.headers ?? []), { header_key: '', header_value: '' }] })}>添加状态查询 Header</Button>
                {(config.headers ?? []).map((header, index) => <div key={index} className="flex gap-2">
                    <Input aria-label={`状态 Header ${index + 1} 名称`} value={header.header_key} placeholder="Header 名称" onChange={event => update({ headers: config.headers.map((item, i) => i === index ? { ...item, header_key: event.target.value } : item) })} />
                    <Input aria-label={`状态 Header ${index + 1} 值`} value={header.header_value} placeholder="支持 Cookie、用户名占位符" onChange={event => update({ headers: config.headers.map((item, i) => i === index ? { ...item, header_value: event.target.value } : item) })} />
                    <Button type="button" variant="ghost" onClick={() => update({ headers: config.headers.filter((_, i) => i !== index) })}>删除</Button>
                </div>)}
            </div>
            <label className="flex items-center justify-between gap-4">签到前检查状态<Switch aria-label="签到前检查状态" checked={!config.skip_precheck} onCheckedChange={checked => update({ skip_precheck: !checked })} /></label>
            {config.skip_precheck ? <p className="text-xs text-muted-foreground">没有签到前的状态，确认成功后只能记为今日已签到，不累计本次奖励。</p> : null}
            <label className="grid gap-2">状态判定方式<Select value={config.code ? 'code' : 'field'} onValueChange={mode => update({ code: mode === 'code' ? codeTemplate : '' })}>
                <SelectTrigger aria-label="状态判定方式"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="field">字段匹配</SelectItem><SelectItem value="code">JavaScript</SelectItem></SelectContent>
            </Select></label>
            {config.code ? <>
                <label className="grid gap-2">状态判定代码<textarea aria-label="状态判定代码" className={textareaClass} rows={6} value={config.code} onChange={event => update({ code: event.target.value })} /></label>
                <p className="text-xs text-muted-foreground">response 是状态响应 JSON；context.checkinDate 为目标签到日期。返回 checked_in、not_checked_in 或 unknown。仅执行同步判定，不发送网络请求。</p>
            </> : <>
                <label className="grid gap-2">状态字段路径<Input aria-label="状态字段路径" value={config.checked_path} onChange={event => update({ checked_path: event.target.value })} placeholder="data.checked_in" required /></label>
                <div className="grid gap-3 sm:grid-cols-2">
                    <ScalarField label="已签到值（JSON）" value={config.checked_value} onChange={checked_value => update({ checked_value })} />
                    <ScalarField label="未签到值（JSON）" value={config.unchecked_value} onChange={unchecked_value => update({ unchecked_value })} />
                </div>
                <label className="grid gap-2">日期字段路径（可选）<Input value={config.date_path} onChange={event => update({ date_path: event.target.value })} placeholder="data.date" /></label>
                <p className="text-xs text-muted-foreground">支持点分路径，如 data.checked_in、data.records.0.checked_in。日期字段须为 YYYY-MM-DD；字段缺失或日期不符会判为无法确认。</p>
            </>}
            <div className="grid gap-3 sm:grid-cols-2">
                {([
                    ['initial_delay_ms', '提交后首次等待（毫秒）', 0, 10000], ['poll_interval_ms', '查询间隔（毫秒）', 1, 10000],
                    ['max_attempts', '每次最多查询次数', 1, 5], ['timeout_seconds', '单次流程超时（秒）', 1, 120],
                    ['confirm_timeout_minutes', '待确认期限（分钟）', 1, 1440],
                ] as const).map(([key, label, min, max]) => <label className="grid gap-2" key={key}>{label}<Input type="number" aria-label={label} min={min} max={max} required value={config[key]} onChange={event => update({ [key]: Number(event.target.value) })} /></label>)}
                <label className="grid gap-2">默认奖励来源<Select value={config.reward_source} onValueChange={(reward_source: 'submit' | 'status') => update({ reward_source })}>
                    <SelectTrigger aria-label="默认奖励来源"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="submit">签到响应 data.reward</SelectItem><SelectItem value="status">状态响应 data.reward</SelectItem></SelectContent>
                </Select></label>
            </div>
            <p className="text-xs text-muted-foreground">开启自动签到后，待确认任务随后台扫描继续查询。确认超时或修正配置后，可手动再次签到：尚未提交时重新检查，已经提交时只复查状态，不重复提交。</p>
            <details className="rounded-xl border p-3">
                <summary className="cursor-pointer">用响应样例测试判定</summary>
                <div className="mt-3 grid gap-3">
                    <label className="grid gap-2">状态响应样例（JSON）<textarea aria-label="状态响应样例" className={textareaClass} rows={4} value={sample} onChange={event => setSample(event.target.value)} /></label>
                    <div className="grid gap-3 sm:grid-cols-2">
                        <label className="grid gap-2">HTTP 状态码<Input type="number" min={100} max={599} value={httpStatus} onChange={event => setHTTPStatus(Number(event.target.value))} /></label>
                        <label className="grid gap-2">目标签到日期<Input type="date" value={date} onChange={event => setDate(event.target.value)} /></label>
                    </div>
                    <Button type="button" variant="outline" disabled={preview.isPending} onClick={testRule}>{preview.isPending ? '测试中…' : '测试状态判定'}</Button>
                    <p className="text-xs text-muted-foreground">仅运行样例，不请求签到站点，也不保存配置。</p>
                    {outcome?.key === previewKey ? <p role="status">{outcome.text}</p> : null}
                </div>
            </details>
        </> : null}
    </div>;
}
