import { expect, test, type Page } from '@playwright/test';
import type { GroupAutoGroupConfig } from '../../src/api/endpoints/group';
import type { ProxyConfigurationReference } from '../../src/api/endpoints/proxy-pool';
import { makeChannel } from './fixtures';
import {
    checkinAccount,
    checkinCredential,
    checkinSite,
    expectIndependent,
    installCheckinAPI,
    longID,
} from './checkin-fixtures';

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        sessionStorage.setItem('octopus:channel-tab', 'site');
        localStorage.setItem(
            'log-ui-storage',
            JSON.stringify({ state: { liveEnabled: false, pageSize: 20 }, version: 0 }),
        );
    });
});

for (const width of [1440, 390]) {
    test(`old navigation recovers and all v2 pages stay independent at ${width}px`, async ({ page }, info) => {
        await page.setViewportSize({ width, height: 1000 });
        const state = await installCheckinAPI(page, {
            nav: 'site',
            app: { channels: [makeChannel(101, 'Converted channel')] },
        });
        await page.goto('/');
        const nav = page.getByRole('navigation').first();
        await expect(nav.getByRole('button', { name: '主页', exact: true })).toHaveAttribute('aria-current', 'page');
        await expect(nav.getByRole('button')).toHaveCount(7);
        for (const name of ['渠道', '分组', '价格', '日志', '设置', '签到']) {
            await nav.getByRole('button', { name, exact: true }).click();
            await expect(nav.getByRole('button', { name, exact: true })).toHaveAttribute('aria-current', 'page');
            if (name === '渠道') {
                await expect(page.getByRole('button', { name: '查看 Converted channel', exact: true })).toBeVisible();
                await expect(page.getByRole('button', { name: /^站点渠道/ })).toHaveCount(0);
            }
            if (name === '设置') {
                await page.getByRole('group', { name: '设置分类' }).getByRole('button', { name: '连接与任务' }).click();
                await expect(page.getByTestId('independent-checkin-tasks')).toBeVisible();
                await expect(page.getByText('站点全量同步', { exact: true })).toHaveCount(0);
                await page.getByTestId('independent-checkin-tasks').scrollIntoViewIfNeeded();
                await page.screenshot({ path: info.outputPath(`v2-tasks-${width}.png`), animations: 'disabled' });
            }
            expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        }
        await expect(page.getByTestId('independent-checkin')).toBeVisible();
        expectIndependent(state);
    });
}

test('ordinary channels retain IDs, keys and edit controls after conversion', async ({ page }) => {
    const channel = makeChannel(101, 'Converted channel');
    const state = await installCheckinAPI(page, {
        nav: 'channel',
        app: {
            channels: [channel],
            mutate: (request) => {
                const body = request.body as Record<string, unknown>;
                expect(body.id).toBe(101);
                if (request.path === '/api/v1/channel/enable') {
                    channel.enabled = body.enabled as boolean;
                    return { data: null };
                }
                expect(request.path).toBe('/api/v1/channel/update');
                expect(body).toEqual({ id: 101, name: 'Renamed channel', custom_header: [] });
                channel.name = body.name as string;
                return { data: channel };
            },
        },
    });
    await page.goto('/');
    await page.getByRole('switch', { name: '启用 Converted channel' }).click();
    await expect(page.getByRole('switch', { name: '启用 Converted channel' })).not.toBeChecked();
    await page.getByRole('button', { name: '查看 Converted channel', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: '编辑', exact: true }).click();
    await dialog.getByLabel('渠道名称', { exact: true }).fill('Renamed channel');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(page.getByRole('button', { name: '查看 Renamed channel', exact: true })).toBeVisible();
    expect(channel.keys[0].id).toBe(101);
    expect(channel.keys[0].channel_key).toBe('demo-key');
    expect(channel.auto_sync).toBe(false);
    expectIndependent(state);
});

