'use client';

import { useCallback, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { Input } from '@/components/ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { SiteCredentialType, SitePlatform, type SiteAccount } from '@/api/endpoints/site';
import type { AccountFormFieldsProps } from './account-form';
import { FORM_SECTION_TRANSITION } from './form-motion';

const CREDENTIAL_LABELS: Record<SiteCredentialType, string> = {
    [SiteCredentialType.UsernamePassword]: '用户名 / 密码',
    [SiteCredentialType.AccessToken]: 'Access Token',
    [SiteCredentialType.APIKey]: 'API Key',
    [SiteCredentialType.Cookie]: '签到站 Cookie',
    [SiteCredentialType.LinkedAccount]: '订阅站账号',
};

function AnimatedFormSection({ children }: { children: ReactNode }) {
    const contentRef = useRef<HTMLDivElement>(null);
    const [height, setHeight] = useState<number | 'auto'>('auto');

    const updateHeight = useCallback(() => {
        const node = contentRef.current;
        if (!node) return;

        const nextHeight = node.offsetHeight;
        setHeight((current) => (current === nextHeight ? current : nextHeight));
    }, []);

    useLayoutEffect(() => {
        updateHeight();
    });

    useLayoutEffect(() => {
        const node = contentRef.current;
        if (!node || typeof ResizeObserver === 'undefined') return;

        const observer = new ResizeObserver(updateHeight);
        observer.observe(node);
        return () => observer.disconnect();
    }, [updateHeight]);

    return (
        <motion.div
            initial={false}
            animate={{ height }}
            transition={FORM_SECTION_TRANSITION}
            className="-mx-1 mb-4 overflow-hidden"
        >
            <div ref={contentRef} className="px-1 pb-1">{children}</div>
        </motion.div>
    );
}

export function AccountCredentialFields({ accountForm, setAccountForm, currentPlatform, currentCredentialOptions, linkedAccounts = [], useSubscriptionToken = false }: AccountFormFieldsProps & {
    currentPlatform: SitePlatform;
    currentCredentialOptions: SiteCredentialType[];
    linkedAccounts?: SiteAccount[];
    useSubscriptionToken?: boolean;
}) {
    return (
        <>
            <div className="grid gap-4 md:grid-cols-2">
                <label className="grid gap-2 text-sm">
                    <span className="font-medium">账号名称</span>
                    <Input
                        value={accountForm.name}
                        onChange={(event) =>
                            setAccountForm((current) =>
                                current
                                    ? { ...current, name: event.target.value }
                                    : current,
                            )
                        }
                        placeholder="例如：主账号"
                        className="rounded-xl"
                    />
                </label>

                <label className="grid gap-2 text-sm">
                    <span className="font-medium">凭据类型</span>
                    <Select
                        value={accountForm.credential_type}
                        onValueChange={(value) =>
                            setAccountForm((current) => {
                                if (!current) return current;
                                const nextType = value as SiteCredentialType;
                                return {
                                    ...current,
                                    credential_type: nextType,
                                    access_token:
                                        nextType === SiteCredentialType.AccessToken
                                            ? current.access_token
                                            : '',
                                    api_key:
                                        nextType === SiteCredentialType.APIKey
                                            ? current.api_key
                                            : '',
                                    platform_user_id:
                                        nextType === SiteCredentialType.AccessToken &&
                                        currentPlatform === SitePlatform.NewAPI
                                            ? current.platform_user_id
                                            : '',
                                };
                            })
                        }
                    >
                        <SelectTrigger className="w-full rounded-xl">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className="rounded-xl">
                            {currentCredentialOptions.map((value) => (
                                <SelectItem className="rounded-xl" key={value} value={value}>
                                    {CREDENTIAL_LABELS[value]}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                </label>
            </div>

            <AnimatedFormSection>
                <AnimatePresence initial={false} mode="popLayout">
                    {accountForm.credential_type === SiteCredentialType.Cookie ? (
                        <div key="cookie" className="grid gap-2 text-sm">
                            <label htmlFor="checkin-cookie" className="font-medium">签到站 Cookie</label>
                            <Input id="checkin-cookie" type="password" autoComplete="off" value={accountForm.cookie}
                                onChange={(event) => setAccountForm((current) => current ? { ...current, cookie: event.target.value } : current)}
                                placeholder="session=...; token=..." className="rounded-xl" required />
                            <p className="text-xs text-muted-foreground">从签到站登录后的浏览器请求中复制完整 Cookie，用于发送自定义 HTTP 签到请求。</p>
                            <label htmlFor="checkin-balance-account" className="mt-2 font-medium">余额账号（可选）</label>
                            <Select value={accountForm.linked_account_id?.toString() ?? 'none'}
                                onValueChange={(value) => setAccountForm((current) => current ? { ...current, linked_account_id: value === 'none' ? null : Number(value) } : current)}>
                                <SelectTrigger id="checkin-balance-account" className="w-full rounded-xl"><SelectValue placeholder="选择关联订阅账号" /></SelectTrigger>
                                <SelectContent>
                                    <SelectItem value="none">不关联余额账号</SelectItem>
                                    {linkedAccounts.map((item) => <SelectItem key={item.id} value={String(item.id)}>{item.name}</SelectItem>)}
                                </SelectContent>
                            </Select>
                            <p className="text-xs text-muted-foreground">未提取到签到奖励时，使用此订阅账号签到前后的余额差。查询沿用订阅站及账号的凭据和代理。</p>
                            {linkedAccounts.length === 0 ? <p className="text-xs text-muted-foreground">可先在签到站点设置中关联订阅站，并添加支持查询余额的订阅账号。</p> : null}
                        </div>
                    ) : accountForm.credential_type === SiteCredentialType.LinkedAccount ? (
                        <div key="linked-account" className="grid gap-2 text-sm">
                            <label htmlFor="checkin-linked-account" className="font-medium">订阅站账号</label>
                            <Select value={accountForm.linked_account_id?.toString() ?? ''}
                                onValueChange={(value) => setAccountForm((current) => current ? { ...current, linked_account_id: Number(value) } : current)}>
                                <SelectTrigger id="checkin-linked-account" className="w-full rounded-xl"><SelectValue placeholder="选择订阅站账号" /></SelectTrigger>
                                <SelectContent>
                                    {linkedAccounts.map((item) => <SelectItem key={item.id} value={String(item.id)}>{item.name}</SelectItem>)}
                                </SelectContent>
                            </Select>
                            <p className="text-xs text-muted-foreground">{useSubscriptionToken ? '签到和状态查询使用此订阅账号的最新 Access Token 进行 Bearer 鉴权；令牌更新后自动生效，无需填写 Cookie。' : '平台签到使用此订阅账号的最新 Access Token、用户 ID 或登录凭据。'}</p>
                            {linkedAccounts.length === 0 ? <p className="text-xs text-amber-600">{useSubscriptionToken ? '请先关联 Sub2API 订阅站，并为订阅站添加 Access Token 账号。' : '请先在签到站点设置中关联订阅站，并为订阅站添加 Access Token 或用户名 / 密码账号。'}</p> : null}
                        </div>
                    ) : accountForm.credential_type === SiteCredentialType.UsernamePassword ? (
                        <motion.div
                            key={SiteCredentialType.UsernamePassword}
                            initial={{ opacity: 0, y: -6 }}
                            animate={{ opacity: 1, y: 0 }}
                            exit={{ opacity: 0, y: -6 }}
                            transition={FORM_SECTION_TRANSITION}
                        >
                            <div className="grid gap-4 md:grid-cols-2">
                                <label className="grid gap-2 text-sm">
                                    <span className="font-medium">用户名</span>
                                    <Input
                                        value={accountForm.username}
                                        onChange={(event) =>
                                            setAccountForm((current) =>
                                                current
                                                    ? { ...current, username: event.target.value }
                                                    : current,
                                            )
                                        }
                                        placeholder="请输入用户名"
                                        className="rounded-xl"
                                    />
                                </label>

                                <label className="grid gap-2 text-sm">
                                    <span className="font-medium">密码</span>
                                    <Input
                                        type="password"
                                        value={accountForm.password}
                                        onChange={(event) =>
                                            setAccountForm((current) =>
                                                current
                                                    ? { ...current, password: event.target.value }
                                                    : current,
                                            )
                                        }
                                        placeholder="请输入密码"
                                        className="rounded-xl"
                                    />
                                </label>
                            </div>
                        </motion.div>
                    ) : accountForm.credential_type === SiteCredentialType.AccessToken ? (
                        <motion.div
                            key={SiteCredentialType.AccessToken}
                            initial={{ opacity: 0, y: -6 }}
                            animate={{ opacity: 1, y: 0 }}
                            exit={{ opacity: 0, y: -6 }}
                            transition={FORM_SECTION_TRANSITION}
                        >
                            <div className="grid gap-4">
                                <label className="grid gap-2 text-sm">
                                    <span className="font-medium">Access Token</span>
                                    <Input
                                        value={accountForm.access_token}
                                        onChange={(event) =>
                                            setAccountForm((current) =>
                                                current
                                                    ? { ...current, access_token: event.target.value }
                                                    : current,
                                            )
                                        }
                                        placeholder="请输入 Access Token"
                                        className="rounded-xl"
                                    />
                                </label>

                                {currentPlatform === SitePlatform.Sub2API ? (
                                    <div className="grid gap-2">
                                        <div className="grid gap-4 md:grid-cols-2">
                                            <label className="grid gap-2 text-sm">
                                                <span className="font-medium">Refresh Token</span>
                                                <Input
                                                    value={accountForm.refresh_token}
                                                    onChange={(event) =>
                                                        setAccountForm((current) =>
                                                            current
                                                                ? {
                                                                      ...current,
                                                                      refresh_token: event.target.value,
                                                                  }
                                                                : current,
                                                        )
                                                    }
                                                    placeholder="可选：请输入 refresh_token"
                                                    className="rounded-xl"
                                                />
                                            </label>

                                            <label className="grid gap-2 text-sm">
                                                <span className="font-medium">token_expires_at</span>
                                                <Input
                                                    value={accountForm.token_expires_at}
                                                    onChange={(event) =>
                                                        setAccountForm((current) =>
                                                            current
                                                                ? {
                                                                      ...current,
                                                                      token_expires_at: event.target.value,
                                                                  }
                                                                : current,
                                                        )
                                                    }
                                                    placeholder="可选：F12 中的时间戳或时间字符串"
                                                    className="rounded-xl"
                                                />
                                            </label>
                                        </div>
                                        <span className="text-xs text-muted-foreground">
                                            自动续期需要有效的 Refresh Token；过期时间可留空，查询鉴权失败时会尝试恢复。
                                            请使用独立登录会话，避免浏览器与本项目争用同一个 Refresh Token。
                                        </span>
                                    </div>
                                ) : null}

                                {currentPlatform === SitePlatform.NewAPI ? (
                                    <label className="grid gap-2 text-sm">
                                        <span className="font-medium">Platform User ID</span>
                                        <Input
                                            value={accountForm.platform_user_id}
                                            onChange={(event) =>
                                                setAccountForm((current) =>
                                                    current
                                                        ? {
                                                              ...current,
                                                              platform_user_id: event.target.value,
                                                          }
                                                        : current,
                                                )
                                            }
                                            placeholder="例如 11494"
                                            className="rounded-xl"
                                            required
                                        />
                                        <span className="text-xs text-muted-foreground">
                                            New API 站点同步 token、分组和签到时需要用户
                                            ID。导入数据会尽量自动填充该值。
                                        </span>
                                    </label>
                                ) : null}
                            </div>
                        </motion.div>
                    ) : (
                        <motion.div
                            key={SiteCredentialType.APIKey}
                            initial={{ opacity: 0, y: -6 }}
                            animate={{ opacity: 1, y: 0 }}
                            exit={{ opacity: 0, y: -6 }}
                            transition={FORM_SECTION_TRANSITION}
                        >
                            <label className="grid gap-2 text-sm">
                                <span className="font-medium">API Key</span>
                                <Input
                                    value={accountForm.api_key}
                                    onChange={(event) =>
                                        setAccountForm((current) =>
                                            current
                                                ? { ...current, api_key: event.target.value }
                                                : current,
                                        )
                                    }
                                    placeholder="请输入 API Key"
                                    className="rounded-xl"
                                />
                            </label>
                        </motion.div>
                    )}
                </AnimatePresence>
            </AnimatedFormSection>
        </>
    );
}
