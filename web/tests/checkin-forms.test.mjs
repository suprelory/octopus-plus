import assert from 'node:assert/strict';
import test from 'node:test';
import {
    accountDraft,
    balanceCredentialMatches,
    canConfirm,
    credentialDraft,
    credentialMatches,
    credentialPayload,
    externalLink,
    siteDraft,
} from '../src/components/modules/checkin/forms.ts';

test('credential edits omit secrets and keep the editor version; clearing is explicit', () => {
    const original = {
        id: 2,
        name: 'shared',
        type: 'access_token',
        platform: 'sub2api',
        auth_base_url: 'https://login.example',
        proxy_mode: 'direct',
        credential_version: 7,
        has_access_token: true,
        has_refresh_token: true,
        has_auth_headers: true,
    };
    const draft = credentialDraft(original);
    assert.equal(draft.access_token, '');
    const patch = credentialPayload({ ...draft, name: 'renamed' }, original);
    assert.equal(patch.expected_version, 7);
    for (const key of [
        'access_token',
        'refresh_token',
        'password',
        'cookie',
        'api_key',
        'auth_headers',
        'id',
        'has_access_token',
    ])
        assert.equal(key in patch, false, key);
    const cleared = credentialPayload(
        { ...draft, refresh_token: 'discarded', access_token: 'new' },
        original,
        ['refresh_token'],
        true,
    );
    assert.equal(cleared.access_token, 'new');
    assert.equal(cleared.refresh_token, '');
    assert.deepEqual(cleared.auth_headers, []);
    const switched = credentialPayload(
        { ...draft, type: 'cookie', cookie: 'session=new', access_token: 'hidden-draft' },
        original,
        ['refresh_token'],
    );
    assert.equal(switched.cookie, 'session=new');
    assert.equal('access_token' in switched, false);
    assert.equal('refresh_token' in switched, false);
});

test('password credentials use the API discriminator for drafts, payloads and matching', () => {
    const credential = {
        id: 3,
        name: 'password login',
        type: 'username_password',
        platform: 'agentrouter',
        auth_base_url: 'https://login.example',
        username: 'demo',
        proxy_mode: 'direct',
        credential_version: 4,
        has_password: true,
    };
    const draft = credentialDraft(credential);
    assert.equal(draft.type, 'username_password');
    assert.equal(credentialDraft().type, 'username_password');
    assert.equal(draft.password, '');
    const patch = credentialPayload({ ...draft, name: 'renamed' }, credential);
    assert.equal(patch.type, 'username_password');
    assert.equal('password' in patch, false);
    const created = credentialPayload({ ...draft, password: 'secret' });
    assert.equal(created.type, 'username_password');
    assert.equal(created.password, 'secret');

    const site = { platform: 'agentrouter', base_url: credential.auth_base_url, checkin_http_enabled: false };
    assert.equal(credentialMatches(site, 'platform', credential), true);
    assert.equal(credentialMatches(site, 'platform', { ...credential, type: 'password' }), false);
    assert.equal(balanceCredentialMatches('agentrouter', credential), true);
});

test('management drafts strip server state, retain false/zero, and disable automation by default', () => {
    const site = siteDraft({
        name: 'disabled',
        enabled: false,
        sort_order: 0,
        id: 1,
        archived: true,
        verification_status: 'supported',
    });
    assert.equal(site.enabled, false);
    assert.equal(site.sort_order, 0);
    for (const key of ['id', 'archived', 'verification_status']) assert.equal(key in site, false);
    const account = accountDraft(site, {
        enabled: false,
        random_checkin: false,
        checkin_random_window_minutes: 0,
        credential_id: 3,
        checkin_pending: { submitted: true },
    });
    assert.equal(account.enabled, false);
    assert.equal(account.checkin_random_window_minutes, 0);
    assert.equal(account.auto_checkin, false);
    assert.equal('checkin_pending' in account, false);
});

test('credential selection follows execution and balance identity boundaries', () => {
    const site = { platform: 'agentrouter', base_url: 'https://site.example', checkin_http_enabled: false };
    const credential = { platform: 'agentrouter', auth_base_url: site.base_url, type: 'username_password' };
    assert.equal(credentialMatches(site, 'platform', credential), true);
    assert.equal(credentialMatches(site, 'platform', { ...credential, type: 'access_token' }), false);
    assert.equal(credentialMatches(site, 'platform', { ...credential, auth_base_url: 'https://other.example' }), false);
    assert.equal(
        credentialMatches({ ...site, checkin_http_enabled: true }, 'cookie', {
            ...credential,
            type: 'cookie',
            auth_base_url: 'https://other.example',
        }),
        true,
    );
    assert.equal(balanceCredentialMatches('sub2api', { platform: 'sub2api', type: 'api_key' }), true);
    assert.equal(balanceCredentialMatches('new-api', { platform: 'sub2api', type: 'api_key' }), false);
    for (const platform of ['new-api', 'one-api', 'one-hub', 'done-hub', 'anyrouter']) {
        const password = { ...credential, platform };
        assert.equal(credentialMatches({ ...site, platform }, 'platform', password), true, platform);
        assert.equal(balanceCredentialMatches(platform, password), true, platform);
    }
});

test('confirmation is available for submitted pending or failed workflows on supported adapters', () => {
    const httpSite = { platform: 'api', checkin_http_enabled: true, checkin_status: {} };
    const agentRouterSite = { platform: 'agentrouter', checkin_http_enabled: false };
    const pending = { checkin_pending: { submitted: true, finished: false, status: 'pending' } };
    const failedAfterDeadline = {
        checkin_pending: {
            submitted: true,
            finished: true,
            status: 'failed',
            deadline: '2020-01-01T00:00:00Z',
        },
    };
    assert.equal(canConfirm(httpSite, pending), true);
    assert.equal(canConfirm({ ...httpSite, checkin_status: null }, pending), false);
    assert.equal(canConfirm(agentRouterSite, pending), true);
    assert.equal(canConfirm(httpSite, failedAfterDeadline), true);
    assert.equal(canConfirm(agentRouterSite, failedAfterDeadline), true);
    for (const site of [httpSite, agentRouterSite]) {
        for (const checkin_pending of [
            undefined,
            { submitted: false, finished: false, status: 'pending' },
            { submitted: false, finished: true, status: 'failed' },
            { submitted: true, finished: true, status: 'success' },
            { submitted: true, finished: true, status: 'skipped' },
        ])
            assert.equal(canConfirm(site, { checkin_pending }), false, JSON.stringify({ site, checkin_pending }));
    }
    assert.equal(canConfirm({ ...agentRouterSite, checkin_http_enabled: true }, pending), false);
    assert.equal(canConfirm({ ...httpSite, platform: 'new-api', checkin_http_enabled: false }, pending), false);
});

test('external links are HTTP(S)', () => {
    assert.equal(externalLink('javascript:alert(1)'), undefined);
    assert.equal(externalLink('https://example.com/path'), 'https://example.com/path');
});
