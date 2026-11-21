'use client';

import { useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Calendar, Clock, Database, ScrollText, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { toast } from '@/components/common/Toast';
import { SettingKey, useExportDB, useImportDB } from '@/api/endpoints/setting';
import { useClearLogs } from '@/api/endpoints/log';
import { SETTING_CONTROL_WIDTH, SettingCard, SettingRow, SettingSection, useSettingField, useSettingToggle } from './shared';
import { DatabaseBackupPanel, type DatabaseExportKind } from './DatabaseBackupPanel';

export function SettingData() {
    const t = useTranslations('setting');

    // 历史日志与统计持久化
    const logEnabled = useSettingToggle(SettingKey.RelayLogKeepEnabled);
    const keepPeriod = useSettingField(SettingKey.RelayLogKeepPeriod);
    const contentEnabled = useSettingToggle(SettingKey.RelayLogContentEnabled);
    const contentPeriod = useSettingField(SettingKey.RelayLogContentKeepPeriod);
    const contentLimit = useSettingField(SettingKey.RelayLogContentMaxMB);
    const statsInterval = useSettingField(SettingKey.StatsSaveInterval);
    const clearLogs = useClearLogs();

    // 备份导出/导入
    const exportDB = useExportDB();
    const importDB = useImportDB();

    const [includeStats, setIncludeStats] = useState(true);
    // 常规备份使用 JSON，含日志的备份使用 ZIP。
    const [exportingKind, setExportingKind] = useState<DatabaseExportKind | null>(null);

    const [file, setFile] = useState<File | null>(null);
    const fileInputRef = useRef<HTMLInputElement | null>(null);

    const rowsAffected = importDB.data?.rows_affected ?? null;
    const rowsAffectedList = useMemo(() => {
        if (!rowsAffected) return [];
        return Object.entries(rowsAffected)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([k, v]) => ({ table: k, count: v }));
    }, [rowsAffected]);

    const handleClearLogs = () => {
        clearLogs.mutate(undefined, {
            onSuccess: () => toast.success(t('log.clearSuccess')),
            onError: () => toast.error(t('log.clearFailed')),
        });
    };

    const onImport = async () => {
        if (!file) {
            toast.error(t('backup.import.noFile'));
            return;
        }
        // accept 属性只约束选择器默认过滤，仍可手动选任意文件，导入前再校验一次
        if (!['application/json', 'application/zip', 'application/x-zip-compressed'].includes(file.type) && !/\.(json|zip)$/i.test(file.name)) {
            toast.error(t('backup.import.invalidFileType'));
            if (fileInputRef.current) fileInputRef.current.value = '';
            setFile(null);
            return;
        }
        try {
            await importDB.mutateAsync(file);
            toast.success(t('backup.import.success'));
            if (fileInputRef.current) fileInputRef.current.value = '';
            setFile(null);
        } catch (e) {
            toast.error(e instanceof Error ? e.message : t('backup.import.failed'));
        }
    };

    const onExport = async (kind: DatabaseExportKind) => {
        setExportingKind(kind);
        try {
            await exportDB.mutateAsync(kind === 'logs'
                ? { include_logs: true, include_stats: includeStats, format: 'zip' }
                : { include_logs: false, include_stats: includeStats, format: 'json' });
            toast.success(t('backup.export.success'));
        } catch (e) {
            toast.error(e instanceof Error ? e.message : t('backup.export.failed'));
        } finally {
            setExportingKind(null);
        }
    };

    return (
        <SettingCard icon={Database} title={t('data.title')}>
            {/* 统计保存周期 */}
            <SettingRow icon={Clock} label={t('statsSaveInterval.label')}>
                <Input
                    type="number"
                    value={statsInterval.value}
                    onChange={(e) => statsInterval.setValue(e.target.value)}
                    onBlur={statsInterval.save}
                    placeholder={t('statsSaveInterval.placeholder')}
                    className={`${SETTING_CONTROL_WIDTH} rounded-xl`}
                />
            </SettingRow>

            {/* 历史日志 */}
            <SettingSection title={t('log.title')} />
            <SettingRow icon={ScrollText} label={t('log.enabled.label')}>
                <Switch checked={logEnabled.enabled} onCheckedChange={logEnabled.toggle} />
            </SettingRow>
            <SettingRow icon={Calendar} label={t('log.keepPeriod.label')}>
                <Input
                    type="number"
                    value={keepPeriod.value}
                    onChange={(e) => keepPeriod.setValue(e.target.value)}
                    onBlur={keepPeriod.save}
                    placeholder={t('log.keepPeriod.placeholder')}
                    className={`${SETTING_CONTROL_WIDTH} rounded-xl`}
                    disabled={!logEnabled.enabled}
                />
            </SettingRow>
            <SettingRow icon={Trash2} label={t('log.clear.label')}>
                <Button
                    variant="destructive"
                    size="sm"
                    onClick={handleClearLogs}
                    disabled={clearLogs.isPending}
                    className="rounded-xl"
                >
                    {clearLogs.isPending ? t('log.clear.clearing') : t('log.clear.button')}
                </Button>
            </SettingRow>

            <SettingRow icon={ScrollText} label={t('log.contentEnabled')}>
                <Switch checked={contentEnabled.enabled} onCheckedChange={contentEnabled.toggle} disabled={!logEnabled.enabled} />
            </SettingRow>
            <SettingRow icon={Calendar} label={t('log.contentPeriod')}>
                <Input type="number" min={0} max={3650} value={contentPeriod.value} onChange={event => contentPeriod.setValue(event.target.value)} onBlur={contentPeriod.save} className={`${SETTING_CONTROL_WIDTH} rounded-xl`} disabled={!logEnabled.enabled} />
            </SettingRow>
            <SettingRow icon={Database} label={t('log.contentLimit')}>
                <Input type="number" min={1} max={64} value={contentLimit.value} onChange={event => contentLimit.setValue(event.target.value)} onBlur={contentLimit.save} className={`${SETTING_CONTROL_WIDTH} rounded-xl`} disabled={!logEnabled.enabled || !contentEnabled.enabled} />
            </SettingRow>
            <p className="text-xs text-muted-foreground">{t('log.contentHint')}</p>

            <DatabaseBackupPanel
                includeStats={includeStats}
                onIncludeStatsChange={setIncludeStats}
                exportingKind={exportingKind}
                onExport={onExport}
                exportPending={exportDB.isPending}
                file={file}
                fileInputRef={fileInputRef}
                onFileChange={(event) => setFile(event.target.files?.[0] ?? null)}
                onImport={onImport}
                importPending={importDB.isPending}
                rowsAffectedList={rowsAffectedList}
            />
        </SettingCard>
    );
}
