import { expect, test } from '@playwright/test';
import { makeSite, makeCheckinSite, mockApp } from './fixtures';

for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    test.describe(`${viewport.width}px site forms`, () => {
        test.use({ viewport });

        test('checkin site saves its own URL, linked subscription, schedule and HTTP request', async ({ page }, testInfo) => {
            const state = await mockApp(page, 'checkin', {
                sites: [makeCheckinSite(), makeSite(2, 'Linked subscription')],
                mutate: request => {
                    expect(request.method).toBe('POST');
                    expect(request.path).toBe('/api/v1/site/update');
                    state.sites[0] = { ...state.sites[0], ...request.body as object };
                    return { data: state.sites[0] };
                },
            });
            await page.goto('/');
            await page.getByRole('button', { name: '更多站点操作' }).click();
            await page.getByRole('button', { name: '编辑站点', exact: true }).click();
            const dialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: '编辑签到站点' }) });
            await dialog.getByLabel('站点名称', { exact: true }).fill('  Updated site  ');
            await dialog.getByLabel('签到站点地址', { exact: true }).fill('https://checkin.example/daily');
            await dialog.getByRole('combobox', { name: '关联订阅站', exact: true }).click();
            await page.getByRole('option', { name: 'Linked subscription', exact: true }).click();
            await dialog.getByLabel('自动签到时区', { exact: true }).fill('UTC');
            await dialog.getByLabel('开始时间', { exact: true }).fill('09:00');
            await dialog.getByLabel('结束时间', { exact: true }).fill('08:00');
            await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
            await expect(page.getByText('签到结束时间不能早于开始时间', { exact: true })).toBeVisible();
            expect(state.mutations).toEqual([]);

            await dialog.getByLabel('结束时间', { exact: true }).fill('18:00');
            await dialog.getByRole('combobox', { name: '站点签到能力', exact: true }).click();
            await page.getByRole('option', { name: '启用', exact: true }).click();
            await dialog.getByRole('switch', { name: '启用自定义 HTTP 签到', exact: true }).check();
            await dialog.getByLabel('请求路径', { exact: true }).fill('/api/checkin/spin?day=today');
            await dialog.getByRole('button', { name: '添加', exact: true }).click();
            await dialog.getByLabel('签到 Header 1 名称', { exact: true }).fill('X-Checkin-Mode');
            await dialog.getByLabel('签到 Header 1 值', { exact: true }).fill('daily');
            await dialog.getByLabel('JSON 请求体', { exact: true }).fill('{"source":"{{username}}"}');
            await dialog.getByLabel('奖励提取代码（可选 JavaScript）', { exact: true }).fill('return response.data?.amount ?? null;');
            await expect(dialog.getByRole('button', { name: '高级设置', exact: true })).toHaveCount(0);
            await page.screenshot({ path: testInfo.outputPath('site-edit.png') });
            const bounds = await dialog.boundingBox();
            expect(bounds!.width).toBeLessThanOrEqual(viewport.width);
            await dialog.getByRole('button', { name: '保存修改', exact: true }).click();
            await expect(dialog).not.toBeVisible();
            expect(state.mutations).toEqual([{
                method: 'POST', path: '/api/v1/site/update', body: {
                    id: 1, name: 'Updated site', kind: 'checkin', linked_site_id: 2,
                    platform: 'new-api', base_url: 'https://checkin.example/daily',
                    enabled: true, proxy_mode: 'direct', proxy_config_id: null,
                    external_checkin_url: null, checkin_mode: 'enabled', checkin_http_enabled: true, checkin_http_method: 'POST',
                    checkin_http_path: '/api/checkin/spin?day=today', checkin_http_body: '{"source":"{{username}}"}',
                    checkin_http_headers: [{ header_key: 'X-Checkin-Mode', header_value: 'daily' }],
                    checkin_reward_extractor: 'return response.data?.amount ?? null;',
                    checkin_status: null,
                    checkin_timezone: 'UTC', checkin_window_start: '09:00',
                    checkin_window_end: '18:00', is_pinned: false, sort_order: 0, global_weight: 1,
                    custom_header: [],
                    route_base_urls: [], tags: [],
                },
            }]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('an archived name conflict explains recovery and preserves the site draft for retry', async ({ page }, testInfo) => {
            const state = await mockApp(page, 'site', {
                sites: [],
                mutate: request => {
                    expect(request.method).toBe('POST');
                    expect(request.path).toBe('/api/v1/site/create');
                    if ((request.body as { name: string }).name === '方舟') {
                        return {
                            status: 409,
                            errorCode: 'site.archived_name_exists',
                            params: { name: '方舟', site_id: 9 },
                            message: 'An archived site already exists.',
                        };
                    }
                    state.sites = [{ ...makeSite(10), ...request.body as object }];
                    return { data: state.sites[0] };
                },
            });
            await page.goto('/');
            await page.getByRole('button', { name: '新增第一个站点', exact: true }).click();
            const dialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: '新增站点', exact: true }) });
            await dialog.getByLabel('站点名称', { exact: true }).fill('  方舟  ');
            await dialog.getByLabel('站点地址', { exact: true }).fill('https://new.example');
            await dialog.getByRole('combobox').first().click();
            await page.getByRole('option', { name: 'New API', exact: true }).click();
            await dialog.getByRole('button', { name: '创建站点', exact: true }).click();

            await expect(page.getByText('归档站点中已存在「方舟」。请打开右上角「视图选项 → 全局操作 → 归档站点」恢复，或使用其他名称。', { exact: true })).toBeVisible();
            await expect(dialog).toBeVisible();
            await expect(dialog.getByLabel('站点名称', { exact: true })).toHaveValue('  方舟  ');
            await expect(dialog.getByLabel('站点地址', { exact: true })).toHaveValue('https://new.example');
            expect(state.sites).toEqual([]);
            await page.screenshot({ path: testInfo.outputPath('archived-name-conflict.png') });

            await dialog.getByLabel('站点名称', { exact: true }).fill('方舟新站点');
            await dialog.getByRole('button', { name: '创建站点', exact: true }).click();
            await expect(dialog).not.toBeVisible();
            expect(state.mutations).toHaveLength(2);
            expect(state.sites[0].name).toBe('方舟新站点');
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });

        test('cookie credentials and scheduling fields produce one consistent payload', async ({ page }, testInfo) => {
            let finishSave!: () => void;
            const pendingSave = new Promise<void>(resolve => { finishSave = resolve; });
            const state = await mockApp(page, 'checkin', {
                sites: [{ ...makeCheckinSite(), checkin_http_enabled: true, checkin_http_path: '/daily' }],
                mutate: async request => {
                    expect(request.method).toBe('POST');
                    expect(request.path).toBe('/api/v1/site/account/create');
                    await pendingSave;
                    return { data: { id: 11, ...request.body as object } };
                },
            });
            await page.goto('/');
            await page.getByRole('button', { name: '新增账号', exact: true }).click();
            const dialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: '新增站点账号' }) });
            await dialog.getByLabel('账号名称', { exact: true }).fill('  Primary account  ');
            await dialog.getByLabel('签到站 Cookie', { exact: true }).fill('  session=checkin-cookie  ');
            await expect(dialog.getByPlaceholder('请输入 Access Token', { exact: true })).not.toBeVisible();
            await expect(dialog.getByRole('switch', { name: '自动同步', exact: true })).toHaveCount(0);
            await dialog.getByRole('switch', { name: '随机签到', exact: true }).check();
            await dialog.getByRole('spinbutton', { name: /签到间隔/ }).fill('48');
            await dialog.getByRole('spinbutton', { name: /随机延迟窗口/ }).fill('30');
            await page.screenshot({ path: testInfo.outputPath('account-edit.png') });
            const bounds = await dialog.boundingBox();
            expect(bounds!.width).toBeLessThanOrEqual(viewport.width);
            await dialog.getByRole('button', { name: '创建账号', exact: true }).click();
            try {
                await expect(dialog.getByRole('button', { name: '保存中...' })).toBeDisabled();
                await expect.poll(() => state.mutations.length).toBe(1);
            } finally {
                finishSave();
            }
            await expect(dialog).not.toBeVisible();
            expect(state.mutations).toEqual([{
                method: 'POST', path: '/api/v1/site/account/create', body: {
                    site_id: 1, name: 'Primary account', credential_type: 'cookie',
                    username: '', password: '', access_token: '', api_key: '', cookie: 'session=checkin-cookie', linked_account_id: null,
                    refresh_token: '', token_expires_at: 0, platform_user_id: null,
                    proxy_mode: 'inherit', proxy_config_id: null, enabled: true,
                    auto_sync: false, auto_checkin: true, random_checkin: true,
                    checkin_interval_hours: 48, checkin_random_window_minutes: 30,
                },
            }]);
            expect(state.unexpectedRequests).toEqual([]);
            expect(state.pageErrors).toEqual([]);
        });
    });
}
