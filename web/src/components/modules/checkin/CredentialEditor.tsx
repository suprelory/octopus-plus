'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { checkinAPI, useCheckinMutation } from '@/api/endpoints/checkin';
import {
    checkinPlatforms,
    type CheckinCredential,
    type CheckinCredentialInput,
    type CheckinCredentialPatch,
    type CredentialSecret,
    type CredentialType,
} from '@/api/endpoints/checkin-types';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { Field, FormDialog, HeadersField, TextField, Toggle, selectClass } from './shared';
import { credentialDraft, credentialPayload, credentialSecretFields } from './forms';

export function CredentialFields({
    value,
    onChange,
    original,
    cleared = [],
    onClear,
    replaceHeaders = true,
    onReplaceHeaders,
}: {
    value: CheckinCredentialInput;
    onChange: (value: CheckinCredentialInput) => void;
    original?: CheckinCredential;
    cleared?: CredentialSecret[];
    onClear?: (keys: CredentialSecret[]) => void;
    replaceHeaders?: boolean;
    onReplaceHeaders?: (value: boolean) => void;
}) {
    const t = useTranslations('checkinV2');
    function set<K extends keyof CheckinCredentialInput>(key: K, next: CheckinCredentialInput[K]) {
        onChange({ ...value, [key]: next });
    }
    return (
        <>
            <div className="grid gap-4 sm:grid-cols-2">
                <TextField
                    label={t('credentialName')}
                    required
                    maxLength={191}
                    value={value.name}
                    onChange={(e) => set('name', e.target.value)}
                />
                <Field label={t('credentialType')}>
                    <select
                        className={selectClass}
                        value={value.type}
                        onChange={(e) => set('type', e.target.value as CredentialType)}
                    >
                        {(['password', 'access_token', 'api_key', 'cookie'] as const).map((type) => (
                            <option key={type} value={type}>
                                {t(`types.${type}`)}
                            </option>
                        ))}
                    </select>
                </Field>
                <Field label={t('credentialPlatform')}>
                    <select
                        className={selectClass}
                        value={value.platform}
                        onChange={(e) => set('platform', e.target.value as CheckinCredentialInput['platform'])}
                    >
                        {checkinPlatforms.map((platform) => (
                            <option key={platform}>{platform}</option>
                        ))}
                    </select>
                </Field>
                <TextField
                    label={t('authURL')}
                    type="url"
                    required
                    value={value.auth_base_url}
                    onChange={(e) => set('auth_base_url', e.target.value)}
                />
                {value.type === 'password' && (
                    <TextField
                        label={t('username')}
                        required
                        autoComplete="off"
                        value={value.username ?? ''}
                        onChange={(e) => set('username', e.target.value)}
                    />
                )}
                <TextField
                    label={t('platformUserID')}
                    type="number"
                    min={1}
                    step={1}
                    value={value.platform_user_id ?? ''}
                    onChange={(e) => set('platform_user_id', e.target.value ? Number(e.target.value) : null)}
                />
            </div>
            {credentialSecretFields[value.type].map((key) => (
                <div key={key} className="grid gap-2">
                    <TextField
                        label={t(`secrets.${key}`)}
                        type="password"
                        autoComplete="new-password"
                        value={value[key] ?? ''}
                        disabled={cleared.includes(key)}
                        required={!original && key !== 'refresh_token'}
                        placeholder={original ? t('secretUnchanged') : ''}
                        onChange={(e) => set(key, e.target.value)}
                        hint={original ? t(original[`has_${key}`] ? 'secretConfigured' : 'secretMissing') : undefined}
                    />
                    {original && onClear && (
                        <Toggle
                            label={t('clearSecret', { field: t(`secrets.${key}`) })}
                            checked={cleared.includes(key)}
                            onChange={(checked) =>
                                onClear(checked ? [...cleared, key] : cleared.filter((item) => item !== key))
                            }
                        />
                    )}
                </div>
            ))}
            {value.type === 'access_token' && (
                <TextField
                    label={t('expiresAt')}
                    type="datetime-local"
                    value={value.token_expires_at ? localDateTime(value.token_expires_at) : ''}
                    onChange={(e) => set('token_expires_at', e.target.value ? new Date(e.target.value).getTime() : 0)}
                    hint={t('expiresHint')}
                />
            )}
            {value.platform === 'agentrouter' && (
                <p className="rounded-lg bg-muted p-3 text-sm">{t('agentrouterHint')}</p>
            )}
            <details className="rounded-lg border p-3">
                <summary className="cursor-pointer text-sm font-medium">{t('authenticationNetwork')}</summary>
                <div className="mt-4 grid gap-4">
                    <ProxySelector value={value} onChange={(network) => onChange({ ...value, ...network })} />
                    {original && onReplaceHeaders && (
                        <Toggle label={t('replaceAuthHeaders')} checked={replaceHeaders} onChange={onReplaceHeaders} />
                    )}
                    {replaceHeaders && (
                        <HeadersField
                            label={t('authHeaders')}
                            value={value.auth_headers ?? []}
                            onChange={(headers) => set('auth_headers', headers)}
                        />
                    )}
                </div>
            </details>
        </>
    );
}

function localDateTime(milliseconds: number) {
    const date = new Date(milliseconds);
    return new Date(milliseconds - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

export function CredentialEditor({ credential, onClose }: { credential?: CheckinCredential; onClose: () => void }) {
    const t = useTranslations('checkinV2');
    const [draft, setDraft] = useState(() => credentialDraft(credential));
    const [cleared, setCleared] = useState<CredentialSecret[]>([]);
    const [replaceHeaders, setReplaceHeaders] = useState(!credential);
    const save = useCheckinMutation((body: CheckinCredentialInput | CheckinCredentialPatch) =>
        credential
            ? checkinAPI.patchCredential(credential.id, body as CheckinCredentialPatch)
            : checkinAPI.createCredential(body as CheckinCredentialInput),
    );
    return (
        <FormDialog
            title={t(credential ? 'editCredential' : 'createCredential')}
            description={t('credentialEditorHint')}
            onClose={onClose}
            pending={save.isPending}
            error={save.error}
            onSubmit={async () => {
                await save.mutateAsync(credentialPayload(draft, credential, cleared, replaceHeaders));
                onClose();
            }}
        >
            <CredentialFields
                value={draft}
                onChange={setDraft}
                original={credential}
                cleared={cleared}
                onClear={setCleared}
                replaceHeaders={replaceHeaders}
                onReplaceHeaders={setReplaceHeaders}
            />
        </FormDialog>
    );
}
