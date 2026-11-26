import { test, expect, type Locator } from '@playwright/test';
import {
    checkinAccount,
    checkinCredential,
    checkinLog,
    checkinSite,
    expectIndependent,
    installCheckinAPI,
    longID,
    nextID,
    notification,
} from './checkin-fixtures';

async function waitUntilOpaque(locator: Locator) {
    await expect
        .poll(() =>
            locator.evaluate((element) => {
                for (let node: Element | null = element; node; node = node.parentElement) {
                    if (getComputedStyle(node).opacity !== '1') return false;
                }
                return true;
            }),
        )
        .toBe(true);
}

test('creates an independent site and an account with an inline credential', async ({ page }) => {
    const state = await installCheckinAPI(page, { sites: [], accounts: [], credentials: [] });
    await page.goto('/');
    await page.getByRole('button', { name: '新增签到站点', exact: true }).click();
    const site = page.getByRole('dialog', { name: '新增签到站点' });
    await site.getByLabel('站点名称', { exact: true }).fill('Cookie site');
    await site.getByLabel('站点平台').selectOption('api');
    await site.getByLabel('站点地址', { exact: true }).fill('https://cookie.example');
    await site.getByLabel('使用自定义 HTTP 签到').check();
    await site.getByLabel('提交路径', { exact: true }).fill('/checkin');
    await site.getByRole('button', { name: '保存', exact: true }).click();
    const card = page.getByRole('article', { name: 'Cookie site', exact: true });
    await expect(card).toBeVisible();
    await card.getByRole('button', { name: '新增账号' }).click();
    const account = page.getByRole('dialog', { name: '新增账号' });
    await account.getByLabel('账号名称').fill('Cookie user');
    await account.getByLabel('凭据名称').fill('Cookie session');
    await account.getByLabel('Cookie', { exact: true }).fill('session=e2e-secret');
    await expect(account.getByLabel('自动签到', { exact: true })).not.toBeChecked();
    await account.getByRole('button', { name: '保存', exact: true }).click();
    await expect(card.getByRole('heading', { name: /Cookie user/ })).toBeVisible();
    const body = state.writes.find((row) => row.path === '/accounts')!.body!;
    expect(body.auto_checkin).toBe(false);
    expect(body.balance_config).toBeNull();
    expect(body).not.toHaveProperty('credential_id');
    expect(body.credential).toMatchObject({
        cookie: 'session=e2e-secret',
        type: 'cookie',
        auth_base_url: 'https://cookie.example',
    });
    await page.getByRole('button', { name: '凭据', exact: true }).click();
    await expect(page.getByRole('article', { name: 'Cookie session' })).toBeVisible();
    await expect(page.getByTestId('independent-checkin')).not.toContainText('e2e-secret');
    expectIndependent(state);
});

for (const platform of ['new-api', 'agentrouter'] as const) {
    test(`creates and selects a password credential for ${platform}`, async ({ page }) => {
        const state = await installCheckinAPI(page, {
            sites: [checkinSite({ platform })],
            accounts: [],
            credentials: [],
        });
        await page.goto('/');
        await page.getByRole('button', { name: '凭据', exact: true }).click();
        await page.getByRole('button', { name: '新增凭据', exact: true }).click();
        const credential = page.getByRole('dialog', { name: '新增凭据' });
        await expect(credential.getByLabel('凭据类型')).toHaveValue('username_password');
        await credential.getByLabel('凭据名称').fill('Password login');
        await credential.getByLabel('认证平台').selectOption(platform);
        await credential.getByLabel('认证地址').fill('https://checkin.example');
        await credential.getByLabel('用户名', { exact: true }).fill('demo');
        await credential.getByLabel('密码', { exact: true }).fill('e2e-secret');
        await credential.getByRole('button', { name: '保存', exact: true }).click();
        await expect(credential).not.toBeVisible();
        expect(state.writes.at(-1)).toMatchObject({
            method: 'POST',
            path: '/credentials',
            body: { type: 'username_password', platform, username: 'demo', password: 'e2e-secret' },
        });

        await page.getByRole('button', { name: '站点与账号', exact: true }).click();
        await page.getByRole('button', { name: '新增账号', exact: true }).click();
        const account = page.getByRole('dialog', { name: '新增账号' });
        await account.getByLabel('账号名称').fill('Password account');
        await account.getByLabel('签到凭据', { exact: true }).selectOption('1');
        await account.getByLabel('余额查询', { exact: true }).selectOption('custom');
        await account.getByLabel('余额凭据').selectOption('1');
        await account.getByRole('button', { name: '保存', exact: true }).click();
        await expect(account).not.toBeVisible();
        expect(state.writes.at(-1)).toMatchObject({
            method: 'POST',
            path: '/accounts',
            body: { credential_id: 1, balance_config: { mode: 'custom', platform, credential_id: 1 } },
        });
        expect(state.writes.at(-1)!.body).not.toHaveProperty('credential');
        expectIndependent(state);
    });
}

