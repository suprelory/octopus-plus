'use client';

import { XIcon } from 'lucide-react';
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { Site as SiteRecord, SiteAccount } from '@/api/endpoints/site';
import { useAccountForm } from './useAccountForm';
import { AccountCredentialFields } from './AccountCredentialFields';
import { AccountAutomationFields } from './AccountAutomationFields';

interface AccountEditDialogProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    site: SiteRecord | null;
    account: SiteAccount | null;
}

/**
 * 站点账号编辑/创建弹窗。视觉风格与 Channel/Group 卡片编辑面板（MorphingDialog）
 * 保持一致：bg-card / rounded-3xl / text-2xl 标题 / 自定义 close 按钮。
 * 内部 flex 布局并对长表单提供独立滚动区域，确保视口高度较小时底部按钮可点击。
 */
export function AccountEditDialog({ open, onOpenChange, site, account }: AccountEditDialogProps) {
    const {
        accountForm,
        setAccountForm,
        currentPlatform,
        currentCredentialOptions,
        linkedAccounts,
        useSubscriptionToken,
        handleSubmit,
        isPending,
    } = useAccountForm({ site, account, onOpenChange });

    if (!accountForm) {
        return (
            <Dialog open={open} onOpenChange={onOpenChange}>
                <DialogContent className="max-w-md rounded-3xl">
                    <DialogTitle>站点账号</DialogTitle>
                    <DialogDescription>站点上下文不存在。</DialogDescription>
                </DialogContent>
            </Dialog>
        );
    }

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent
                aria-describedby={undefined}
                showCloseButton={false}
                className="w-screen max-w-full md:max-w-xl bg-card text-card-foreground px-6 py-4 rounded-3xl flex flex-col gap-0 border-0 sm:max-w-xl max-h-[min(calc(100vh-2rem),52rem)] overflow-hidden"
            >
                <header className="mb-4 flex items-start justify-between gap-4 shrink-0">
                    <div className="min-w-0 flex-1">
                        <DialogTitle className="text-2xl font-bold text-card-foreground truncate">
                            {account ? '编辑站点账号' : '新增站点账号'}
                        </DialogTitle>
                    </div>
                    <button
                        type="button"
                        onClick={() => onOpenChange(false)}
                        aria-label="关闭"
                        className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors shrink-0"
                    >
                        <XIcon className="size-5" />
                    </button>
                </header>

                <form className="flex flex-1 min-h-0 flex-col" onSubmit={handleSubmit}>
                    <div className="flex-1 min-h-0 space-y-5 overflow-y-auto px-1">
                        <AccountCredentialFields
                            accountForm={accountForm}
                            setAccountForm={setAccountForm}
                            currentPlatform={currentPlatform}
                            currentCredentialOptions={currentCredentialOptions}
                            linkedAccounts={linkedAccounts}
                            useSubscriptionToken={useSubscriptionToken}
                        />

                        <AccountAutomationFields
                            accountForm={accountForm}
                            setAccountForm={setAccountForm}
                            isCheckin={site?.kind === 'checkin'}
                        />

                        <div className="grid gap-2 text-sm">
                            {useSubscriptionToken ? (
                                <span className="text-xs text-muted-foreground">
                                    签到、状态查询和令牌刷新统一使用所选订阅账号的代理及订阅站 User-Agent。
                                </span>
                            ) : (
                                <>
                                    <ProxySelector
                                        allowInherit
                                        value={{
                                            proxy_mode: accountForm.proxy_mode,
                                            proxy_config_id: accountForm.proxy_config_id,
                                        }}
                                        onChange={(next) =>
                                            setAccountForm((current) =>
                                                current
                                                    ? {
                                                          ...current,
                                                          proxy_mode: next.proxy_mode,
                                                          proxy_config_id: next.proxy_config_id ?? null,
                                                      }
                                                    : current,
                                            )
                                        }
                                    />
                                    <span className="text-xs text-muted-foreground">
                                        {site?.kind === 'checkin' ? '用于该签到账号的请求；继承时使用签到站点的代理配置。' : '用于该账号的同步和模型拉取；自动投影的渠道会跟随这里解析后的代理。'}
                                    </span>
                                </>
                            )}
                        </div>
                    </div>

                    <footer className="mt-5 flex shrink-0 flex-col gap-3 px-1 pt-2 sm:flex-row">
                        <Button
                            type="button"
                            variant="secondary"
                            className="h-12 w-full rounded-2xl sm:flex-1"
                            onClick={() => onOpenChange(false)}
                        >
                            取消
                        </Button>
                        <Button
                            type="submit"
                            className="h-12 w-full rounded-2xl sm:flex-1"
                            disabled={isPending}
                        >
                            {isPending ? '保存中...' : account ? '保存修改' : '创建账号'}
                        </Button>
                    </footer>
                </form>
            </DialogContent>
        </Dialog>
    );
}
