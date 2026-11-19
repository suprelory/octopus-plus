'use client';

import { cloneElement, isValidElement, useId, useState, type ComponentProps, type ReactNode } from 'react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import type { CheckinHeader } from '@/api/endpoints/checkin-types';

export const selectClass =
    'h-9 w-full rounded-md border border-input bg-background px-3 text-sm outline-none focus:ring-2 focus:ring-ring disabled:opacity-50';
export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
    const id = useId();
    const control = isValidElement<{ id?: string; 'aria-describedby'?: string }>(children)
        ? cloneElement(children, { id, 'aria-describedby': hint ? `${id}-hint` : undefined })
        : children;
    return (
        <div className="grid gap-1.5 text-sm">
            <label htmlFor={id} className="font-medium">
                {label}
            </label>
            {control}
            {hint && (
                <span id={`${id}-hint`} className="text-xs text-muted-foreground">
                    {hint}
                </span>
            )}
        </div>
    );
}
export function TextField({ label, hint, ...props }: ComponentProps<typeof Input> & { label: string; hint?: string }) {
    return (
        <Field label={label} hint={hint}>
            <Input {...props} />
        </Field>
    );
}
export function Toggle({
    label,
    checked,
    onChange,
    disabled,
}: {
    label: string;
    checked: boolean;
    onChange: (value: boolean) => void;
    disabled?: boolean;
}) {
    return (
        <label className="flex items-center gap-2 text-sm">
            <input
                type="checkbox"
                checked={checked}
                onChange={(e) => onChange(e.target.checked)}
                disabled={disabled}
                className="size-4 accent-primary"
            />
            {label}
        </label>
    );
}
export function ErrorNotice({ error, retry }: { error: unknown; retry?: () => void }) {
    const t = useTranslations('checkinV2');
    if (!error) return null;
    const message =
        typeof error === 'object' && 'message' in error && typeof error.message === 'string'
            ? error.message
            : t('requestFailed');
    return (
        <div
            role="alert"
            className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive"
        >
            <p>{message}</p>
            {retry && (
                <Button type="button" variant="outline" size="sm" onClick={retry} className="mt-2">
                    {t('reload')}
                </Button>
            )}
        </div>
    );
}
export function FormDialog({
    title,
    description,
    children,
    onClose,
    onSubmit,
    pending,
    error,
}: {
    title: string;
    description: string;
    children: ReactNode;
    onClose: () => void;
    onSubmit: () => Promise<unknown>;
    pending: boolean;
    error?: unknown;
}) {
    const t = useTranslations('checkinV2');
    const formId = useId();
    const [validation, setValidation] = useState<unknown>();
    return (
        <Dialog
            open
            onOpenChange={(open) => {
                if (!open && !pending) onClose();
            }}
        >
            <DialogContent className="sm:max-w-3xl" showCloseButton={!pending}>
                <DialogHeader>
                    <DialogTitle>{title}</DialogTitle>
                    <DialogDescription>{description}</DialogDescription>
                </DialogHeader>
                <form
                    id={formId}
                    className="grid max-h-[65dvh] gap-5 overflow-y-auto px-1 pb-1"
                    onSubmit={async (e) => {
                        e.preventDefault();
                        setValidation(undefined);
                        try {
                            await onSubmit();
                        } catch (err) {
                            setValidation(err);
                        }
                    }}
                >
                    <fieldset disabled={pending} className="grid min-w-0 gap-5">
                        {children}
                    </fieldset>
                    <ErrorNotice error={validation || error} />
                </form>
                <DialogFooter>
                    <Button type="button" variant="outline" disabled={pending} onClick={onClose}>
                        {t('cancel')}
                    </Button>
                    <Button type="submit" form={formId} disabled={pending}>
                        {pending ? t('saving') : t('save')}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
export function ConfirmDialog({
    title,
    description,
    onClose,
    action,
    label,
}: {
    title: string;
    description: string;
    onClose: () => void;
    action: () => Promise<unknown>;
    label?: string;
}) {
    const t = useTranslations('checkinV2');
    const [pending, setPending] = useState(false);
    const [error, setError] = useState<unknown>();
    return (
        <Dialog
            open
            onOpenChange={(open) => {
                if (!open && !pending) onClose();
            }}
        >
            <DialogContent showCloseButton={!pending}>
                <DialogHeader>
                    <DialogTitle>{title}</DialogTitle>
                    <DialogDescription>{description}</DialogDescription>
                </DialogHeader>
                <ErrorNotice error={error} />
                <DialogFooter>
                    <Button variant="outline" disabled={pending} onClick={onClose}>
                        {t('cancel')}
                    </Button>
                    <Button
                        disabled={pending}
                        onClick={async () => {
                            setPending(true);
                            try {
                                await action();
                                onClose();
                            } catch (err) {
                                setError(err);
                            } finally {
                                setPending(false);
                            }
                        }}
                    >
                        {label ?? t('confirm')}
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
export function HeadersField({
    label,
    value,
    onChange,
}: {
    label: string;
    value: CheckinHeader[];
    onChange: (value: CheckinHeader[]) => void;
}) {
    const t = useTranslations('checkinV2');
    return (
        <fieldset className="grid min-w-0 gap-2 rounded-lg border p-3">
            <legend className="px-1 text-sm font-medium">{label}</legend>
            {value.map((header, index) => (
                <div className="flex min-w-0 gap-2" key={index}>
                    <Input
                        aria-label={t('headerName', { index: index + 1 })}
                        value={header.header_key}
                        placeholder="X-Example"
                        onChange={(e) =>
                            onChange(
                                value.map((row, i) => (i === index ? { ...row, header_key: e.target.value } : row)),
                            )
                        }
                    />
                    <Input
                        aria-label={t('headerValue', { index: index + 1 })}
                        value={header.header_value}
                        autoComplete="off"
                        onChange={(e) =>
                            onChange(
                                value.map((row, i) => (i === index ? { ...row, header_value: e.target.value } : row)),
                            )
                        }
                    />
                    <Button
                        type="button"
                        variant="outline"
                        onClick={() => onChange(value.filter((_, i) => i !== index))}
                        aria-label={t('removeHeader', { index: index + 1 })}
                    >
                        −
                    </Button>
                </div>
            ))}
            <Button
                type="button"
                variant="outline"
                size="sm"
                className="justify-self-start"
                onClick={() => onChange([...value, { header_key: '', header_value: '' }])}
            >
                {t('addHeader')}
            </Button>
        </fieldset>
    );
}
export function JSONField({
    label,
    value,
    onChange,
}: {
    label: string;
    value: unknown;
    onChange: (value: unknown) => void;
}) {
    const t = useTranslations('checkinV2');
    const [text, setText] = useState(() => JSON.stringify(value ?? null));
    return (
        <Field label={label}>
            <Textarea
                value={text}
                onChange={(e) => {
                    setText(e.target.value);
                    try {
                        onChange(JSON.parse(e.target.value));
                        e.target.setCustomValidity('');
                    } catch {
                        e.target.setCustomValidity(t('invalidJSON'));
                    }
                }}
                className="font-mono text-xs"
            />
        </Field>
    );
}
export function TimeText({ value }: { value?: string | number | null }) {
    if (!value) return <span>—</span>;
    const date = new Date(value);
    return (
        <time dateTime={Number.isNaN(date.getTime()) ? undefined : date.toISOString()}>
            {Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()}
        </time>
    );
}
