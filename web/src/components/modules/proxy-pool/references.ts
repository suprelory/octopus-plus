import type { ProxyConfigurationReference } from '@/api/endpoints/proxy-pool';
import type { JumpTarget } from '@/stores/jump';

export function referenceNodeKey(reference: ProxyConfigurationReference) {
    return [
        reference.type,
        reference.channel_id,
        reference.checkin_site_id,
        reference.checkin_account_id,
        reference.checkin_credential_id,
    ]
        .map((value) => value ?? 0)
        .join(':');
}

export function referenceJumpTarget(reference: ProxyConfigurationReference): JumpTarget | null {
    switch (reference.type) {
        case 'channel':
            return reference.channel_id ? { kind: 'channel-card', channelId: reference.channel_id } : null;
        case 'checkin_site':
            return reference.checkin_site_id ? { kind: 'checkin-site', siteId: reference.checkin_site_id } : null;
        case 'checkin_account':
        case 'checkin_balance':
            return reference.checkin_site_id && reference.checkin_account_id
                ? {
                      kind: reference.type === 'checkin_balance' ? 'checkin-balance' : 'checkin-account',
                      siteId: reference.checkin_site_id,
                      accountId: reference.checkin_account_id,
                  }
                : null;
        case 'checkin_credential':
            return reference.checkin_credential_id
                ? { kind: 'checkin-credential', credentialId: reference.checkin_credential_id }
                : null;
        default:
            return null;
    }
}