test('unconverted managed channels remain read only without subscription jumps', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        nav: 'channel',
        app: {
            channels: [
                makeChannel(102, 'Pending channel', {
                    managed: true,
                    managed_source: { site_id: 8, site_account_id: 9, group_key: 'default' },
                }),
            ],
        },
    });
    await page.goto('/');
    await expect(page.getByRole('switch', { name: '启用 Pending channel' })).toBeDisabled();
    await page.getByRole('button', { name: '查看 Pending channel', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('该渠道尚未完成 v2 数据迁移');
    await expect(dialog.getByRole('button', { name: /编辑|删除|查看来源站点|查看站点渠道/ })).toHaveCount(0);
    expect(state.app.mutations).toEqual([]);
    expectIndependent(state);
});

async function autoGroup(page: Page, managed = false) {
    const state = await installCheckinAPI(page, { nav: 'group' });
    let config: GroupAutoGroupConfig = {
        projected_global_auto_group: 1,
        sources: [
            {
                channel_id: 101,
                channel_name: 'Converted channel',
                enabled: true,
                managed,
                auto_group: 0,
                effective_auto_group: managed ? 1 : 0,
                global_override: managed,
                model_count: 1,
                models: ['gpt-4.1'],
                endpoint_type: 'openai_chat',
                site_name: 'Legacy source must not appear',
            },
        ],
    };
    const writes: unknown[] = [];
    await page.route('**/api/v1/group/auto-group/config', async (route) => {
        if (route.request().method() === 'PUT') {
            const body = route.request().postDataJSON();
            writes.push(body);
            config = {
                ...config,
                sources: config.sources.map((item) => ({ ...item, auto_group: body.items[0].auto_group })),
            };
        }
        await route.fulfill({ json: { code: 200, data: config } });
    });
    await page.goto('/');
    await page.getByRole('button', { name: '自动分组', exact: true }).click();
    await page.getByRole('button', { name: /^openai_chat/ }).click();
    return { state, writes, dialog: page.getByRole('dialog') };
}

test('automatic grouping is organized by protocol and only writes channel settings', async ({ page }) => {
    const { state, writes, dialog } = await autoGroup(page);
    await expect(dialog).not.toContainText('Legacy source must not appear');
    await expect(dialog).not.toContainText('全局默认模式');
    await dialog.getByRole('group', { name: 'Converted channel', exact: true }).getByRole('combobox').click();
    await page.getByRole('option', { name: '精确匹配', exact: true }).click();
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(writes).toEqual([{ items: [{ channel_id: 101, auto_group: 2 }], run_now: true }]);
    expectIndependent(state);
});

test('automatic grouping cannot overwrite unmigrated global overrides', async ({ page }) => {
    const { state, writes, dialog } = await autoGroup(page, true);
    await expect(dialog.getByRole('alert')).toContainText('部分渠道尚未完成迁移');
    await dialog.getByRole('group', { name: 'Converted channel', exact: true }).getByRole('combobox').click();
    await page.getByRole('option', { name: '精确匹配', exact: true }).click();
    await expect(dialog.getByRole('button', { name: '保存', exact: true })).toBeDisabled();
    expect(writes).toEqual([]);
    expectIndependent(state);
});

test('logs filter ordinary channels and retain diagnostics without subscription model actions', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        nav: 'log',
        app: {
            channels: [makeChannel(101, 'Converted channel')],
            logs: [
                {
                    id: 7,
                    time: 1790300000,
                    request_model_name: 'gpt-4.1',
                    actual_model_name: 'gpt-4.1',
                    channel: 101,
                    channel_name: 'Converted channel',
                    input_tokens: 3,
                    output_tokens: 2,
                    ftut: 3,
                    use_time: 30,
                    cost: 0.01,
                    error: 'Upstream timeout',
                    attempts: [
                        {
                            channel_id: 101,
                            channel_name: 'Converted channel',
                            model_name: 'gpt-4.1',
                            status: 'failed',
                            attempt_num: 1,
                            duration: 30,
                            msg: 'Upstream timeout',
                        },
                    ],
                },
            ],
        },
    });
    await page.goto('/');
    await page.getByRole('button', { name: '日志筛选' }).click();
    await expect(page.getByText('全部渠道', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Converted channel', exact: true }).click();
    await page.keyboard.press('Escape');
    await page
        .getByRole('button', { name: /gpt-4.1/ })
        .first()
        .click();
    const dialog = page.getByRole('dialog', { name: /gpt-4.1/ });
    await dialog.getByText('重试详情', { exact: true }).click();
    await expect(dialog).toContainText('Upstream timeout');
    await expect(page.getByRole('button', { name: '禁用模型', exact: true })).toHaveCount(0);
    expectIndependent(state);
});

