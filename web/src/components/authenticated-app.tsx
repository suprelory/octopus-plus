'use client';

import { lazy, Suspense, useDeferredValue, useEffect, useRef } from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { apiClient } from '@/api/client';
import { TooltipProvider } from '@/components/animate-ui/components/animate/tooltip';
import Logo from '@/components/modules/logo';
import { NavBar, useNavStore } from '@/components/modules/navbar';
import { useProxyPoolDialogStore } from '@/components/modules/proxy-pool/dialog-store';
import { ENTRANCE_VARIANTS } from '@/lib/animations/fluid-transitions';
import { ContentLoader } from '@/route/content-loader';
const ProxyPoolDialog = lazy(() =>
    import('@/components/modules/proxy-pool/ProxyPoolDialog').then((module) => ({ default: module.ProxyPoolDialog }))
);
const Toolbar = lazy(() =>
    import('@/components/modules/toolbar').then((module) => ({ default: module.Toolbar }))
);

export function AuthenticatedApp() {
    const { activeItem, direction } = useNavStore();
    const visibleItem = useDeferredValue(activeItem);
    const t = useTranslations('navbar');
    const queryClient = useQueryClient();
    const prefetchStartedRef = useRef(false);
    const proxyPoolDialogOpen = useProxyPoolDialogStore((state) => state.isOpen);
    const showToolbar = visibleItem === 'channel'
        || visibleItem === 'group'
        || visibleItem === 'model'
        || visibleItem === 'log';

    useEffect(() => {
        if (prefetchStartedRef.current) return;
        prefetchStartedRef.current = true;

        const prefetches: Array<Promise<unknown>> = [];

        switch (activeItem) {
            case 'home': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['stats', 'total'],
                        queryFn: async () => apiClient.get('/api/v1/stats/total'),
                    })
                );
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['stats', 'daily'],
                        queryFn: async () => apiClient.get('/api/v1/stats/daily'),
                    })
                );
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['stats', 'hourly'],
                        queryFn: async () => apiClient.get('/api/v1/stats/hourly'),
                    })
                );
                break;
            }
            case 'checkin': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['checkin', 'sites'],
                        queryFn: async () => apiClient.get('/api/v1/checkin/sites', { include_archived: true }),
                    })
                );
                break;
            }
            case 'channel': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['channels', 'list'],
                        queryFn: async () => apiClient.get('/api/v1/channel/list'),
                    })
                );
                break;
            }
            case 'group': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['groups', 'list'],
                        queryFn: async () => apiClient.get('/api/v1/group/list'),
                    })
                );
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['models', 'channel'],
                        queryFn: async () => apiClient.get('/api/v1/model/channel'),
                    })
                );
                break;
            }
            case 'model': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['models', 'list'],
                        queryFn: async () => apiClient.get('/api/v1/model/list'),
                    })
                );
                break;
            }
            case 'setting': {
                prefetches.push(
                    queryClient.prefetchQuery({
                        queryKey: ['apikeys', 'list'],
                        queryFn: async () => apiClient.get('/api/v1/apikey/list'),
                    })
                );
                break;
            }
            default:
                break;
        }

        void Promise.allSettled(prefetches);
        // The initial route is intentionally prefetched once when the authenticated app mounts.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    return (
        <TooltipProvider>
            <motion.div
                key="main-app"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                transition={{ duration: 0.3 }}
                className="mx-auto flex h-dvh max-w-6xl flex-col overflow-hidden px-3 md:grid md:grid-cols-[auto_1fr] md:gap-6 md:px-6"
            >
                <NavBar />
                <main className="flex min-h-0 w-full min-w-0 flex-1 flex-col pb-[calc(6rem+env(safe-area-inset-bottom))] md:pb-0">
                    <header className="my-6 flex flex-none flex-wrap items-start gap-x-2 gap-y-3 px-2">
                        <Logo size={48} />
                        <div className="flex-1 overflow-hidden pb-2 sm:pb-0">
                            <AnimatePresence mode="wait" custom={direction}>
                                <motion.div
                                    key={visibleItem}
                                    custom={direction}
                                    variants={{
                                        initial: (nextDirection: number) => ({
                                            y: 32 * nextDirection,
                                            opacity: 0,
                                        }),
                                        animate: {
                                            y: 0,
                                            opacity: 1,
                                        },
                                        exit: (nextDirection: number) => ({
                                            y: -32 * nextDirection,
                                            opacity: 0,
                                        }),
                                    }}
                                    initial="initial"
                                    animate="animate"
                                    exit="exit"
                                    transition={{ duration: 0.3 }}
                                    className="flex flex-col gap-2 sm:flex-row sm:items-baseline sm:gap-6"
                                >
                                    <span className="mt-1 text-3xl font-bold">{t(visibleItem)}</span>
                                </motion.div>
                            </AnimatePresence>
                        </div>
                        {showToolbar && (
                            <div className="relative ml-auto flex min-h-[36px] items-center gap-3">
                                <Suspense fallback={null}>
                                    <Toolbar activeItem={visibleItem} />
                                </Suspense>
                            </div>
                        )}
                        {proxyPoolDialogOpen && (
                            <Suspense fallback={null}>
                                <ProxyPoolDialog />
                            </Suspense>
                        )}
                    </header>
                    <AnimatePresence mode="wait" initial={false}>
                        <motion.div
                            key={visibleItem}
                            variants={ENTRANCE_VARIANTS.content}
                            initial="initial"
                            animate="animate"
                            exit={{
                                opacity: 0,
                                scale: 0.98,
                            }}
                            transition={{ duration: 0.25 }}
                            className="h-full min-h-0 flex-1"
                        >
                            <ContentLoader activeRoute={visibleItem} />
                        </motion.div>
                    </AnimatePresence>
                </main>
            </motion.div>
        </TooltipProvider>
    );
}