test('creates an account with an inline password credential using the API type', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        sites: [checkinSite({ platform: 'agentrouter' })],
        accounts: [],
        credentials: [],
    });
    await page.goto('/');
    await page.getByRole('button', { name: '新增账号', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '新增账号' });
    await expect(dialog.getByLabel('凭据类型')).toHaveValue('username_password');
    await dialog.getByLabel('账号名称').fill('Password account');
    await dialog.getByLabel('凭据名称').fill('Inline login');
    await dialog.getByLabel('用户名', { exact: true }).fill('demo');
    await dialog.getByLabel('密码', { exact: true }).fill('e2e-secret');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.writes.map((row) => row.path)).toEqual(['/accounts']);
    expect(state.writes[0].body).toMatchObject({
        auth_mode: 'platform',
        credential: {
            type: 'username_password',
            platform: 'agentrouter',
            auth_base_url: 'https://checkin.example',
            username: 'demo',
            password: 'e2e-secret',
        },
    });
    expect(state.writes[0].body).not.toHaveProperty('credential_id');
    expectIndependent(state);
});

test('credential editing preserves omitted secrets and detects stale versions', async ({ page }) => {
    let conflicts = true;
    const state = await installCheckinAPI(page, {
        mutate: (request) =>
            request.method === 'PATCH' && request.path === '/credentials/1' && conflicts
                ? { status: 409, errorCode: 'checkin.credential_conflict' }
                : undefined,
    });
    await page.goto('/');
    await page.getByRole('button', { name: '凭据', exact: true }).click();
    await page.getByRole('article', { name: 'Independent credential' }).getByRole('button', { name: '编辑' }).click();
    let dialog = page.getByRole('dialog', { name: '编辑凭据' });
    await expect(dialog.getByLabel('凭据类型')).toHaveValue('username_password');
    await expect(dialog.getByLabel('用户名', { exact: true })).toHaveValue('demo');
    await expect(dialog.getByLabel('密码', { exact: true })).toHaveValue('');
    await dialog.getByLabel('凭据名称').fill('Renamed');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog.getByRole('alert')).toContainText('凭据已被更新');
    const patch = state.writes.at(-1)!.body!;
    expect(patch.type).toBe('username_password');
    expect(patch.expected_version).toBe(7);
    for (const key of ['password', 'access_token', 'refresh_token', 'auth_headers', 'id', 'credential_version'])
        expect(patch).not.toHaveProperty(key);
    await dialog.getByRole('button', { name: '取消', exact: true }).click();
    conflicts = false;
    state.credentials[0].credential_version = 8;
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await expect.poll(() => state.reads.filter((path) => path.endsWith('/credentials')).length).toBeGreaterThan(1);
    await page.getByRole('article', { name: 'Independent credential' }).getByRole('button', { name: '编辑' }).click();
    dialog = page.getByRole('dialog', { name: '编辑凭据' });
    await dialog.getByLabel('凭据名称').fill('Updated');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.writes.at(-1)!.body!.expected_version).toBe(8);
    expectIndependent(state);
});

