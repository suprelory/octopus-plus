'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { checkinAPI, useCheckinCapabilities, useCheckinMutation } from '@/api/endpoints/checkin';
import {
    checkinPlatforms,
    type CheckinSite,
    type CheckinSiteInput,
    type CheckinStatusRule,
} from '@/api/endpoints/checkin-types';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { Textarea } from '@/components/ui/textarea';
import { Field, FormDialog, HeadersField, JSONField, TextField, Toggle, selectClass } from './shared';
import { siteDraft, statusDraft } from './forms';
import { RuleTester } from './RuleTester';

export function SiteEditor({ site, onClose }: { site?: CheckinSite; onClose: () => void }) {
    const t = useTranslations('checkinV2');
    const [draft, setDraft] = useState(() => siteDraft(site));
    const [tags, setTags] = useState(() => site?.tags?.join(', ') ?? '');
    const capabilities = useCheckinCapabilities();
    const save = useCheckinMutation((body: CheckinSiteInput) =>
        site ? checkinAPI.patchSite(site.id, body) : checkinAPI.createSite(body),
    );
    function set<K extends keyof CheckinSiteInput>(key: K, value: CheckinSiteInput[K]) {
        setDraft({ ...draft, [key]: value });
    }
    const setStatus = (value: Partial<CheckinStatusRule>) =>
        set('checkin_status', { ...(draft.checkin_status ?? statusDraft()), ...value });
    const status = draft.checkin_status;
    return (
        <FormDialog
            title={t(site ? 'editSite' : 'createSite')}
            description={t('siteEditorHint')}
            onClose={onClose}
            pending={save.isPending}
            error={save.error}
            onSubmit={async () => {
                try {
                    new Intl.DateTimeFormat('en', { timeZone: draft.checkin_timezone });
                } catch {
                    throw new Error(t('invalidTimezone'));
                }
                if (draft.checkin_window_start > draft.checkin_window_end) throw new Error(t('invalidWindow'));
                await save.mutateAsync({
                    ...draft,
                    name: draft.name.trim(),
                    base_url: draft.base_url.trim(),
                    tags: [
                        ...new Set(
                            tags
                                .split(/[,，]/)
                                .map((tag) => tag.trim())
                                .filter(Boolean),
                        ),
                    ],
                });
                onClose();
            }}
        >
            <div className="grid gap-4 sm:grid-cols-2">
                <TextField
                    label={t('siteName')}
                    required
                    maxLength={191}
                    value={draft.name}
                    onChange={(e) => set('name', e.target.value)}
                />
                <Field label={t('sitePlatform')}>
                    <select
                        className={selectClass}
                        value={draft.platform}
                        onChange={(e) => set('platform', e.target.value as CheckinSiteInput['platform'])}
                    >
                        {checkinPlatforms.map((platform) => (
                            <option key={platform}>{platform}</option>
                        ))}
                    </select>
                </Field>
                <TextField
                    label={t('siteURL')}
                    type="url"
                    required
                    value={draft.base_url}
                    onChange={(e) => set('base_url', e.target.value)}
                />
                <TextField
                    label={t('externalURL')}
                    type="url"
                    value={draft.external_checkin_url ?? ''}
                    onChange={(e) => set('external_checkin_url', e.target.value || null)}
                />
            </div>
            <div className="flex flex-wrap gap-4">
                <Toggle label={t('enabled')} checked={draft.enabled} onChange={(value) => set('enabled', value)} />
                <Toggle label={t('pinned')} checked={draft.is_pinned} onChange={(value) => set('is_pinned', value)} />
            </div>
            <Field label={t('checkinMode')}>
                <select
                    className={selectClass}
                    value={draft.checkin_mode}
                    onChange={(e) => set('checkin_mode', e.target.value as CheckinSiteInput['checkin_mode'])}
                >
                    {['auto', 'enabled', 'disabled'].map((mode) => (
                        <option key={mode} value={mode}>
                            {t(`checkinModes.${mode}`)}
                        </option>
                    ))}
                </select>
            </Field>
            {capabilities.data && (
                <p className="text-xs text-muted-foreground">
                    {t(capabilities.data[draft.platform]?.has_builtin ? 'builtinAvailable' : 'builtinUnavailable')}
                </p>
            )}
            <div className="grid gap-4 sm:grid-cols-3">
                <TextField
                    label={t('timezone')}
                    required
                    value={draft.checkin_timezone}
                    onChange={(e) => set('checkin_timezone', e.target.value)}
                />
                <TextField
                    label={t('windowStart')}
                    type="time"
                    required
                    value={draft.checkin_window_start}
                    onChange={(e) => set('checkin_window_start', e.target.value)}
                />
                <TextField
                    label={t('windowEnd')}
                    type="time"
                    required
                    value={draft.checkin_window_end}
                    onChange={(e) => set('checkin_window_end', e.target.value)}
                />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
                <TextField
                    label={t('tags')}
                    value={tags}
                    onChange={(e) => setTags(e.target.value)}
                    hint={t('tagsHint')}
                />
                <TextField
                    label={t('sortOrder')}
                    type="number"
                    step={1}
                    required
                    value={draft.sort_order}
                    onChange={(e) => set('sort_order', Number(e.target.value))}
                />
            </div>
            <fieldset className="grid gap-4 rounded-lg border p-3">
                <legend className="px-1 text-sm font-medium">{t('executionNetwork')}</legend>
                <ProxySelector value={draft} onChange={(network) => setDraft({ ...draft, ...network })} />
                <HeadersField
                    label={t('siteHeaders')}
                    value={draft.custom_header}
                    onChange={(headers) => set('custom_header', headers)}
                />
            </fieldset>
            <Toggle
                label={t('customHTTP')}
                checked={draft.checkin_http_enabled}
                onChange={(value) => set('checkin_http_enabled', value)}
            />
            {draft.checkin_http_enabled && (
                <div className="grid gap-4 rounded-lg border p-3">
                    <div className="grid gap-4 sm:grid-cols-[8rem_1fr]">
                        <Field label={t('method')}>
                            <select
                                className={selectClass}
                                value={draft.checkin_http_method}
                                onChange={(e) => set('checkin_http_method', e.target.value as 'GET' | 'POST')}
                            >
                                <option>POST</option>
                                <option>GET</option>
                            </select>
                        </Field>
                        <TextField
                            label={t('submitPath')}
                            required
                            placeholder="/checkin"
                            value={draft.checkin_http_path}
                            onChange={(e) => set('checkin_http_path', e.target.value)}
                        />
                    </div>
                    <Field label={t('submitBody')}>
                        <Textarea
                            value={draft.checkin_http_body}
                            onChange={(e) => set('checkin_http_body', e.target.value)}
                            className="font-mono text-xs"
                        />
                    </Field>
                    <HeadersField
                        label={t('submitHeaders')}
                        value={draft.checkin_http_headers}
                        onChange={(headers) => set('checkin_http_headers', headers)}
                    />
                    <p className="text-xs text-muted-foreground">{t('httpCredentialHint')}</p>
                </div>
            )}
            <Toggle
                label={t('statusQuery')}
                checked={!!status}
                onChange={(enabled) => set('checkin_status', enabled ? statusDraft() : null)}
            />
            {status && (
                <div className="grid gap-4 rounded-lg border p-3">
                    <div className="grid gap-4 sm:grid-cols-[8rem_1fr]">
                        <Field label={t('statusMethod')}>
                            <select
                                className={selectClass}
                                value={status.method}
                                onChange={(e) => setStatus({ method: e.target.value as 'GET' | 'POST' })}
                            >
                                <option>GET</option>
                                <option>POST</option>
                            </select>
                        </Field>
                        <TextField
                            label={t('statusPath')}
                            required
                            placeholder="/status"
                            value={status.path}
                            onChange={(e) => setStatus({ path: e.target.value })}
                        />
                    </div>
                    <Field label={t('statusBody')}>
                        <Textarea
                            value={status.body}
                            onChange={(e) => setStatus({ body: e.target.value })}
                            className="font-mono text-xs"
                        />
                    </Field>
                    <HeadersField
                        label={t('statusHeaders')}
                        value={status.headers ?? []}
                        onChange={(headers) => setStatus({ headers })}
                    />
                    <div className="grid gap-4 sm:grid-cols-2">
                        <TextField
                            label={t('checkedPath')}
                            value={status.checked_path}
                            onChange={(e) => setStatus({ checked_path: e.target.value })}
                        />
                        <TextField
                            label={t('datePath')}
                            value={status.date_path}
                            onChange={(e) => setStatus({ date_path: e.target.value })}
                        />
                        <JSONField
                            label={t('checkedValue')}
                            value={status.checked_value}
                            onChange={(checked_value) => setStatus({ checked_value })}
                        />
                        <JSONField
                            label={t('uncheckedValue')}
                            value={status.unchecked_value}
                            onChange={(unchecked_value) => setStatus({ unchecked_value })}
                        />
                    </div>
                    <Field label={t('statusCode')}>
                        <Textarea
                            value={status.code}
                            onChange={(e) => setStatus({ code: e.target.value })}
                            className="font-mono text-xs"
                        />
                    </Field>
                    <Toggle
                        label={t('skipPrecheck')}
                        checked={status.skip_precheck}
                        onChange={(skip_precheck) => setStatus({ skip_precheck })}
                    />
                    <div className="grid gap-4 sm:grid-cols-2">
                        {(
                            [
                                'initial_delay_ms',
                                'poll_interval_ms',
                                'max_attempts',
                                'timeout_seconds',
                                'confirm_timeout_minutes',
                            ] as const
                        ).map((key) => (
                            <TextField
                                key={key}
                                label={t(`statusSettings.${key}`)}
                                type="number"
                                min={key === 'initial_delay_ms' ? 0 : 1}
                                step={1}
                                required
                                value={status[key]}
                                onChange={(e) => setStatus({ [key]: Number(e.target.value) })}
                            />
                        ))}
                        <Field label={t('rewardSource')}>
                            <select
                                className={selectClass}
                                value={status.reward_source}
                                onChange={(e) => setStatus({ reward_source: e.target.value as 'submit' | 'status' })}
                            >
                                <option value="submit">{t('submitResponse')}</option>
                                <option value="status">{t('statusResponse')}</option>
                            </select>
                        </Field>
                    </div>
                </div>
            )}
            <Field label={t('rewardCode')} hint={t('rewardCodeHint')}>
                <Textarea
                    value={draft.checkin_reward_extractor}
                    onChange={(e) => set('checkin_reward_extractor', e.target.value)}
                    className="min-h-24 font-mono text-xs"
                />
            </Field>
            <RuleTester code={draft.checkin_reward_extractor} config={status} />
        </FormDialog>
    );
}
