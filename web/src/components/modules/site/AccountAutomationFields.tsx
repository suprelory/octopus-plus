'use client';

import { CalendarCheck2, RefreshCw, UserRound } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import type { AccountFormFieldsProps } from './account-form';
import { FORM_SECTION_TRANSITION } from './form-motion';

export function AccountAutomationFields({ accountForm, setAccountForm, isCheckin = false }: AccountFormFieldsProps & { isCheckin?: boolean }) {
    return (
        <div className="rounded-xl border border-border/50 bg-muted/20 p-4">
            <div className="grid gap-x-6 gap-y-3 md:grid-cols-2">
                <label className="flex cursor-pointer items-center justify-between gap-3">
                    <span className="flex items-center gap-2 text-sm font-medium text-card-foreground">
                        <UserRound className="size-4 text-muted-foreground" />
                        启用账号
                    </span>
                    <Switch
                        checked={accountForm.enabled}
                        onCheckedChange={(checked) =>
                            setAccountForm((current) =>
                                current ? { ...current, enabled: checked } : current,
                            )
                        }
                    />
                </label>
                {!isCheckin ? <label className="flex cursor-pointer items-center justify-between gap-3">
                    <span className="flex items-center gap-2 text-sm text-card-foreground">
                        <RefreshCw className="size-4 text-muted-foreground" />
                        自动同步
                    </span>
                    <Switch
                        checked={accountForm.auto_sync}
                        onCheckedChange={(checked) =>
                            setAccountForm((current) =>
                                current ? { ...current, auto_sync: checked } : current,
                            )
                        }
                    />
                </label> : null}
                {isCheckin ? <><label className="flex cursor-pointer items-center justify-between gap-3">
                    <span className="flex items-center gap-2 text-sm text-card-foreground">
                        <CalendarCheck2 className="size-4 text-muted-foreground" />
                        自动签到
                    </span>
                    <Switch
                        checked={accountForm.auto_checkin}
                        onCheckedChange={(checked) =>
                            setAccountForm((current) =>
                                current
                                    ? { ...current, auto_checkin: checked }
                                    : current,
                            )
                        }
                    />
                </label>
                <label className="flex cursor-pointer items-center justify-between gap-3">
                    <span className="flex items-center gap-2 text-sm text-card-foreground">
                        <CalendarCheck2 className="size-4 text-muted-foreground" />
                        随机签到
                    </span>
                    <Switch
                        checked={accountForm.random_checkin}
                        onCheckedChange={(checked) =>
                            setAccountForm((current) =>
                                current
                                    ? { ...current, random_checkin: checked }
                                    : current,
                            )
                        }
                    />
                </label></> : null}
            </div>

            <AnimatePresence initial={false}>
                {isCheckin && accountForm.auto_checkin ? (
                    <motion.div
                        key="auto-checkin-options"
                        initial={{ height: 0, opacity: 0 }}
                        animate={{ height: 'auto', opacity: 1 }}
                        exit={{ height: 0, opacity: 0 }}
                        transition={FORM_SECTION_TRANSITION}
                        className="overflow-hidden"
                    >
                        <motion.div
                            initial={{ y: -6 }}
                            animate={{ y: 0 }}
                            exit={{ y: -6 }}
                            transition={FORM_SECTION_TRANSITION}
                            className="mt-4 grid gap-4 border-t border-border/50 pt-4 md:grid-cols-2"
                        >
                            <p className="text-xs text-muted-foreground md:col-span-2">
                                按站点当地自然日每天自动签到一次，在设定的时间窗口内执行。
                            </p>

                            {accountForm.random_checkin ? (
                                <label className="grid gap-2 text-sm">
                                    <span className="font-medium">随机延迟窗口（分钟）</span>
                                    <Input
                                        type="number"
                                        min={0}
                                        max={1440}
                                        value={accountForm.checkin_random_window_minutes}
                                        onChange={(event) =>
                                            setAccountForm((current) =>
                                                current
                                                    ? {
                                                          ...current,
                                                          checkin_random_window_minutes: Number(
                                                              event.target.value,
                                                          ),
                                                      }
                                                    : current,
                                            )
                                        }
                                        placeholder="120"
                                        className="rounded-xl"
                                    />
                                </label>
                            ) : null}
                        </motion.div>
                    </motion.div>
                ) : null}
            </AnimatePresence>
        </div>
    );
}
