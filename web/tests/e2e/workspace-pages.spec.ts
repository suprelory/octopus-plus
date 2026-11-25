import { expect, test, type Page, type TestInfo } from '@playwright/test';
import type { Group } from '../../src/api/endpoints/group';
import type { RelayLog } from '../../src/api/endpoints/log';
import { emptyStats, makeChannel, mockApp } from './fixtures';

const models = [
    { name: 'gpt-4.1', input: 2, output: 8, cache_read: 0.5, cache_write: 0 },
    { name: 'claude-sonnet-4', input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 },
    { name: 'gemini-2.5-flash', input: 0.3, output: 2.5, cache_read: 0.075, cache_write: 0 },
];
const groups: Group[] = models.map((model, index) => ({ id: index + 1, name: model.name, mode: index === 0 ? 3 : 1,
    match_regex: '', items: [{ id: index + 1, channel_id: 1, model_name: model.name, priority: 1, weight: 1 }] }));
const logs: RelayLog[] = models.map((model, index) => ({ id: index + 1, time: 1790300000 - index * 30,
    request_model_name: model.name, actual_model_name: model.name, channel: 1, channel_name: 'OpenAI Primary',
    endpoint_type: 'openai_chat', request_api_key_name: 'Development', client_ip: '192.0.2.10',
    input_tokens: 2400, bill_input_tokens: 2400, output_tokens: 800, ftut: 350, use_time: 3200, cost: 0.016, error: index === 2 ? 'Upstream timeout' : '' }));

async function capture(page: Page, info: TestInfo, name: string) {
    await page.waitForTimeout(500);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: info.outputPath(`${name}.png`), fullPage: true, animations: 'disabled' });
}