test('explicit secret and header clearing stays distinct from omission', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        credentials: [
            checkinCredential({
                type: 'access_token',
                platform: 'sub2api',
                has_access_token: true,
                has_refresh_token: true,
            }),
        ],
    });
    await page.goto('/');
    await page.getByRole('button', { name: '凭据', exact: true }).click();
    await page.getByRole('article', { name: 'Independent credential' }).getByRole('button', { name: '编辑' }).click();
    const dialog = page.getByRole('dialog', { name: '编辑凭据' });
    await dialog.getByLabel('清空已保存的Refresh Token').check();
    await dialog.getByText('认证请求网络', { exact: true }).click();
    await dialog.getByLabel('替换已保存的认证请求头').check();
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.writes.at(-1)!.body).toMatchObject({ expected_version: 7, refresh_token: '', auth_headers: [] });
    expect(state.writes.at(-1)!.body).not.toHaveProperty('access_token');
    expectIndependent(state);
});

test('configures custom balance with an independent credential and preserves false values', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        credentials: [
            checkinCredential(),
            checkinCredential({ id: 2, name: 'Balance key', type: 'api_key', platform: 'sub2api' }),
        ],
    });
    await page.goto('/');
    const account = page.getByRole('region', { name: 'Independent account', exact: true });
    await account.getByRole('button', { name: '编辑' }).click();
    const dialog = page.getByRole('dialog', { name: '编辑账号' });
    await dialog.getByLabel('自动签到', { exact: true }).uncheck();
    await dialog.getByLabel('余额查询', { exact: true }).selectOption('custom');
    await dialog.getByLabel('余额平台').selectOption('sub2api');
    await dialog.getByLabel('余额地址').fill('https://balance.example');
    await dialog.getByLabel('余额凭据').selectOption('2');
    await dialog.getByRole('button', { name: '保存', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.writes.at(-1)!.body).toMatchObject({
        auto_checkin: false,
        balance_config: { mode: 'custom', credential_id: 2, platform: 'sub2api', base_url: 'https://balance.example' },
    });
    expect(state.writes.at(-1)!.body).not.toHaveProperty('site_id');
    await account.getByRole('button', { name: '刷新余额' }).click();
    await expect(page.getByRole('status').filter({ hasText: '上游今日入账' })).toContainText('余额: 0');
    await expect(page.getByRole('status').filter({ hasText: '上游今日入账' })).toContainText('上游今日入账: —');
    expectIndependent(state);
});

test('confirms a submitted workflow without resubmitting and shows zero reward', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        sites: [checkinSite({ platform: 'agentrouter' })],
        accounts: [
            checkinAccount({
                last_checkin_status: 'pending',
                checkin_pending: {
                    log_id: longID,
                    batch_job_id: nextID,
                    source: 'batch',
                    date: '2026-11-20',
                    started_at: '2026-11-20T00:00:00Z',
                    deadline: '2026-11-21T00:00:00Z',
                    submitted: true,
                    finished: false,
                    status: 'pending',
                    reason: 'confirmation_pending',
                },
            }),
        ],
    });
    await page.goto('/');
    const account = page.getByRole('region', { name: 'Independent account', exact: true });
    await expect(account.getByRole('button', { name: '删除' })).toBeDisabled();
    await expect(account.getByRole('button', { name: '签到', exact: true })).toHaveCount(0);
    await expect(account).toContainText(longID);
    await account.getByRole('button', { name: '确认结果' }).click();
    await expect(page.getByText('成功 · 奖励：0', { exact: true })).toBeVisible();
    expect(state.writes.map((row) => row.path)).toEqual(['/accounts/1/confirm']);
    expectIndependent(state);
});

