import { expect, test, type Page } from '@playwright/test';
import { SiteCredentialType, type SiteAccount } from '../../src/api/endpoints/site-types';
import { makeCheckinSite, mockApp } from './fixtures';

test.use({ timezoneId: 'UTC' });

function account(siteID: number, status: 'success' | 'failed', checkedAt: string): SiteAccount {
    return {
        id: siteID * 10 + 1, site_id: siteID, name: `Account ${siteID}`, credential_type: SiteCredentialType.AccessToken,
        username: '', password: '', access_token: 'test-token', api_key: '', refresh_token: '',
        token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: false,
        auto_checkin: true, random_checkin: false,
        checkin_random_window_minutes: 0, checkin_failure_count: 0,
        last_sync_status: 'idle', last_sync_message: '', last_checkin_message: '',
        last_checkin_status: status, last_checkin_at: checkedAt,
        last_checkin_success_at: status === 'success' ? checkedAt : undefined,
        balance: 0, balance_used: 0, today_income: 0,
        tokens: [], user_groups: [], models: [], channel_bindings: [],
    };
}

function siteCard(page: Page, name: string) {
    return page.locator('section.page-card:visible').filter({
        has: page.getByRole('heading', { name, exact: true }),
    });
}

async function expectSites(page: Page, names: string[]) {
    await expect.poll(async () => (await page.locator('section.page-card:visible h2').allTextContents()).sort())
        .toEqual([...names].sort());
}

for (const timezoneId of ['UTC', 'Asia/Shanghai', 'America/Los_Angeles']) {
    test.describe(timezoneId, () => {
        test.use({ timezoneId });

        test('summary, badges and filters use each site date independently of the browser', async ({ page }) => {
            await page.clock.setFixedTime(new Date('2026-10-27T17:33:00Z'));
            const state = await mockApp(page, 'checkin', {
                sites: [
                    { ...makeCheckinSite(1, 'Shanghai today'), checkin_timezone: 'Asia/Shanghai',
                        accounts: [account(1, 'success', '2026-10-27T17:00:00Z')] },
                    { ...makeCheckinSite(2, 'Shanghai yesterday'), checkin_timezone: 'Asia/Shanghai',
                        accounts: [account(2, 'failed', '2026-10-27T15:00:00Z')] },
                    { ...makeCheckinSite(3, 'UTC today'), checkin_timezone: 'UTC',
                        accounts: [account(3, 'failed', '2026-10-27T15:00:00Z')] },
                    { ...makeCheckinSite(4, 'Los Angeles today'), checkin_timezone: 'America/Los_Angeles',
                        accounts: [account(4, 'success', '2026-10-27T08:00:00Z')] },
                    { ...makeCheckinSite(5, 'Los Angeles yesterday'), checkin_timezone: 'America/Los_Angeles',
                        accounts: [account(5, 'success', '2026-10-27T06:00:00Z')] },
                ],
            });
            await page.goto('/');

            for (const [name, badge] of [
                ['Shanghai today', '正常'], ['Shanghai yesterday', '未执行'], ['UTC today', '1 异常'],
                ['Los Angeles today', '正常'], ['Los Angeles yesterday', '未执行'],
            ]) {
                await expect(siteCard(page, name).getByText(badge, { exact: true })).toBeVisible();
            }
            for (const { filter, names } of [
                { filter: '2 成功', names: ['Shanghai today', 'Los Angeles today'] },
                { filter: '1 签到失败', names: ['UTC today'] },
                { filter: '2 未执行', names: ['Shanghai yesterday', 'Los Angeles yesterday'] },
            ]) {
                await page.getByRole('button', { name: '5 全部', exact: true }).click();
                await page.getByRole('button', { name: filter, exact: true }).click();
                await expectSites(page, names);
            }
            expect(state.mutations).toEqual([]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });
    });
}

for (const { start, checkedAt, expired, current } of [
    { start: '2026-10-27T15:58:00Z', checkedAt: '2026-10-27T15:00:00Z', expired: 'Shanghai', current: 'UTC' },
    { start: '2026-10-27T23:58:00Z', checkedAt: '2026-10-27T23:00:00Z', expired: 'UTC', current: 'Shanghai' },
]) {
    test(`${expired} midnight refreshes counts, badges and an active filter without changing site data`, async ({ page }) => {
        await page.clock.install({ time: new Date(start) });
        const state = await mockApp(page, 'checkin', {
            sites: [
                { ...makeCheckinSite(1, 'Shanghai success'), checkin_timezone: 'Asia/Shanghai',
                    accounts: [account(1, 'success', checkedAt)] },
                { ...makeCheckinSite(2, 'Shanghai failed'), checkin_timezone: 'Asia/Shanghai',
                    accounts: [account(2, 'failed', checkedAt)] },
                { ...makeCheckinSite(3, 'UTC success'), checkin_timezone: 'UTC',
                    accounts: [account(3, 'success', checkedAt)] },
                { ...makeCheckinSite(4, 'UTC failed'), checkin_timezone: 'UTC',
                    accounts: [account(4, 'failed', checkedAt)] },
            ],
        });
        await page.goto('/');
        await expect(page.getByRole('button', { name: '2 签到失败', exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: '0 未执行', exact: true })).toBeVisible();
        await page.getByRole('button', { name: '2 成功', exact: true }).click();
        await expectSites(page, ['Shanghai success', 'UTC success']);

        // Advance across the site's midnight with the same API data and filter selection.
        await page.clock.fastForward(3 * 60_000);
        await expect(page.getByRole('button', { name: '1 成功', exact: true })).toHaveAttribute('aria-pressed', 'true');
        await expect(page.getByRole('button', { name: '1 签到失败', exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: '2 未执行', exact: true })).toBeVisible();
        await expectSites(page, [`${current} success`]);

        await page.getByRole('button', { name: '4 全部', exact: true }).click();
        await expect(siteCard(page, `${expired} success`).getByText('未执行', { exact: true })).toBeVisible();
        await expect(siteCard(page, `${expired} failed`).getByText('未执行', { exact: true })).toBeVisible();
        await expect(siteCard(page, `${current} success`).getByText('正常', { exact: true })).toBeVisible();
        await expect(siteCard(page, `${current} failed`).getByText('1 异常', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: '2 未执行', exact: true }).click();
        await expectSites(page, [`${expired} success`, `${expired} failed`]);
        expect(state.mutations).toEqual([]);
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}
