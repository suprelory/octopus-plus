import type { ProxyConfigurationReference } from '@/api/endpoints/proxy-pool';
import type { JumpTarget } from '@/stores/jump';

export function referenceNodeKey(reference: ProxyConfigurationReference) {
    return [
        reference.type,
        reference.site_id,
        reference.site_account_id,
        reference.channel_id,
        reference.checkin_site_id,
        reference.checkin_account_id,
        reference.checkin_credential_id,
    ]
        .map((value) => value ?? 0)
        .join(':');
}

// IDs belong to separate domains; legacy IDs must never be treated as check-in IDs.
export function referenceJumpTarget(reference: ProxyConfigurationReference, v2: boolean): JumpTarget | null {
    switch (reference.type) {
        case 'channel':
            return reference.channel_id ? { kind: 'channel-card', channelId: reference.channel_id } : null;
        case 'checkin_site':
            return v2 && reference.checkin_site_id ? { kind: 'checkin-site', siteId: reference.checkin_site_id } : null;
        case 'checkin_account':
        case 'checkin_balance':
            return v2 && reference.checkin_site_id && reference.checkin_account_id
                ? {
                      kind: reference.type === 'checkin_balance' ? 'checkin-balance' : 'checkin-account',
                      siteId: reference.checkin_site_id,
                      accountId: reference.checkin_account_id,
                  }
                : null;
        case 'checkin_credential':
            return v2 && reference.checkin_credential_id
                ? { kind: 'checkin-credential', credentialId: reference.checkin_credential_id }
                : null;
        case 'site':
            return !v2 && reference.site_id ? { kind: 'site-card', siteId: reference.site_id } : null;
        case 'site_account':
            return !v2 && reference.site_id && reference.site_account_id
                ? { kind: 'site-account', siteId: reference.site_id, accountId: reference.site_account_id }
                : null;
        case 'managed_channel':
            if (v2) return reference.channel_id ? { kind: 'channel-card', channelId: reference.channel_id } : null;
            return reference.site_id
                ? reference.site_account_id
                    ? { kind: 'site-channel-account', siteId: reference.site_id, accountId: reference.site_account_id }
                    : { kind: 'site-channel-card', siteId: reference.site_id }
                : null;
        default:
            return null;
    }
}