for (const reason of ['verification_required', 'confirmation_expired'] as const) {
    test(`confirms a finished ${reason} workflow without resubmitting`, async ({ page }) => {
        const http = reason === 'confirmation_expired';
        const platform = http ? 'api' : 'agentrouter';
        const state = await installCheckinAPI(page, {
            sites: [
                checkinSite({
                    platform,
                    checkin_http_enabled: http,
                    checkin_http_path: http ? '/checkin' : '',
                    checkin_status: http
                        ? {
                              method: 'GET',
                              path: '/status',
                              body: '',
                              headers: [],
                              skip_precheck: false,
                              checked_path: 'checked',
                              checked_value: true,
                              unchecked_value: false,
                              date_path: 'date',
                              code: '',
                              initial_delay_ms: 1000,
                              poll_interval_ms: 2000,
                              max_attempts: 3,
                              timeout_seconds: 10,
                              confirm_timeout_minutes: 30,
                              reward_source: 'status',
                          }
                        : null,
                }),
            ],
            credentials: [
                checkinCredential({
                    platform,
                    type: http ? 'cookie' : 'username_password',
                    has_cookie: http,
                    has_password: !http,
                }),
            ],
            accounts: [
                checkinAccount({
                    auth_mode: http ? 'cookie' : 'platform',
                    balance_config: null,
                    last_checkin_status: 'failed',
                    checkin_pending: {
                        log_id: longID,
                        source: 'manual',
                        date: '2026-11-19',
                        started_at: '2026-11-19T00:00:00Z',
                        deadline: '2026-11-19T00:30:00Z',
                        submitted: true,
                        finished: true,
                        status: 'failed',
                        reason,
                    },
                }),
            ],
        });
        await page.goto('/');
        const account = page.getByRole('region', { name: 'Independent account', exact: true });
        await expect(account.getByRole('button', { name: '确认结果', exact: true })).toBeEnabled();
        // A finished workflow must also leave the next day's check-in available.
        await expect(account.getByRole('button', { name: '签到', exact: true })).toBeEnabled();
        expect(state.writes).toHaveLength(0);
        await account.getByRole('button', { name: '确认结果', exact: true }).click();
        await expect(page.getByText('成功 · 奖励：0', { exact: true })).toBeVisible();
        await expect(account.getByRole('button', { name: '确认结果', exact: true })).toHaveCount(0);
        await expect(account.getByRole('button', { name: '签到', exact: true })).toBeEnabled();
        expect(state.writes.map((row) => row.path)).toEqual(['/accounts/1/confirm']);
        expect(state.accounts[0].checkin_pending).toMatchObject({
            log_id: longID,
            date: '2026-11-19',
            submitted: true,
            finished: true,
            status: 'success',
        });
        expectIndependent(state);
    });
}

test('creates only the selected batch, polls string IDs and refuses empty selections', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        sites: [checkinSite(), checkinSite({ id: 2, name: 'Second site' })],
    });
    await page.goto('/');
    await expect(page.getByRole('button', { name: '所选站点签到' })).toBeDisabled();
    await page.getByLabel('选择Second site', { exact: true }).check();
    await page.getByRole('button', { name: '所选站点签到' }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
    await expect(page.getByRole('article', { name: `批次 ${longID}` })).toContainText('已完成');
    expect(state.writes.find((row) => row.path === '/batches')!.body).toEqual({ scope: 'selected', site_ids: [2] });
    expect(state.reads.some((path) => path.endsWith(`/batches/${longID}`))).toBe(true);
    await page.getByRole('button', { name: '站点与账号' }).click();
    await page.getByRole('textbox', { name: '搜索站点、账号或标签' }).fill('Independent');
    await expect(page.getByRole('button', { name: '所选站点签到' })).toBeDisabled();
    expectIndependent(state);
});

