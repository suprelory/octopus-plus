'use client';

import { useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { checkinAPI, useCheckinMutation } from '@/api/endpoints/checkin';
import {
    checkinPlatforms,
    type CheckinAccount,
    type CheckinAccountCreate,
    type CheckinAccountInput,
    type CheckinBalanceConfig,
    type CheckinCredential,
    type CheckinCredentialInput,
    type CheckinSite,
} from '@/api/endpoints/checkin-types';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { accountDraft, balanceCredentialMatches, credentialDraft, credentialMatches, credentialPayload } from './forms';
import { Field, FormDialog, HeadersField, TextField, Toggle, selectClass } from './shared';
import { CredentialFields } from './CredentialEditor';

export function AccountEditor({
    site,
    account,
    credentials,
    focusBalance = false,
    onClose,
}: {
    site: CheckinSite;
    account?: CheckinAccount;
    credentials: CheckinCredential[];
    focusBalance?: boolean;
    onClose: () => void;
}) {
    const t = useTranslations('checkinV2');
    const balanceRef = useRef<HTMLSelectElement>(null);
    const [draft, setDraft] = useState(() => accountDraft(site, account));
    const [inline, setInline] = useState(!account && credentials.length === 0);
    const [credential, setCredential] = useState<CheckinCredentialInput>(() => ({
        ...credentialDraft(),
        platform: site.platform,
        auth_base_url: site.base_url,
        type: site.checkin_http_enabled ? 'cookie' : 'password',
    }));
    const save = useCheckinMutation((body: CheckinAccountCreate | CheckinAccountInput) =>
        account
            ? checkinAPI.patchAccount(account.id, body as CheckinAccountInput)
            : checkinAPI.createAccount(body as CheckinAccountCreate),
    );
    function set<K extends keyof CheckinAccountInput>(key: K, value: CheckinAccountInput[K]) {
        setDraft({ ...draft, [key]: value });
    }
    const balance = draft.balance_config ?? { mode: 'disabled' as const };
    const patchBalance = (value: Partial<CheckinBalanceConfig>) => set('balance_config', { ...balance, ...value });
    const compatible = credentials.filter((item) => credentialMatches(site, draft.auth_mode, item));
    const current = credentials.find((item) => item.id === draft.credential_id);
    const balanceOptions = credentials.filter((item) =>
        balanceCredentialMatches(balance.platform ?? site.platform, item),
    );
    return (
        <FormDialog
            initialFocusRef={focusBalance ? balanceRef : undefined}
            title={t(account ? 'editAccount' : 'createAccount')}
            description={t('accountEditorHint', { site: site.name })}
            pending={save.isPending}
            error={save.error}
            onClose={onClose}
            onSubmit={async () => {
                if (!inline && !compatible.some((item) => item.id === draft.credential_id))
                    throw new Error(t('chooseCompatibleCredential'));
                const body = {
                    ...draft,
                    name: draft.name.trim(),
                    balance_config:
                        balance.mode === 'disabled'
                            ? null
                            : balance.mode === 'primary'
                              ? { mode: 'primary' as const }
                              : balance,
                };
                if (account) await save.mutateAsync(body);
                else if (inline) {
                    const fields: Partial<CheckinAccountInput> = { ...body };
                    delete fields.credential_id;
                    await save.mutateAsync({
                        ...fields,
                        site_id: site.id,
                        credential: credentialPayload(credential) as CheckinCredentialInput,
                    } as CheckinAccountCreate);
                } else await save.mutateAsync({ ...body, site_id: site.id });
                onClose();
            }}
        >
            <div className="grid gap-4 sm:grid-cols-2">
                <TextField
                    label={t('accountName')}
                    required
                    maxLength={191}
                    value={draft.name}
                    onChange={(e) => set('name', e.target.value)}
                />
                <Field label={t('authMode')}>
                    <select
                        className={selectClass}
                        value={draft.auth_mode}
                        onChange={(e) => {
                            set('auth_mode', e.target.value as CheckinAccountInput['auth_mode']);
                        }}
                    >
                        {(site.checkin_http_enabled ? ['cookie', 'bearer'] : ['platform']).map((mode) => (
                            <option key={mode} value={mode}>
                                {t(`authModes.${mode}`)}
                            </option>
                        ))}
                    </select>
                </Field>
            </div>
            {!account && <Toggle label={t('inlineCredential')} checked={inline} onChange={setInline} />}
            {inline ? (
                <div className="grid gap-4 rounded-lg border p-4">
                    <CredentialFields value={credential} onChange={setCredential} />
                </div>
            ) : (
                <Field label={t('primaryCredential')} hint={t('compatibleCredentialsHint')}>
                    <select
                        required
                        className={selectClass}
                        value={draft.credential_id || ''}
                        onChange={(e) => set('credential_id', Number(e.target.value))}
                    >
                        <option value="">{t('chooseCredential')}</option>
                        {!!draft.credential_id && !compatible.some((item) => item.id === draft.credential_id) && (
                            <option value={draft.credential_id} disabled>
                                {current?.name ?? `#${draft.credential_id}`} · {t('incompatible')}
                            </option>
                        )}
                        {compatible.map((item) => (
                            <option key={item.id} value={item.id}>
                                {item.name} · {t(`types.${item.type}`)}
                            </option>
                        ))}
                    </select>
                </Field>
            )}
            <div className="flex flex-wrap gap-4">
                <Toggle label={t('enabled')} checked={draft.enabled} onChange={(value) => set('enabled', value)} />
                <Toggle
                    label={t('autoCheckin')}
                    checked={draft.auto_checkin}
                    onChange={(value) => set('auto_checkin', value)}
                />
                <Toggle
                    label={t('randomCheckin')}
                    checked={draft.random_checkin}
                    onChange={(value) => set('random_checkin', value)}
                />
            </div>
            {draft.random_checkin && (
                <TextField
                    label={t('randomWindow')}
                    type="number"
                    min={0}
                    max={1440}
                    step={1}
                    required
                    value={draft.checkin_random_window_minutes}
                    onChange={(e) => set('checkin_random_window_minutes', Number(e.target.value))}
                />
            )}
            <fieldset className="grid gap-3 rounded-lg border p-3">
                <legend className="px-1 text-sm font-medium">{t('executionNetwork')}</legend>
                <ProxySelector value={draft} onChange={(network) => setDraft({ ...draft, ...network })} allowInherit />
            </fieldset>
            <fieldset className="grid min-w-0 gap-4 rounded-lg border p-3">
                <legend className="px-1 text-sm font-medium">{t('balanceConfiguration')}</legend>
                <Field label={t('balanceMode')}>
                    <select
                        className={selectClass}
                        value={balance.mode}
                        ref={balanceRef}
                        onChange={(e) =>
                            set(
                                'balance_config',
                                e.target.value === 'custom'
                                    ? {
                                          mode: 'custom',
                                          platform: site.platform === 'api' ? 'sub2api' : site.platform,
                                          base_url: site.base_url,
                                          credential_id: null,
                                          proxy_mode: 'direct',
                                          headers: [],
                                      }
                                    : { mode: e.target.value as 'disabled' | 'primary' },
                            )
                        }
                    >
                        {['disabled', 'primary', 'custom'].map((mode) => (
                            <option key={mode} value={mode}>
                                {t(`balanceModes.${mode}`)}
                            </option>
                        ))}
                    </select>
                </Field>
                {balance.mode === 'primary' && (
                    <p className="text-xs text-muted-foreground">{t('primaryBalanceHint')}</p>
                )}
                {balance.mode === 'custom' && (
                    <>
                        <div className="grid gap-4 sm:grid-cols-2">
                            <Field label={t('balancePlatform')}>
                                <select
                                    className={selectClass}
                                    value={balance.platform}
                                    onChange={(e) =>
                                        patchBalance({
                                            platform: e.target.value as CheckinBalanceConfig['platform'],
                                            credential_id: null,
                                        })
                                    }
                                >
                                    {checkinPlatforms
                                        .filter((platform) => platform !== 'api')
                                        .map((platform) => (
                                            <option key={platform}>{platform}</option>
                                        ))}
                                </select>
                            </Field>
                            <TextField
                                label={t('balanceURL')}
                                type="url"
                                required
                                value={balance.base_url ?? ''}
                                onChange={(e) => patchBalance({ base_url: e.target.value })}
                            />
                        </div>
                        <Field label={t('balanceCredential')}>
                            <select
                                required
                                className={selectClass}
                                value={balance.credential_id ?? ''}
                                onChange={(e) => patchBalance({ credential_id: Number(e.target.value) })}
                            >
                                <option value="">{t('chooseCredential')}</option>
                                {!!balance.credential_id &&
                                    !balanceOptions.some((item) => item.id === balance.credential_id) && (
                                        <option value={balance.credential_id} disabled>
                                            #{balance.credential_id} · {t('incompatible')}
                                        </option>
                                    )}
                                {balanceOptions.map((item) => (
                                    <option key={item.id} value={item.id}>
                                        {item.name}
                                    </option>
                                ))}
                            </select>
                        </Field>
                        <ProxySelector
                            value={{
                                proxy_mode: balance.proxy_mode ?? 'direct',
                                proxy_config_id: balance.proxy_config_id,
                            }}
                            onChange={patchBalance}
                        />
                        <HeadersField
                            label={t('balanceHeaders')}
                            value={balance.headers ?? []}
                            onChange={(headers) => patchBalance({ headers })}
                        />
                    </>
                )}
            </fieldset>
        </FormDialog>
    );
}
