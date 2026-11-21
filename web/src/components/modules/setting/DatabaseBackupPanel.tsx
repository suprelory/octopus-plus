'use client';

import type { ChangeEvent, RefObject } from 'react';
import { Download, FileArchive, Upload } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { SettingSection } from './shared';

export type DatabaseExportKind = 'json' | 'logs';

export function DatabaseBackupPanel({
    includeStats,
    onIncludeStatsChange,
    exportingKind,
    onExport,
    exportPending,
    file,
    fileInputRef,
    onFileChange,
    onImport,
    importPending,
    rowsAffectedList,
}: {
    includeStats: boolean;
    onIncludeStatsChange: (value: boolean) => void;
    exportingKind: DatabaseExportKind | null;
    onExport: (kind: DatabaseExportKind) => void;
    exportPending: boolean;
    file: File | null;
    fileInputRef: RefObject<HTMLInputElement | null>;
    onFileChange: (event: ChangeEvent<HTMLInputElement>) => void;
    onImport: () => void;
    importPending: boolean;
    rowsAffectedList: Array<{ table: string; count: number }>;
}) {
    const t = useTranslations('setting');

    return (
        <>
            <SettingSection title={t('backup.export.title')} />
            <div className="space-y-3">
                <div className="flex items-center justify-between gap-4">
                    <div className="text-sm text-muted-foreground">{t('backup.export.includeStats')}</div>
                    <Switch checked={includeStats} onCheckedChange={onIncludeStatsChange} />
                </div>

                <Button type="button" variant="outline" className="w-full rounded-xl" onClick={() => onExport('json')} disabled={exportPending}>
                    <Download className="size-4" />
                    {exportingKind === 'json' ? t('backup.export.exporting') : t('backup.export.button')}
                </Button>
                <Button type="button" variant="outline" className="w-full rounded-xl" onClick={() => onExport('logs')} disabled={exportPending}>
                    <FileArchive className="size-4" />
                    {exportingKind === 'logs' ? t('backup.export.exporting') : t('backup.export.withLogsButton')}
                </Button>
                <p className="text-xs text-muted-foreground">
                    {t('backup.export.withLogsWarning')}
                </p>
            </div>

            <SettingSection title={t('backup.import.title')} />
            <div className="space-y-3">
                <Input ref={fileInputRef} type="file" accept="application/json,application/zip,.json,.zip" onChange={onFileChange} className="rounded-xl" />
                <Button type="button" variant="destructive" className="w-full rounded-xl" onClick={onImport} disabled={importPending}>
                    <Upload className="size-4" />
                    {importPending ? t('backup.import.importing') : t('backup.import.button')}
                </Button>
                {file ? <p className="truncate text-xs text-muted-foreground">{file.name}</p> : null}
                {rowsAffectedList.length > 0 ? (
                    <div className="mt-2 space-y-1">
                        <div className="text-xs font-semibold text-card-foreground">{t('backup.import.result')}</div>
                        <div className="grid grid-cols-2 gap-1 text-xs text-muted-foreground">
                            {rowsAffectedList.map((item) => (
                                <div key={item.table} className="flex justify-between gap-2">
                                    <span className="truncate">{item.table}</span>
                                    <span className="tabular-nums">{item.count}</span>
                                </div>
                            ))}
                        </div>
                    </div>
                ) : null}
            </div>
        </>
    );
}
