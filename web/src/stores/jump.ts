import { create } from 'zustand';
import { useNavStore, type NavItem } from '@/components/modules/navbar';

export type ChannelJumpTarget = { kind: 'channel-card'; channelId: number };

export type CheckinJumpTarget =
    | { kind: 'checkin-site'; siteId: number }
    | { kind: 'checkin-account' | 'checkin-balance'; siteId: number; accountId: number }
    | { kind: 'checkin-credential'; credentialId: number };

export type JumpTarget = ChannelJumpTarget | CheckinJumpTarget;

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
        case 'channel-card':
            return 'channel';
        default:
            return 'home';
    }
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
