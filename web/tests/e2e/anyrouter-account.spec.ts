import { expect, test } from '@playwright/test';
import { SitePlatform } from '../../src/api/endpoints/site';
import { makeSite, mockApp } from './fixtures';

for (const userID of ['', '24680']) {
    test(`AnyRouter account saves with ${userID ? 'an explicit' : 'an omitted'} user ID`, async ({ page }) => {
        const state = await mockApp(page, 'site', {
            sites: [{ ...makeSite(), platform: SitePlatform.AnyRouter }],
            mutate: request => {
                expect(request.path).toBe('/api/v1/site/account/create');
                return { data: { id: 11, ...request.body as object } };
            },
        });
        await page.goto('/');
        await page.getByRole('button', { name: '新增账号', exact: true }).click();
        const dialog = page.getByRole('dialog');
        await dialog.getByLabel('账号名称', { exact: true }).fill('AnyRouter account');
        await dialog.getByPlaceholder('请输入 Access Token', { exact: true }).fill('session=test-session');
        const field = dialog.getByRole('textbox', { name: /^Platform User ID/ });
        await expect(field).toBeVisible();
        await field.fill('1.5');
        await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
        await expect(page.getByText('Platform User ID 必须是大于 0 的整数', { exact: true })).toBeVisible();
        expect(state.mutations).toHaveLength(0);
        await field.fill(userID);
        await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
        await expect(dialog).not.toBeVisible();
        expect(state.mutations).toHaveLength(1);
        expect(state.mutations[0].body).toMatchObject({
            access_token: 'session=test-session', platform_user_id: userID ? 24680 : null,
        });
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}
