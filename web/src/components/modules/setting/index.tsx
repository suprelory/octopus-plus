'use client';

import { useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Database, KeyRound, Network, Settings2 } from 'lucide-react';
import { PageOverview } from '@/components/common/PageOverview';
import { cn } from '@/lib/utils';
import { SettingAppearance } from './Appearance';
import { SettingAPIKey } from './APIKey';
import { SettingAccount } from './Account';
import { SettingInfo } from './Info';
import { SettingNetwork } from './Network';
import { SettingReliability } from './Reliability';
import { SettingSyncTasks } from './SyncTasks';
import { SettingCheckinNotifications } from './CheckinNotifications';
import { SettingNotificationChannels } from './NotificationChannels';
import { SettingData } from './Data';
import { SettingWebDAVBackup } from './WebDAVBackup';

export function Setting() {
    const t = useTranslations('workspace.settings');
    const [activeSection, setActiveSection] = useState('general');
    const scrollRef = useRef<HTMLDivElement>(null);
    const sections = [
        { id: 'general', icon: Settings2, content: <><div className="space-y-4"><SettingAppearance /><SettingInfo /></div><SettingAccount /></> },
        { id: 'access', icon: KeyRound, content: <SettingAPIKey /> },
        { id: 'connection', icon: Network, content: <><div className="space-y-4"><SettingNetwork /><SettingSyncTasks /><SettingCheckinNotifications /></div><div className="space-y-4"><SettingNotificationChannels /><SettingReliability /></div></> },
        { id: 'data', icon: Database, content: <><SettingData /><SettingWebDAVBackup /></> },
    ];
    return (
        <div className="flex h-full min-h-0 flex-col gap-4">
            <PageOverview title={t('title')} description={t('description')} />
            <div role="group" aria-label={t('navigation')} className="grid shrink-0 grid-cols-2 gap-1 rounded-xl border border-border/60 bg-card/70 p-1 sm:flex sm:w-fit">
                {sections.map(({ id, icon: Icon }) => <button key={id} type="button" aria-pressed={activeSection === id} aria-controls={`settings-${id}`}
                    onClick={() => { setActiveSection(id); scrollRef.current?.scrollTo({ top: 0 }); }}
                    className={cn('flex items-center justify-center gap-2 rounded-lg px-4 py-2 text-xs font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring', activeSection === id ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground')}>
                    <Icon aria-hidden className="size-4" />{t(id)}
                </button>)}
            </div>
            <div ref={scrollRef} className="page-scroll-area flex-1">
                {sections.map(({ id, content }) => <section key={id} id={`settings-${id}`} aria-label={t(id)} hidden={activeSection !== id}>
                    <div className={cn('grid items-start gap-4 *:min-w-0', id !== 'access' && 'lg:grid-cols-2')}>{content}</div>
                </section>)}
            </div>
        </div>
    );
}
