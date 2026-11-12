import { expect, test } from '@playwright/test';
import { SiteCredentialType, SitePlatform, type SiteAccount } from '../../src/api/endpoints/site';
import { makeCheckinSite, makeSite, mockApp } from './fixtures';

test('AgentRouter subscription defaults to password credentials and still supports tokens', async ({ page }) => {
    const state = await mockApp(page, 'site', {
        sites: [{ ...makeSite(), platform: SitePlatform.AgentRouter }],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/account/create');
            return { data: { id: 11, ...request.body as object } };
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '新增账号', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByPlaceholder('请输入用户名', { exact: true })).toBeVisible();
    await expect(dialog.getByText(/第三方登录账号需先在站点绑定邮箱/)).toBeVisible();
    await dialog.getByRole('combobox', { name: '凭据类型', exact: true }).click();
    await page.getByRole('option', { name: 'Access Token', exact: true }).click();
    await expect(dialog.getByRole('textbox', { name: /^Platform User ID/ })).not.toHaveAttribute('required');
    await dialog.getByRole('combobox', { name: '凭据类型', exact: true }).click();
    await page.getByRole('option', { name: '用户名 / 密码', exact: true }).click();
    await dialog.getByLabel('账号名称', { exact: true }).fill('AgentRouter account');
    await dialog.getByPlaceholder('请输入用户名', { exact: true }).fill('user@example.com');
    await dialog.getByPlaceholder('请输入密码', { exact: true }).fill('password');
    await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations[0].body).toMatchObject({ credential_type: 'username_password', username: 'user@example.com', password: 'password' });
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('AgentRouter checkin only offers linked password accounts', async ({ page }) => {
    const source: SiteAccount = {
        id: 11, site_id: 1, name: 'Password account', credential_type: SiteCredentialType.UsernamePassword,
        username: 'user@example.com', password: 'password', access_token: '', api_key: '', refresh_token: '',
        token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: true,
        auto_checkin: false, random_checkin: false, checkin_random_window_minutes: 0,
        checkin_failure_count: 0, last_sync_status: '', last_checkin_status: '', last_sync_message: '', last_checkin_message: '',
        balance: 0, balance_used: 0, today_income: 0, tokens: [], user_groups: [], models: [], channel_bindings: [],
    };
    const state = await mockApp(page, 'checkin', {
        sites: [{ ...makeSite(1), platform: SitePlatform.AgentRouter, accounts: [source, {
            ...source, id: 12, name: 'Token account', credential_type: SiteCredentialType.AccessToken, access_token: 'token', username: '', password: '',
        }] }, { ...makeCheckinSite(2), platform: SitePlatform.AgentRouter, linked_site_id: 1, base_url: 'https://site-1.example' }],
        mutate: request => {
            expect(request.path).toBe('/api/v1/site/account/create');
            expect(request.body).toMatchObject({ credential_type: 'linked_account', linked_account_id: 11, access_token: '', password: '' });
            return { data: { id: 22, ...request.body as object } };
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '新增账号', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('账号名称', { exact: true }).fill('Daily rewards');
    await dialog.getByRole('combobox', { name: '订阅站账号', exact: true }).click();
    await expect(page.getByRole('option', { name: 'Token account', exact: true })).toHaveCount(0);
    await page.getByRole('option', { name: 'Password account', exact: true }).click();
    await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.mutations).toHaveLength(1);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
