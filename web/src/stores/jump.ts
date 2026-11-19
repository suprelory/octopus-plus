import { create } from 'zustand';
import { useNavStore, type NavItem } from '@/components/modules/navbar';
import { V2_UI } from '@/lib/v2-ui';

export type SiteJumpTarget =
    | { kind: 'site-card'; siteId: number }
    | { kind: 'site-account'; siteId: number; accountId: number };

export type SiteChannelJumpTarget =
    | { kind: 'site-channel-card'; siteId: number }
    | { kind: 'site-channel-account'; siteId: number; accountId: number }
    | { kind: 'site-channel-model'; siteId: number; accountId: number; groupKey: string; modelName: string };

export type ChannelJumpTarget = { kind: 'channel-card'; channelId: number };

export type CheckinJumpTarget =
    | { kind: 'checkin-site'; siteId: number }
    | { kind: 'checkin-account' | 'checkin-balance'; siteId: number; accountId: number }
    | { kind: 'checkin-credential'; credentialId: number };

export type JumpTarget = SiteJumpTarget | SiteChannelJumpTarget | ChannelJumpTarget | CheckinJumpTarget;

export type PendingJump = {
    requestId: number;
    target: JumpTarget;
};

interface JumpState {
    sequence: number;
    pending: PendingJump | null;
    requestJump: (target: JumpTarget) => void;
    clearPending: (requestId?: number) => void;
}

export function getJumpTargetRoute(target: JumpTarget): NavItem {
    switch (target.kind) {
        case 'checkin-site':
        case 'checkin-account':
        case 'checkin-balance':
        case 'checkin-credential':
            return 'checkin';
        case 'site-card':
        case 'site-account':
            return 'site';
        case 'site-channel-card':
        case 'site-channel-account':
        case 'site-channel-model':
        case 'channel-card':
            return 'channel';
        default:
            return 'home';
    }
}

export function isSiteJumpTarget(target: JumpTarget): target is SiteJumpTarget {
    return target.kind === 'site-card' || target.kind === 'site-account';
}

export function isSiteChannelJumpTarget(target: JumpTarget): target is SiteChannelJumpTarget {
    return (
        target.kind === 'site-channel-card' ||
        target.kind === 'site-channel-account' ||
        target.kind === 'site-channel-model'
    );
}

export function isChannelJumpTarget(target: JumpTarget): target is ChannelJumpTarget {
    return target.kind === 'channel-card';
}

export function isCheckinJumpTarget(target: JumpTarget): target is CheckinJumpTarget {
    return target.kind === 'checkin-site' || target.kind === 'checkin-account'
        || target.kind === 'checkin-balance' || target.kind === 'checkin-credential';
}

export const useJumpStore = create<JumpState>((set, get) => ({
    sequence: 0,
    pending: null,
    requestJump: (target) => {
        if (V2_UI && (isSiteJumpTarget(target) || isSiteChannelJumpTarget(target))) {
            set({ pending: null });
            useNavStore.getState().setActiveItem(isSiteJumpTarget(target) ? 'home' : 'channel');
            return;
        }
        if (!V2_UI && isCheckinJumpTarget(target)) return;
        const route = getJumpTargetRoute(target);
        const navState = useNavStore.getState();
        if (navState.activeItem !== route) {
            navState.setActiveItem(route);
        }

        const nextSequence = get().sequence + 1;
        set({
            sequence: nextSequence,
            pending: {
                requestId: nextSequence,
                target,
            },
        });
    },
    clearPending: (requestId) =>
        set((state) => {
            if (!state.pending) return state;
            if (typeof requestId === 'number' && state.pending.requestId !== requestId) {
                return state;
            }

            return { ...state, pending: null };
        }),
}));
