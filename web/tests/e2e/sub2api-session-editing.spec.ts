import { expect, test } from '@playwright/test';
import { SiteCredentialType, SitePlatform, type SiteAccount } from '../../src/api/endpoints/site';
import { makeSite, mockApp } from './fixtures';

const account: SiteAccount = {
    id: 11, site_id: 1, name: 'Sub2API session', credential_type: SiteCredentialType.AccessToken,
    username: '', password: '', access_token: 'old-access', api_key: '', refresh_token: 'rt_old',
    token_expires_at: 0, credential_version: 7, proxy_mode: 'inherit', enabled: true,
    auto_sync: true, auto_checkin: false, random_checkin: false, checkin_interval_hours: 24,
    checkin_random_window_minutes: 0, checkin_failure_count: 0, last_sync_status: '', last_checkin_status: '',
    last_sync_message: '', last_checkin_message: '', balance: 0, balance_used: 0, today_income: 0,
    tokens: [], user_groups: [], models: [], channel_bindings: [],
};

test('renaming an account does not overwrite tokens refreshed while its editor was open', async ({ page }) => {
    const state = await mockApp(page, 'site', {
        sites: [{ ...makeSite(), platform: SitePlatform.Sub2API, accounts: [{ ...account }] }],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/account/update');
            const updated = { ...state.sites[0].accounts[0], ...request.body as object };
            state.sites[0].accounts = [updated];
            return { data: updated };
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '展开账号', exact: true }).click();
    await page.getByRole('button', { name: '更多账号操作', exact: true }).click();
    await page.getByRole('button', { name: '编辑账号', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByPlaceholder('请输入 Access Token', { exact: true })).toHaveValue('old-access');
    state.sites[0].accounts = [{ ...account, access_token: 'refreshed-access', refresh_token: 'rt_refreshed', credential_version: 8 }];
    await dialog.getByLabel('账号名称', { exact: true }).fill('Renamed session');
    await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations).toHaveLength(1);
    for (const field of ['access_token', 'refresh_token', 'token_expires_at', 'credential_type', 'credential_version']) {
        expect(state.mutations[0].body).not.toHaveProperty(field);
    }
    expect(state.sites[0].accounts[0]).toMatchObject({
        name: 'Renamed session', access_token: 'refreshed-access', refresh_token: 'rt_refreshed', credential_version: 8,
    });
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('editing credentials sends the opening version and keeps the draft on a conflict', async ({ page }) => {
    const message = '账号凭据已更新或缺少凭据版本，请刷新页面并重新打开编辑窗口后再保存';
    const state = await mockApp(page, 'site', {
        sites: [{ ...makeSite(), platform: SitePlatform.Sub2API, accounts: [{ ...account }] }],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/account/update');
            const payload = request.body as Partial<SiteAccount>;
            if (payload.credential_version !== state.sites[0].accounts[0].credential_version) {
                return { status: 409, errorCode: 'site.account.credential_conflict', message };
            }
            throw new Error('stale form unexpectedly used the new credential version');
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '展开账号', exact: true }).click();
    await page.getByRole('button', { name: '更多账号操作', exact: true }).click();
    await page.getByRole('button', { name: '编辑账号', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByPlaceholder('请输入 Access Token', { exact: true })).toHaveValue('old-access');
    state.sites[0].accounts = [{ ...account, access_token: 'refreshed-access', refresh_token: 'rt_refreshed', credential_version: 8 }];
    await dialog.getByPlaceholder('请输入 Access Token', { exact: true }).fill('manual-access');
    await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
    await expect(page.getByText(message, { exact: true })).toBeVisible();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByPlaceholder('请输入 Access Token', { exact: true })).toHaveValue('manual-access');
    expect(state.mutations[0].body).toMatchObject({ access_token: 'manual-access', credential_version: 7 });
    expect(state.mutations[0].body).not.toHaveProperty('refresh_token');
    expect(state.sites[0].accounts[0]).toMatchObject({ access_token: 'refreshed-access', refresh_token: 'rt_refreshed', credential_version: 8 });
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
