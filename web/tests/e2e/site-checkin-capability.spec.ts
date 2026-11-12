import { expect, test } from '@playwright/test';
import { SiteCredentialType, SitePlatform, type SiteAccount, type SiteCheckinCapability } from '../../src/api/endpoints/site-types';
import { makeCheckinSite, mockApp } from './fixtures';

test.use({ timezoneId: 'UTC' });

function account(id: number, siteID: number, auto = true): SiteAccount {
    return {
        id, site_id: siteID, name: 'Account ' + id, credential_type: SiteCredentialType.AccessToken,
        username: '', password: '', access_token: 'test-token', api_key: '', refresh_token: '',
        token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: false,
        auto_checkin: auto, random_checkin: false,
        checkin_random_window_minutes: 0, checkin_failure_count: 0,
        last_sync_status: 'idle', last_checkin_status: 'idle', last_sync_message: '',
        last_checkin_message: '', balance: 0, balance_used: 0, today_income: 0,
        tokens: [], user_groups: [], models: [], channel_bindings: [],
    };
}

const doneHubDefault: SiteCheckinCapability = {
    enabled: false, can_verify: true, default_enabled: false, source: 'platform_default', support: 'unknown',
};

test('DoneHub can verify through the account endpoint even with automatic check-in off', async ({ page }) => {
    // Reproduce CI #166: Shanghai is already on the next day while the browser is in UTC.
    const now = new Date('2026-10-27T17:33:00Z');
    await page.clock.setFixedTime(now);
    const verified: SiteCheckinCapability = {
        ...doneHubDefault, enabled: true, source: 'verified', support: 'supported', verified_at: now.toISOString(),
    };
    const state = await mockApp(page, 'checkin', {
        sites: [
            { ...makeCheckinSite(1, 'DoneHub first'), platform: SitePlatform.DoneHub, checkin_capability: doneHubDefault, accounts: [account(11, 1, false)] },
            { ...makeCheckinSite(2, 'DoneHub second'), platform: SitePlatform.DoneHub, checkin_capability: doneHubDefault, accounts: [account(22, 2)] },
        ],
        mutate: request => {
            expect(request).toEqual({ method: 'POST', path: '/api/v1/site/account/checkin/11', body: {} });
            state.sites[0].checkin_capability = verified;
            state.sites[0].accounts[0].last_checkin_status = 'success';
            state.sites[0].accounts[0].last_checkin_at = verified.verified_at;
            return { data: { site_id: 1, account_id: 11, status: 'success', reason: 'checked_in', message: 'checkin success', reward: '3', checkin_capability: verified } };
        },
    });
    await page.goto('/');
    const first = page.locator('section.page-card:visible').filter({ hasText: 'DoneHub first' });
    const second = page.locator('section.page-card:visible').filter({ hasText: 'DoneHub second' });
    await first.getByRole('button', { name: '展开账号', exact: true }).click();
    await second.getByRole('button', { name: '展开账号', exact: true }).click();
    await expect(first.getByText('签到能力尚未验证', { exact: true })).toBeVisible();
    await first.getByRole('button', { name: '签到并验证', exact: true }).click();
    await expect(first.getByText(/^已通过接口确认支持签到/)).toBeVisible();
    await expect(first.getByText('正常', { exact: true })).toBeVisible();
    await expect(first.getByText('未执行', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: '1 成功', exact: true })).toBeVisible();
    await expect(second.getByText('签到能力尚未验证', { exact: true })).toBeVisible();
    await expect(first.getByText(/下次自动签到/)).toHaveCount(0);
    expect(state.sites[0].accounts[0].auto_checkin).toBe(false);
    expect(state.mutations).toHaveLength(1);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

for (const status of ['success', 'failed', 'skipped'] as const) {
    test(`manual check-in ${status} updates the site badge, summary and filters with automatic check-in off`, async ({ page }) => {
        const now = new Date('2026-09-29T17:33:00Z');
        await page.clock.setFixedTime(now);
        const state = await mockApp(page, 'checkin', {
            sites: [{ ...makeCheckinSite(1, 'Manual rewards'), accounts: [{
                ...account(11, 1, false), last_checkin_at: '2026-09-28T17:33:00Z',
                last_checkin_success_at: '2026-09-28T17:33:00Z', last_checkin_status: 'success',
            }] }],
            mutate: request => {
                expect(request).toEqual({ method: 'POST', path: '/api/v1/site/account/checkin/11', body: {} });
                Object.assign(state.sites[0].accounts[0], {
                    last_checkin_at: now.toISOString(), last_checkin_status: status,
                    last_checkin_message: `Manual result: ${status}`,
                    ...(status === 'success' ? { last_checkin_success_at: now.toISOString() } : {}),
                });
                return { data: { site_id: 1, account_id: 11, status, message: `Manual result: ${status}` } };
            },
        });
        await page.goto('/');
        const card = page.locator('section.page-card:visible').filter({ hasText: 'Manual rewards' });
        await expect(card.getByText('未执行', { exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: '0 成功', exact: true })).toBeVisible();
        await card.getByRole('button', { name: '展开账号', exact: true }).click();
        await card.getByRole('button', { name: '签到并验证', exact: true }).click();
        await expect(card.getByText(status === 'success' ? '正常' : '1 异常', { exact: true })).toBeVisible();
        await expect(card.getByText('未执行', { exact: true })).toHaveCount(0);
        await expect(card.getByText(/下次自动签到/)).toHaveCount(0);
        await page.getByRole('button', { name: status === 'success' ? '1 成功' : '1 签到失败', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Manual rewards', exact: true })).toBeVisible();
        await page.getByRole('button', { name: '0 未执行', exact: true }).click();
        await page.getByRole('button', { name: status === 'success' ? '1 成功' : '1 签到失败', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Manual rewards', exact: true })).toHaveCount(0);
        expect(state.sites[0].accounts[0].auto_checkin).toBe(false);
        expect(state.mutations).toHaveLength(1);
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}

test('disabled check-in capability preserves site, account and empty-site status priority', async ({ page }) => {
    const disabledSite = {
        ...makeCheckinSite(1, 'Stopped site'), checkin_mode: 'disabled' as const,
        checkin_capability: { ...doneHubDefault, can_verify: false, source: 'disabled' as const },
    };
    const state = await mockApp(page, 'checkin', {
        sites: [
            { ...disabledSite, enabled: false, accounts: [account(11, 1)] },
            { ...disabledSite, id: 2, name: 'Stopped account', accounts: [{ ...account(22, 2), enabled: false }] },
            { ...disabledSite, id: 3, name: 'Empty site', accounts: [] },
        ],
    });
    await page.goto('/');
    for (const [name, label] of [['Stopped site', '站点停用'], ['Stopped account', '1 已停用'], ['Empty site', '待配置']]) {
        const card = page.locator('section.page-card:visible').filter({ hasText: name });
        await expect(card.getByText(label, { exact: true })).toBeVisible();
        await expect(card.getByText('签到已禁用', { exact: true })).toHaveCount(0);
        await expect(card.getByText('未执行', { exact: true })).toHaveCount(0);
    }
    expect(state.mutations).toEqual([]);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('summary and filters use site capability for custom HTTP and disabled sites', async ({ page }) => {
    const state = await mockApp(page, 'checkin', {
        sites: [
            { ...makeCheckinSite(1, 'Custom API'), platform: SitePlatform.API, checkin_http_enabled: true,
                checkin_capability: { ...doneHubDefault, enabled: true, source: 'custom_http' }, accounts: [account(11, 1)] },
            { ...makeCheckinSite(2, 'Disabled custom API'), platform: SitePlatform.API, checkin_mode: 'disabled', checkin_http_enabled: true,
                checkin_capability: { ...doneHubDefault, can_verify: false, source: 'disabled' }, accounts: [account(22, 2)] },
            { ...makeCheckinSite(3, 'Unverified DoneHub'), platform: SitePlatform.DoneHub,
                checkin_capability: doneHubDefault, accounts: [account(33, 3)] },
        ],
    });
    await page.goto('/');
    const disabled = page.locator('section.page-card:visible').filter({ hasText: 'Disabled custom API' });
    const custom = page.locator('section.page-card:visible').filter({ hasText: 'Custom API' });
    const unverified = page.locator('section.page-card:visible').filter({ hasText: 'Unverified DoneHub' });
    await expect(disabled.getByText('签到已禁用', { exact: true })).toBeVisible();
    await expect(disabled.getByText('未执行', { exact: true })).toHaveCount(0);
    await expect(unverified.getByText('签到未启用', { exact: true })).toBeVisible();
    await expect(unverified.getByText('未执行', { exact: true })).toHaveCount(0);
    await expect(custom.getByText('未执行', { exact: true })).toBeVisible();
    await disabled.getByRole('button', { name: '展开账号', exact: true }).click();
    await expect(disabled.getByText('本站已禁用签到', { exact: true })).toBeVisible();
    await expect(disabled.getByRole('button', { name: /^(签到并验证|立即签到)$/ })).toHaveCount(0);
    await page.getByRole('button', { name: '1 未执行', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Custom API', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Disabled custom API', exact: true })).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Unverified DoneHub', exact: true })).toHaveCount(0);
    expect(state.mutations).toEqual([]);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