test('history filters and pagination retain long IDs and unknown versus zero reward', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        logs: [checkinLog({ batch_job_id: longID }), checkinLog({ id: longID, reward: '' })],
    });
    await page.goto('/');
    await page.getByRole('button', { name: '历史与收益' }).click();
    await expect(page.getByRole('row').filter({ hasText: nextID })).toContainText('0');
    await page.getByRole('button', { name: '加载更多' }).click();
    await expect(
        page.getByRole('row').filter({ has: page.getByRole('cell', { name: longID, exact: true }) }),
    ).toContainText('—');
    expect(state.reads.some((path) => path.includes(`before_id=${nextID}`))).toBe(true);
    await page.getByLabel('批次 ID', { exact: true }).fill(longID);
    await page.getByLabel('统计时区').fill('Asia/Shanghai');
    await page.getByLabel('来源', { exact: true }).selectOption('batch');
    await page.getByRole('button', { name: '应用筛选' }).click();
    await expect
        .poll(() => state.filters.some((filter) => filter.batch_id === longID && filter.source === 'batch'))
        .toBe(true);
    expectIndependent(state);
});

test('notification retries require explicit confirmation and preserve delivery IDs', async ({ page }) => {
    const state = await installCheckinAPI(page, { notifications: [notification()] });
    await page.goto('/');
    await page.getByRole('button', { name: '通知', exact: true }).click();
    await page.getByRole('button', { name: '重试通知' }).click();
    const dialog = page.getByRole('dialog', { name: '重试通知' });
    await expect(dialog).toContainText('重试可能产生重复通知');
    expect(state.writes).toHaveLength(0);
    await dialog.getByRole('button', { name: '取消', exact: true }).click();
    expect(state.writes).toHaveLength(0);
    await page.getByRole('button', { name: '重试通知' }).click();
    await dialog.getByRole('button', { name: '重试通知' }).click();
    await expect(page.getByRole('article', { name: `通知 ${longID}` })).toContainText('已送达');
    expect(state.writes.map((row) => row.path)).toEqual([`/notifications/${longID}/retry`]);
    await expect(page.getByRole('button', { name: '重试通知' })).toHaveCount(0);
    expectIndependent(state);
});

