import { expect, test } from '@playwright/test';
import { SiteCredentialType, type SiteAccount } from '../../src/api/endpoints/site-types';
import { makeCheckinSite, makeSite, mockApp } from './fixtures';

function account(siteID: number, status = 'success', overrides: Partial<SiteAccount> = {}): SiteAccount {
    return {
        id: siteID * 10 + 1, site_id: siteID, name: `Account ${siteID}`, credential_type: SiteCredentialType.AccessToken,
        username: '', password: '', access_token: 'test-token', api_key: '', refresh_token: '',
        token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: false,
        auto_checkin: false, random_checkin: false,
        checkin_random_window_minutes: 0, checkin_failure_count: 0,
        last_sync_status: status, last_checkin_status: 'idle', last_sync_message: '',
        last_checkin_message: '', balance: 0, balance_used: 0, today_income: 0,
        tokens: [], user_groups: [], models: [], channel_bindings: [], ...overrides,
    };
}

for (const width of [1440, 390]) {
    test(`site status filters combine with search, tags and sorting independently of checkin at ${width}px`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: 1000 });
        const sites = [
            { ...makeSite(1, 'Alpha normal'), tags: ['常用'], accounts: [account(1)] },
            { ...makeSite(2, 'Zeta failure'), tags: ['常用'], accounts: [account(2, 'failed'), account(2, 'success', { id: 22, name: 'Secondary account' })] },
            { ...makeSite(3, 'Beta failure'), tags: ['备用'], accounts: [account(3, 'failed')] },
            { ...makeSite(4, 'Gamma stopped'), enabled: false, accounts: [account(4, 'failed')] },
            { ...makeSite(5, 'Delta empty stopped'), enabled: false },
            makeSite(6, 'Epsilon pending'),
            { ...makeSite(7, 'Eta inactive account'), accounts: [account(7, 'failed', { enabled: false })] },
            { ...makeSite(8, 'Theta partial'), accounts: [account(8, 'partial')] },
        ];
        const state = await mockApp(page, 'site', {
            sites: [...sites, { ...makeCheckinSite(9, 'Daily rewards'), accounts: [account(9, 'failed', { auto_checkin: true })] }],
        });
        await page.goto('/');
        const filters = page.getByRole('group', { name: '站点状态筛选', exact: true });
        const all = filters.getByRole('button', { name: /全部$/ });
        const normal = filters.getByRole('button', { name: /正常$/ });
        const abnormal = filters.getByRole('button', { name: /异常$/ });
        const disabled = filters.getByRole('button', { name: /停用$/ });
        const headings = page.getByRole('heading', { name: / (normal|failure|stopped|pending|inactive account|partial)$/ });
        const expectSites = async (names: string[]) => {
            await expect.poll(async () => (await headings.allTextContents()).sort()).toEqual([...names].sort());
        };

        await expect(filters.getByRole('button')).toHaveText([/8\s*全部/, /4\s*正常/, /2\s*异常/, /2\s*停用/]);
        await expect(all).toHaveAttribute('aria-pressed', 'true');
        await normal.press('Enter');
        await expect(normal).toHaveAttribute('aria-pressed', 'true');
        await expectSites(['Alpha normal', 'Epsilon pending', 'Eta inactive account', 'Theta partial']);
        await abnormal.click();
        await expect(headings).toHaveCount(6);
        await normal.click();
        await expectSites(['Beta failure', 'Zeta failure']);
        await expect(page.getByText('2 站点 / 3 账号', { exact: true })).toBeVisible();

        await all.click();
        await disabled.click();
        await expectSites(['Gamma stopped', 'Delta empty stopped']);
        await page.getByRole('button', { name: '清空筛选', exact: true }).click();
        await expectSites(sites.map(site => site.name));

        await abnormal.click();
        await page.getByRole('button', { name: '常用 · 2', exact: true }).click();
        await expectSites(['Zeta failure']);
        await expect(page.getByText('1 站点 / 2 账号', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: '搜索', exact: true }).click();
        const search = page.getByRole('textbox', { name: '搜索', exact: true });
        await search.fill('Secondary account');
        await expectSites(['Zeta failure']);
        await expect(page.getByText('1 站点 / 1 账号', { exact: true })).toBeVisible();
        await search.fill('Alpha');
        await expect(page.getByText('没有匹配的站点', { exact: true })).toBeVisible();
        await expect(filters.getByRole('button')).toHaveText([/8\s*全部/, /4\s*正常/, /2\s*异常/, /2\s*停用/]);
        await page.getByRole('button', { name: '清空筛选', exact: true }).first().click();
        await expect(all).toHaveAttribute('aria-pressed', 'true');
        await expect(search).toHaveValue('');
        await expect(page.getByRole('button', { name: '常用 · 2', exact: true })).toHaveAttribute('aria-pressed', 'false');
        await expectSites(sites.map(site => site.name));

        await abnormal.click();
        await page.getByRole('button', { name: '视图选项', exact: true }).click();
        await page.getByRole('button', { name: '名称正序', exact: true }).click();
        await expect(headings).toHaveText(['Beta failure', 'Zeta failure']);
        await page.getByRole('button', { name: '名称倒序', exact: true }).click();
        await expect(headings).toHaveText(['Zeta failure', 'Beta failure']);
        await page.keyboard.press('Escape');

        await page.getByRole('navigation').getByRole('button', { name: '签到', exact: true }).click();
        await expect(page.getByRole('heading', { name: 'Daily rewards', exact: true })).toBeVisible();
        await expect(filters).toHaveCount(0);
        await page.getByRole('button', { name: /^1\s*未执行$/ }).click();
        await expect(page.getByRole('heading', { name: 'Daily rewards', exact: true })).toBeVisible();
        await page.getByRole('button', { name: '清空筛选', exact: true }).click();
        await page.getByRole('navigation').getByRole('button', { name: '站点', exact: true }).click();
        await expect(abnormal).toHaveAttribute('aria-pressed', 'true');
        await expectSites(['Beta failure', 'Zeta failure']);
        await filters.scrollIntoViewIfNeeded();
        for (const button of await filters.getByRole('button').all()) {
            const bounds = await button.boundingBox();
            expect(bounds!.x).toBeGreaterThanOrEqual(0);
            expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width + 1);
        }
        await page.screenshot({ path: testInfo.outputPath('site-status-filters.png'), animations: 'disabled' });
        expect(state.mutations).toEqual([]);
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}

