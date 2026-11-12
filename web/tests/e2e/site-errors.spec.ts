import { expect, test } from '@playwright/test';
import { SiteCredentialType, type SiteAccount } from '../../src/api/endpoints/site-types';
import { makeSite, makeCheckinSite, mockApp } from './fixtures';

function makeAccount(): SiteAccount {
    return {
        id: 11, site_id: 1, name: 'Primary account', credential_type: SiteCredentialType.AccessToken,
        username: '', password: '', access_token: 'test-token', api_key: '', refresh_token: '',
        token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: false,
        auto_checkin: true, random_checkin: false,
        checkin_random_window_minutes: 0, checkin_failure_count: 0,
        last_sync_status: 'idle', last_checkin_status: 'idle', last_sync_message: '',
        last_checkin_message: '', balance: 0, balance_used: 0, today_income: 0,
        tokens: [], user_groups: [], models: [], channel_bindings: [],
    };
}

for (const error of [
    { name: 'DNS', code: 'site.upstream.network_error', status: 502, raw: 'lookup site.invalid: no such host',
        params: { reason: 'lookup site.invalid: no such host' }, expected: '无法连接站点：lookup site.invalid: no such host' },
    { name: 'timeout', code: 'site.upstream.timeout', status: 504, raw: 'context deadline exceeded',
        params: { reason: 'context deadline exceeded' }, expected: '站点请求超时：context deadline exceeded' },
    { name: 'business failure', code: 'site.upstream.business_error', status: 502, raw: 'Access token expired',
        params: { reason: 'Access token expired' }, expected: '站点拒绝了请求：Access token expired' },
    { name: 'gateway', code: 'site.upstream.http_error', status: 502, raw: 'http 502: Bad Gateway',
        params: { statusCode: 502, reason: 'Bad Gateway' }, expected: '站点上游请求失败（HTTP 502）：Bad Gateway' },
    { name: 'legacy gateway response', code: 'site.upstream.http_error', status: 502, raw: 'http 502: Bad Gateway',
        params: { statusCode: 502 }, expected: 'http 502: Bad Gateway' },
]) {
    test(`manual sync shows the actual ${error.name} reason`, async ({ page }) => {
        const account = makeAccount();
        const site = { ...makeSite(), accounts: [account] };
        const state = await mockApp(page, 'site', {
            sites: [site],
            mutate: request => {
                expect(request.path).toBe('/api/v1/site/account/sync/11');
                account.last_sync_status = 'failed';
                account.last_sync_message = error.raw;
                return { status: error.status, errorCode: error.code, message: error.raw, params: error.params };
            },
        });
        await page.goto('/');
        await page.getByRole('button', { name: '展开账号', exact: true }).click();
        await page.getByRole('button', { name: '同步账号', exact: true }).click();
        await expect(page.getByText(error.expected, { exact: true })).toBeVisible();
        await expect(page.getByRole('article').filter({ hasText: 'Primary account' })).toContainText(error.raw);
        await expect(page.getByText('服务内部错误，请稍后重试。', { exact: true })).toHaveCount(0);
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}

for (const status of ['failed', 'skipped'] as const) {
    test(`manual checkin displays a ${status} outcome`, async ({ page }) => {
        const account = makeAccount();
        const message = status === 'failed' ? 'checkin failed' : 'checkin is not supported by this platform';
        const state = await mockApp(page, 'checkin', {
            sites: [{ ...makeCheckinSite(), accounts: [account] }],
            mutate: request => {
                expect(request.path).toBe('/api/v1/site/account/checkin/11');
                account.last_checkin_status = status;
                account.last_checkin_message = message;
                return { data: { account_id: 11, site_id: 1, status, message, reward: '' } };
            },
        });
        await page.goto('/');
        await page.getByRole('button', { name: '展开账号', exact: true }).click();
        await page.getByRole('button', { name: '签到并验证', exact: true }).click();
        const expected = `${status === 'failed' ? '失败' : '跳过'}：${message}`;
        await expect(page.getByText(expected, { exact: true })).toBeVisible();
        await expect(page.getByRole('article').filter({ hasText: 'Primary account' })).toContainText(message);
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}