test('archives, restores and deletes sites without changing their enabled state', async ({ page }) => {
    const state = await installCheckinAPI(page, { accounts: [], sites: [checkinSite({ enabled: false })] });
    await page.goto('/');
    await page.getByRole('button', { name: '归档', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
    await page.getByLabel('已归档站点', { exact: true }).check();
    await page.getByRole('button', { name: '恢复', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
    await page.getByLabel('已归档站点', { exact: true }).uncheck();
    await expect(page.getByRole('article', { name: 'Independent site' })).toContainText('停用');
    await page.getByRole('button', { name: '删除', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认', exact: true }).click();
    await expect(page.getByText('暂无签到站点', { exact: true })).toBeVisible();
    expect(state.writes.map((row) => row.path)).toEqual(['/sites/1/archive', '/sites/1/restore', '/sites/1']);
    expectIndependent(state);
});

test('local rule previews only send sample data and English errors are translated', async ({ page }) => {
    const state = await installCheckinAPI(page, {
        locale: 'en',
        mutate: (request) =>
            request.path === '/accounts/1/execute'
                ? { status: 409, errorCode: 'checkin.verification_required' }
                : undefined,
    });
    await page.goto('/');
    await page
        .getByRole('region', { name: 'Independent account', exact: true })
        .getByRole('button', { name: 'Check in', exact: true })
        .click();
    await expect(page.getByTestId('independent-checkin').getByRole('alert')).toContainText(
        'Complete verification at the site',
    );
    await page
        .getByRole('article', { name: 'Independent site' })
        .getByRole('button', { name: 'Edit', exact: true })
        .first()
        .click();
    const dialog = page.getByRole('dialog', { name: 'Edit check-in site' });
    await dialog.getByLabel('Reward extraction (JavaScript)', { exact: true }).fill('return response.reward;');
    await dialog.getByText('Test rules with samples', { exact: true }).click();
    await dialog.getByLabel('Response sample (JSON)', { exact: true }).fill('{"reward":0}');
    await dialog.getByRole('button', { name: 'Test reward rule', exact: true }).click();
    await expect(dialog.getByRole('status')).toHaveText('Reward: 0');
    expect(state.writes.at(-1)).toMatchObject({
        path: '/reward/test',
        body: { code: 'return response.reward;', response: { reward: 0 } },
    });
    await dialog.getByLabel('Query status before and after submission', { exact: true }).check();
    await dialog.getByLabel('Status path', { exact: true }).fill('/status');
    await dialog.getByLabel('Checked value path', { exact: true }).fill('checked');
    await dialog.getByLabel('Response sample (JSON)', { exact: true }).fill('{"checked":true}');
    await dialog.getByRole('button', { name: 'Test status rule', exact: true }).click();
    await expect(dialog.getByRole('status')).toHaveText('Checked in');
    expect(state.writes.at(-1)).toMatchObject({
        path: '/status/test',
        body: { response: { checked: true }, config: { path: '/status', checked_path: 'checked' } },
    });
    expectIndependent(state);
});

test('a failed execute is not retried automatically and can be retried explicitly', async ({ page }) => {
    let failure = true;
    const state = await installCheckinAPI(page, {
        mutate: (request) =>
            request.path === '/accounts/1/execute' && failure
                ? { status: 503, errorCode: 'checkin.session_backoff' }
                : undefined,
    });
    await page.goto('/');
    const account = page.getByRole('region', { name: 'Independent account', exact: true });
    await account.getByRole('button', { name: '签到', exact: true }).click();
    await expect(page.getByTestId('independent-checkin').getByRole('alert')).toContainText('会话处于冷却期');
    await expect(account.getByRole('button', { name: '签到', exact: true })).toBeEnabled();
    expect(state.writes.filter((row) => row.path === '/accounts/1/execute')).toHaveLength(1);
    failure = false;
    await account.getByRole('button', { name: '签到', exact: true }).click();
    await expect(page.getByText('成功 · 奖励：0', { exact: true })).toBeVisible();
    expect(state.writes.filter((row) => row.path === '/accounts/1/execute')).toHaveLength(2);
    expectIndependent(state);
});

test('new API read errors stay independent and recover with translated Traditional Chinese errors', async ({
    page,
}) => {
    const state = await installCheckinAPI(page, { locale: 'zh_hant' });
    let unavailable = true;
    await page.route('**/api/v1/checkin/sites*', async (route) => {
        if (unavailable)
            await route.fulfill({
                status: 404,
                contentType: 'application/json',
                body: JSON.stringify({ code: 404, error_code: 'checkin.not_found', message: 'not found' }),
            });
        else await route.fallback();
    });
    await page.goto('/');
    const alert = page.getByTestId('independent-checkin').getByRole('alert');
    await expect(alert).toContainText('資源或引用設定不存在，請重新整理後重試。');
    expectIndependent(state);
    unavailable = false;
    await alert.getByRole('button', { name: '重新整理' }).click();
    await expect(page.getByRole('article', { name: 'Independent site', exact: true })).toBeVisible();
    await expect(alert).toHaveCount(0);
    expectIndependent(state);
});

for (const width of [1440, 390]) {
    test(`workspace and form remain within the viewport at ${width}px`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: 900 });
        const state = await installCheckinAPI(page);
        await page.goto('/');
        const workspace = page.getByTestId('independent-checkin');
        await expect(workspace).toBeVisible();
        await waitUntilOpaque(workspace);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await page.screenshot({ path: testInfo.outputPath(`checkin-${width}.png`), animations: 'disabled' });
        await page.getByRole('button', { name: '新增签到站点', exact: true }).click();
        const dialog = page.getByRole('dialog', { name: '新增签到站点' });
        await expect(dialog).toBeVisible();
        await waitUntilOpaque(dialog);
        expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await page.screenshot({ path: testInfo.outputPath(`checkin-${width}-form.png`), animations: 'disabled' });
        expectIndependent(state);
    });
}