test('changing site enablement updates status counts and the filtered results', async ({ page }) => {
    const state = await mockApp(page, 'site', {
        sites: [{ ...makeSite(1, 'Failed site'), accounts: [account(1, 'failed')] }, makeSite(2, 'Healthy site')],
        mutate: request => {
            expect(request.method).toBe('POST');
            expect(request.path).toBe('/api/v1/site/enable');
            const body = request.body as { id: number; enabled: boolean };
            expect(body.id).toBe(1);
            state.sites[0] = { ...state.sites[0], enabled: body.enabled };
            return { data: null };
        },
    });
    await page.goto('/');
    const filters = page.getByRole('group', { name: '站点状态筛选', exact: true });
    const failedSite = page.locator('section.page-card:visible').filter({ has: page.getByRole('heading', { name: 'Failed site', exact: true }) });
    await filters.getByRole('button', { name: /异常$/ }).click();
    await failedSite.getByRole('button', { name: '更多站点操作', exact: true }).click();
    await page.getByRole('button', { name: '停用站点', exact: true }).click();
    await expect(page.getByText('没有匹配的站点', { exact: true })).toBeVisible();
    await expect(filters.getByRole('button')).toHaveText([/2\s*全部/, /1\s*正常/, /0\s*异常/, /1\s*停用/]);
    await filters.getByRole('button', { name: /停用$/ }).click();
    await expect(failedSite).toBeVisible();
    await expect(failedSite.getByText('站点停用', { exact: true })).toBeVisible();
    await failedSite.getByRole('button', { name: '更多站点操作', exact: true }).click();
    await page.getByRole('button', { name: '启用站点', exact: true }).click();
    await expect(filters.getByRole('button')).toHaveText([/2\s*全部/, /1\s*正常/, /1\s*异常/, /0\s*停用/]);
    await expect(failedSite.getByText('1 异常', { exact: true })).toBeVisible();
    expect(state.mutations).toEqual([
        { method: 'POST', path: '/api/v1/site/enable', body: { id: 1, enabled: false } },
        { method: 'POST', path: '/api/v1/site/enable', body: { id: 1, enabled: true } },
    ]);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