test('settings confirm all-site batches and poll string IDs without old scheduling APIs', async ({ page }) => {
    const state = await installCheckinAPI(page, { nav: 'setting' });
    await page.goto('/');
    await page.getByRole('group', { name: '设置分类' }).getByRole('button', { name: '连接与任务' }).click();
    const tasks = page.getByTestId('independent-checkin-tasks');
    await tasks.getByRole('button', { name: '全量签到', exact: true }).click();
    let dialog = page.getByRole('dialog', { name: '全量签到', exact: true });
    await expect(dialog).toContainText('仅执行符合条件且已开启自动签到的账号');
    await dialog.getByRole('button', { name: '取消', exact: true }).click();
    expect(state.writes).toEqual([]);
    await tasks.getByRole('button', { name: '全量签到', exact: true }).click();
    dialog = page.getByRole('dialog', { name: '全量签到', exact: true });
    await dialog.getByRole('button', { name: '确认', exact: true }).click();
    await expect(tasks).toContainText(longID);
    await expect(tasks).toContainText('已完成');
    expect(state.writes).toEqual([{ method: 'POST', path: '/batches', body: { scope: 'all' } }]);
    expect(state.reads).toContain(`/api/v1/checkin/batches/${longID}`);
    expectIndependent(state);
});

test('settings keep a failed batch confirmation open without automatically retrying', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        nav: 'setting',
        mutate: (request) =>
            request.path === '/batches' ? { status: 409, errorCode: 'checkin.batch_active' } : undefined,
    });
    await page.goto('/');
    await page.getByRole('group', { name: '设置分类' }).getByRole('button', { name: '连接与任务' }).click();
    await page.getByTestId('independent-checkin-tasks').getByRole('button', { name: '全量签到', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '全量签到', exact: true });
    await dialog.getByRole('button', { name: '确认', exact: true }).click();
    await expect(dialog.getByRole('alert')).toContainText('已有不同站点范围的活动批次');
    await expect(dialog.getByRole('button', { name: '确认', exact: true })).toBeEnabled();
    expect(state.writes).toHaveLength(1);
    expectIndependent(state);
});

const references: ProxyConfigurationReference[] = [
    { type: 'checkin_site', checkin_site_id: 2, checkin_site_name: 'Archived site', checkin_site_archived: true },
    {
        type: 'checkin_account',
        checkin_site_id: 1,
        checkin_site_name: 'Independent site',
        checkin_account_id: 1,
        checkin_account_name: 'Independent account',
    },
    {
        type: 'checkin_balance',
        checkin_site_id: 1,
        checkin_site_name: 'Independent site',
        checkin_account_id: 1,
        checkin_account_name: 'Independent account',
    },
    { type: 'checkin_credential', checkin_credential_id: 1, checkin_credential_name: 'Independent credential' },
    { type: 'checkin_credential', checkin_credential_id: 2, checkin_credential_name: 'Second credential' },
    { type: 'site', site_id: 1, site_name: 'Legacy reference' },
];

