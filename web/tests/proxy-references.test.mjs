import assert from 'node:assert/strict';
import test from 'node:test';
import { referenceNodeKey, referenceJumpTarget } from '../src/components/modules/proxy-pool/references.ts';

test('proxy reference keys distinguish entities and execution, authentication and balance uses', () => {
    const references = [
        { type: 'checkin_site', checkin_site_id: 1 },
        { type: 'checkin_site', checkin_site_id: 2 },
        { type: 'checkin_account', checkin_site_id: 1, checkin_account_id: 1 },
        { type: 'checkin_balance', checkin_site_id: 1, checkin_account_id: 1 },
        { type: 'checkin_credential', checkin_credential_id: 1 },
        { type: 'checkin_credential', checkin_credential_id: 2 },
        { type: 'channel', channel_id: 1 },
    ];
    assert.equal(new Set(references.map(referenceNodeKey)).size, references.length);
});

test('proxy jumps preserve ID domains and require the IDs of their target', () => {
    assert.deepEqual(
        referenceJumpTarget({ type: 'checkin_balance', checkin_site_id: 3, checkin_account_id: 4 }),
        { kind: 'checkin-balance', siteId: 3, accountId: 4 },
    );
    assert.deepEqual(referenceJumpTarget({ type: 'checkin_credential', checkin_credential_id: 5 }), {
        kind: 'checkin-credential',
        credentialId: 5,
    });
    assert.equal(referenceJumpTarget({ type: 'site', site_id: 3 }), null);
    assert.deepEqual(referenceJumpTarget({ type: 'checkin_site', checkin_site_id: 3 }), { kind: 'checkin-site', siteId: 3 });
    assert.equal(referenceJumpTarget({ type: 'checkin_account', checkin_account_id: 3 }), null);
    assert.deepEqual(referenceJumpTarget({ type: 'channel', channel_id: 11 }), {
        kind: 'channel-card',
        channelId: 11,
    });
});