for (const { width, theme } of [{ width: 1440, theme: 'light' }, { width: 390, theme: 'dark' }] as const) {
    test.describe(`${width}px ${theme} workspace`, () => {
        test.use({ viewport: { width, height: 1000 } });
        test.beforeEach(async ({ page }) => {
            await page.addInitScript(theme => {
                localStorage.setItem('theme', theme);
                localStorage.setItem('log-ui-storage', JSON.stringify({ state: { liveEnabled: false, pageSize: 20 }, version: 0 }));
            }, theme);
        });

        test('group overview retains filtering and expandable members', async ({ page }, info) => {
            const state = await mockApp(page, 'group', { groups });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '分组概览' })).toBeVisible();
            const group = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'gpt-4.1', exact: true }) });
            const expand = group.getByRole('button', { expanded: false });
            await expand.focus();
            await page.keyboard.press('Enter');
            await expect(group.getByRole('button', { name: '故障转移', exact: true })).toHaveAttribute('aria-pressed', 'true');
            await capture(page, info, 'groups');
            await page.getByRole('button', { name: '搜索', exact: true }).click();
            await page.getByRole('textbox', { name: '搜索', exact: true }).fill('no-matching-group');
            await expect(page.getByText('没有找到匹配的分组或模型。', { exact: true })).toBeVisible();
            expect(state.mutations).toEqual([]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('model prices support editing, canceling deletion, search and list layout', async ({ page }, info) => {
            const state = await mockApp(page, 'model', { models: models.map(model => ({ ...model })), mutate: request => {
                expect(request.path).toBe('/api/v1/model/update');
                const updated = { ...models[0], input: 2.25 };
                expect(request.body).toEqual(updated);
                state.models[0] = updated;
                return { data: updated };
            } });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '模型价格', exact: true })).toBeVisible();
            await expect(page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'gemini-2.5-flash', exact: true }) })).toContainText('0.075');
            await capture(page, info, 'models');
            const card = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'gpt-4.1', exact: true }) });
            await card.getByRole('button', { name: '编辑', exact: true }).click();
            await page.getByRole('spinbutton', { name: '输入', exact: true }).fill('2.25');
            await page.getByRole('button', { name: '保存', exact: true }).click();
            await expect(card).toContainText('2.25');
            await card.getByRole('button', { name: '删除', exact: true }).click();
            await card.getByRole('button', { name: '取消', exact: true }).click();
            await page.getByRole('button', { name: '视图选项' }).click();
            await page.getByRole('button', { name: '列表', exact: true }).click();
            await page.keyboard.press('Escape');
            await capture(page, info, 'models-list');
            await page.getByRole('button', { name: '搜索', exact: true }).click();
            await page.getByRole('textbox', { name: '搜索', exact: true }).fill('no-matching-model');
            await expect(page.getByText('没有匹配的模型', { exact: true })).toBeVisible();
            expect(state.mutations).toHaveLength(1);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('settings categories retain account drafts and expose every settings panel', async ({ page }, info) => {
            const state = await mockApp(page, 'setting', { apiKeys: [{ id: 1, name: 'Development', api_key: 'sk-demo-only', enabled: true, max_cost: 100 }] });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '工作区设置' })).toBeVisible();
            await page.getByPlaceholder('请输入新用户名', { exact: true }).fill('draft-admin');
            await capture(page, info, 'settings-general');
            const navigation = page.getByRole('group', { name: '设置分类' });
            for (const [category, heading, name] of [
                ['访问密钥', 'API 密钥', 'settings-keys'],
                ['连接与任务', '网络与服务', 'settings-connections'],
                ['数据与备份', '数据管理', 'settings-data'],
            ]) {
                await navigation.getByRole('button', { name: category, exact: true }).click();
                await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
                await capture(page, info, name);
            }
            await navigation.getByRole('button', { name: '通用', exact: true }).click();
            await expect(page.getByPlaceholder('请输入新用户名', { exact: true })).toHaveValue('draft-admin');
            expect(state.mutations).toEqual([]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('request history keeps detail, field visibility and search controls', async ({ page }, info) => {
            const state = await mockApp(page, 'log', { logs, channels: [makeChannel()] });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '请求记录' })).toBeVisible();
            await expect(page.getByRole('button', { name: '开启实时推送' })).toHaveAttribute('aria-pressed', 'false');
            await expect(page.getByText('Upstream timeout', { exact: true })).toBeVisible();
            await capture(page, info, 'logs');
            await page.getByRole('button', { name: /gpt-4.1/ }).first().click();
            await expect(page.getByRole('dialog')).toBeVisible();
            await expect(page.getByRole('dialog').getByText('response-demo', { exact: false })).toBeVisible();
            await capture(page, info, 'log-detail');
            await page.keyboard.press('Escape');
            await page.getByRole('button', { name: '显示字段', exact: true }).click();
            await page.getByRole('checkbox', { name: '客户端 IP', exact: true }).uncheck();
            await page.keyboard.press('Escape');
            await expect(page.getByText('192.0.2.10', { exact: true })).toHaveCount(0);
            await page.getByRole('button', { name: '搜索', exact: true }).click();
            await page.getByRole('textbox', { name: '搜索', exact: true }).fill('no-matching-request');
            await expect(page.getByText('没有符合条件的日志', { exact: true })).toBeVisible();
            expect(state.mutations).toEqual([]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('login methods remain usable and report failed authentication', async ({ page }, info) => {
            const state = await mockApp(page, 'home', { auth: 'guest', mutate: request => {
                expect(request.path).toBe('/api/v1/user/login');
                expect(request.body).toEqual({ username: 'demo-user', password: 'invalid-demo-password', expire: -1 });
                return { status: 401, message: 'Invalid demo credentials' };
            } });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible();
            await capture(page, info, 'login');
            await page.getByRole('tab', { name: '密钥登录', exact: true }).click();
            await expect(page.getByLabel('API 密钥', { exact: true })).toBeVisible();
            await expect(page.getByLabel('用户名', { exact: true })).not.toBeVisible();
            await page.getByRole('tab', { name: '账户登录', exact: true }).click();
            await page.getByLabel('用户名', { exact: true }).fill('demo-user');
            await page.getByLabel('密码', { exact: true }).fill('invalid-demo-password');
            await page.getByRole('button', { name: '登录', exact: true }).click();
            await expect(page.getByRole('alert').filter({ hasText: '登录失败,请检查登录凭据' })).toBeVisible();
            await expect(page.getByLabel('用户名', { exact: true })).toHaveValue('demo-user');
            expect(state.mutations).toHaveLength(1);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('bootstrap form stays within the viewport and validates passwords', async ({ page }, info) => {
            const state = await mockApp(page, 'home', { auth: 'guest', bootstrapRequired: true });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '初始化管理员' })).toBeVisible();
            await capture(page, info, 'bootstrap');
            await page.locator('#bootstrap-token').fill('demo-bootstrap-token');
            await page.locator('#bootstrap-password').fill('demo-password-one');
            await page.locator('#bootstrap-password-confirm').fill('demo-password-two');
            await page.getByRole('button', { name: /创建/ }).click();
            await expect(page.getByRole('alert').filter({ hasText: '两次输入的密码不一致' })).toBeVisible();
            expect(state.mutations).toEqual([]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('API key dashboard shows usage and accessible account actions', async ({ page }, info) => {
            const state = await mockApp(page, 'home', { auth: 'apikey', apiKeyStats: {
                info: { id: 1, name: 'Development', api_key: 'sk-demo-key-for-preview', enabled: true, max_cost: 100, supported_models: models.map(model => model.name).join(',') },
                stats: { ...emptyStats, api_key_id: 1, request_success: 1240, request_failed: 3, input_token: 1200000, output_token: 400000, input_cost: 16, output_cost: 12, wait_time: 420000 },
            } });
            await page.goto('/');
            await expect(page.getByRole('heading', { name: '密钥用量概览' })).toBeVisible();
            await expect(page.getByRole('heading', { name: 'Development' })).toBeVisible();
            await expect(page.getByRole('button', { name: '切换主题', exact: true })).toBeVisible();
            await capture(page, info, 'apikey-usage');
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });
    });
}