async function proxyFixture(page: Page, locale: 'zh_hans' | 'zh_hant' | 'en' = 'zh_hans') {
    const state = await installCheckinAPI(page, {
        locale,
        sites: [checkinSite(), checkinSite({ id: 2, name: 'Archived site', archived: true, enabled: false })],
        accounts: [checkinAccount()],
        credentials: [checkinCredential(), checkinCredential({ id: 2, name: 'Second credential' })],
    });
    await page.route('**/api/v1/proxy-pool/list', (route) =>
        route.fulfill({
            json: {
                code: 200,
                data: [
                    {
                        id: 1,
                        name: 'Shared proxy',
                        enabled: true,
                        url: 'socks5h://127.0.0.1:1080',
                        remark: '',
                        reference_count: references.length,
                    },
                ],
            },
        }),
    );
    await page.route('**/api/v1/proxy-pool/references/1', (route) =>
        route.fulfill({ json: { code: 200, data: references } }),
    );
    await page.goto('/');
    return state;
}

test('proxy references preserve distinct identities and open site, account, balance and credential editors', async ({
    page,
}) => {
    const state = await proxyFixture(page);
    async function openReferences() {
        await page.getByRole('button', { name: '代理池', exact: true }).click();
        await expect(page.getByRole('button', { name: '仍被引用，不能删除', exact: true })).toBeDisabled();
        await page.getByRole('button', { name: '引用 6', exact: true }).click();
        await expect(page.getByRole('dialog', { name: '查看引用' }).getByRole('group')).toHaveCount(6);
        await expect(
            page.getByRole('group', { name: '站点: Legacy reference', exact: true }).getByRole('button'),
        ).toBeDisabled();
    }
    for (const [label, title, field, value] of [
        ['签到站点: Archived site', '编辑签到站点', '站点名称', 'Archived site'],
        ['签到账号: Independent account', '编辑账号', '账号名称', 'Independent account'],
        ['余额查询: Independent account', '编辑账号', '账号名称', 'Independent account'],
        ['认证凭据: Second credential', '编辑凭据', '凭据名称', 'Second credential'],
    ]) {
        await openReferences();
        await page.getByRole('group', { name: label, exact: true }).getByRole('button').click();
        const dialog = page.getByRole('dialog', { name: title, exact: true });
        await expect(dialog.getByLabel(field, { exact: true })).toHaveValue(value);
        if (label.startsWith('余额')) await expect(dialog.getByLabel('余额查询', { exact: true })).toBeFocused();
        await dialog.getByRole('button', { name: '取消', exact: true }).click();
    }
    expect(state.writes).toEqual([]);
    expectIndependent(state);
});

test('missing proxy targets show an error without opening a different entity', async ({ page }) => {
    const state = await proxyFixture(page);
    await page.route('**/api/v1/proxy-pool/references/1', (route) =>
        route.fulfill({
            json: {
                code: 200,
                data: [
                    {
                        type: 'checkin_credential',
                        checkin_credential_id: 99,
                        checkin_credential_name: 'Removed credential',
                    },
                ],
            },
        }),
    );
    await page.getByRole('button', { name: '代理池', exact: true }).click();
    await page.getByRole('button', { name: '引用 6', exact: true }).click();
    await page.getByRole('group', { name: '认证凭据: Removed credential', exact: true }).getByRole('button').click();
    await expect(page.getByText('该引用已不存在，请刷新代理引用列表。', { exact: true })).toBeVisible();
    await expect(page.getByRole('dialog', { name: '编辑凭据', exact: true })).toHaveCount(0);
    expect(state.writes).toEqual([]);
    expectIndependent(state);
});

for (const [locale, pool, refs, credential] of [
    ['en', 'Proxy pool', 'Refs 6', 'Authentication credential: Second credential'],
    ['zh_hant', '代理池', '引用 6', '認證憑據: Second credential'],
] as const) {
    test(`new proxy reference labels render in ${locale}`, async ({ page }) => {
        const state = await proxyFixture(page, locale);
        await page.getByRole('button', { name: pool, exact: true }).click();
        await page.getByRole('button', { name: refs, exact: true }).click();
        await expect(page.getByRole('group', { name: credential, exact: true })).toBeVisible();
        expectIndependent(state);
    });
}
