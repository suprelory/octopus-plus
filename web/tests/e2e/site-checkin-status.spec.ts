import { expect, test } from '@playwright/test';
import { makeCheckinSite, mockApp } from './fixtures';

for (const width of [1440, 390]) {
    test.describe(`status confirmation ${width}px`, () => {
        test.use({ viewport: { width, height: 1000 } });
        test('saves status requests, previews rules and can return to response mode', async ({ page }) => {
            const state = await mockApp(page, 'checkin', {
                sites: [makeCheckinSite()],
                mutate: request => {
                    if (request.path === '/api/v1/site/checkin-status/test') {
                        expect(request.body).toMatchObject({ http_status: 200, config: { checked_path: 'data.done', path: '/api/checkin/status' } });
                        return { data: { state: 'checked_in' } };
                    }
                    expect(request.path).toBe('/api/v1/site/update');
                    state.sites[0] = { ...state.sites[0], ...request.body as object };
                    return { data: state.sites[0] };
                },
            });
            await page.goto('/');
            async function openEditor() {
                await page.getByRole('button', { name: '更多站点操作' }).click();
                await page.getByRole('button', { name: '编辑站点', exact: true }).click();
                return page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: '编辑签到站点' }) });
            }
            const dialog = await openEditor();
            await dialog.getByRole('switch', { name: '启用自定义 HTTP 签到', exact: true }).check();
            await dialog.getByLabel('请求路径', { exact: true }).fill('/api/checkin');
            await dialog.getByRole('switch', { name: '通过状态接口确认签到', exact: true }).check();
            await expect(dialog.getByRole('switch', { name: '签到前检查状态', exact: true })).toBeChecked();
            await dialog.getByLabel('状态字段路径', { exact: true }).fill('data.done');
            await dialog.getByLabel('每次最多查询次数', { exact: true }).fill('4');
            await dialog.getByRole('combobox', { name: '默认奖励来源', exact: true }).click();
            await page.getByRole('option', { name: '状态响应 data.reward', exact: true }).click();
            await dialog.getByText('用响应样例测试判定', { exact: true }).click();
            await dialog.getByLabel('状态响应样例', { exact: true }).fill('{"data":{"done":true}}');
            await dialog.getByRole('button', { name: '测试状态判定', exact: true }).click();
            await expect(dialog.getByText('判定结果：已签到', { exact: true })).toBeVisible();
            expect(state.mutations).toHaveLength(1);
            expect((await dialog.boundingBox())!.width).toBeLessThanOrEqual(width);
            await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
            await expect(dialog).not.toBeVisible();
            expect(state.mutations[1].body).toMatchObject({ checkin_status: {
                method: 'GET', path: '/api/checkin/status', checked_path: 'data.done', checked_value: true,
                unchecked_value: false, max_attempts: 4, skip_precheck: false, reward_source: 'status',
            } });
            const reopened = await openEditor();
            await expect(reopened.getByLabel('状态字段路径', { exact: true })).toHaveValue('data.done');
            await reopened.getByRole('switch', { name: '通过状态接口确认签到', exact: true }).uncheck();
            await reopened.getByRole('button', { name: '保存修改', exact: true }).click();
            await expect(reopened).not.toBeVisible();
            expect(state.mutations[2].body).toMatchObject({ checkin_status: null });
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });
    });
}
