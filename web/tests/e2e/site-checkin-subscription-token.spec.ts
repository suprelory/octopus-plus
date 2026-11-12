import { expect, test } from '@playwright/test';
import { SiteCredentialType, SitePlatform, type SiteAccount } from '../../src/api/endpoints/site';
import { makeCheckinSite, makeSite, mockApp } from './fixtures';

const source: SiteAccount = {
    id: 11, site_id: 1, name: 'Sub2API token account', credential_type: SiteCredentialType.AccessToken,
    username: '', password: '', access_token: 'subscription-token', api_key: '', refresh_token: '',
    token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: true, auto_checkin: false,
    random_checkin: false, checkin_random_window_minutes: 0,
    checkin_failure_count: 0, last_sync_status: '', last_checkin_status: '', last_sync_message: '', last_checkin_message: '',
    balance: 0, balance_used: 0, today_income: 0, tokens: [], user_groups: [], models: [], channel_bindings: [],
};

test('Sub2API custom checkin saves the subscription token option and clears it when HTTP is disabled', async ({ page }) => {
    const state = await mockApp(page, 'checkin', {
        sites: [
            { ...makeSite(1), platform: SitePlatform.Sub2API, accounts: [source] },
            { ...makeCheckinSite(2), platform: SitePlatform.Sub2API, linked_site_id: 1, checkin_http_enabled: true, checkin_http_path: '/daily' },
        ],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/update');
            state.sites[1] = { ...state.sites[1], ...request.body as object };
            return { data: state.sites[1] };
        },
    });
    await page.goto('/');
    async function openEditor() {
        await page.getByRole('button', { name: '更多站点操作', exact: true }).click();
        await page.getByRole('button', { name: '编辑站点', exact: true }).click();
        return page.getByRole('dialog');
    }
    const dialog = await openEditor();
    const option = dialog.getByRole('switch', { name: '使用订阅账号 Access Token', exact: true });
    await expect(option).not.toBeChecked();
    await option.check();
    await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations[0].body).toMatchObject({ linked_site_id: 1, checkin_http_enabled: true, checkin_use_subscription_token: true });
    await openEditor();
    await expect(option).toBeChecked();
    await dialog.getByRole('switch', { name: '启用自定义 HTTP 签到', exact: true }).uncheck();
    await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations[1].body).toMatchObject({ checkin_http_enabled: false, checkin_use_subscription_token: false });
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('subscription token checkin selects only Access Token accounts without copying credentials', async ({ page }) => {
    const keyAccount = { ...source, id: 12, name: 'API key only', credential_type: SiteCredentialType.APIKey, access_token: '', api_key: 'source-key' };
    const state = await mockApp(page, 'checkin', {
        sites: [
            { ...makeSite(1), platform: SitePlatform.Sub2API, accounts: [source, keyAccount] },
            { ...makeCheckinSite(2), platform: SitePlatform.Sub2API, linked_site_id: 1, checkin_http_enabled: true, checkin_http_path: '/daily', checkin_use_subscription_token: true },
        ],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/account/create');
            expect(request.body).toMatchObject({ credential_type: 'linked_account', linked_account_id: 11, cookie: '', access_token: '', refresh_token: '', api_key: '' });
            return { data: { id: 22, ...request.body as object } };
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '新增账号', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('账号名称', { exact: true }).fill('Daily token checkin');
    await expect(dialog.getByLabel('签到站 Cookie', { exact: true })).toHaveCount(0);
    await expect(dialog.getByPlaceholder('请输入 Access Token')).toHaveCount(0);
    await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
    expect(state.mutations).toHaveLength(0);
    await dialog.getByRole('combobox', { name: '订阅站账号', exact: true }).click();
    await expect(page.getByRole('option', { name: 'API key only', exact: true })).toHaveCount(0);
    await page.getByRole('option', { name: source.name, exact: true }).click();
    await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations).toHaveLength(1);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('subscription token option requires a Sub2API subscription link', async ({ page }) => {
    const state = await mockApp(page, 'checkin', {
        sites: [{ ...makeCheckinSite(2), platform: SitePlatform.Sub2API, checkin_http_enabled: true, checkin_http_path: '/daily' }],
    });
    await page.goto('/');
    await page.getByRole('button', { name: '更多站点操作', exact: true }).click();
    await page.getByRole('button', { name: '编辑站点', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('switch', { name: '使用订阅账号 Access Token', exact: true })).toBeDisabled();
    await dialog.getByRole('combobox', { name: '平台类型', exact: true }).click();
    await page.getByRole('option', { name: 'New API', exact: true }).click();
    await expect(dialog.getByRole('switch', { name: '使用订阅账号 Access Token', exact: true })).toHaveCount(0);
    expect(state.mutations).toHaveLength(0);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
