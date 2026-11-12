import { expect, test } from '@playwright/test';
import { SiteCredentialType, type SiteAccount } from '../../src/api/endpoints/site-types';
import type { SiteCheckinStats } from '../../src/api/endpoints/site-checkin';
import { makeCheckinSite, mockApp } from './fixtures';

const account: SiteAccount = {
    id: 11, site_id: 1, name: 'Primary account', credential_type: SiteCredentialType.AccessToken,
    username: '', password: '', access_token: '', api_key: '', refresh_token: '', token_expires_at: 0,
    proxy_mode: 'inherit', enabled: true, auto_sync: false, auto_checkin: true, random_checkin: false,
    checkin_random_window_minutes: 0, checkin_failure_count: 0,
    last_sync_status: 'idle', last_checkin_status: 'idle', last_sync_message: '', last_checkin_message: '',
    balance: 4, balance_used: 1, today_income: 0, tokens: [], user_groups: [], models: [], channel_bindings: [],
};
const stats: SiteCheckinStats = {
    today_reward: 1.25, recent_7_days_reward: 7.5, recent_30_days_reward: 25.5, total_reward: 80.25,
    total_count: 30, success_count: 26, failed_count: 3, skipped_count: 1,
    invalid_reward_count: 2, unknown_reward_count: 1, timezone: 'Asia/Shanghai',
    by_site: [{ site_id: 1, site_name: 'Alpha site', reward: 80.25, total_count: 30, success_count: 26, failed_count: 3, skipped_count: 1 }],
};

for (const width of [1440, 390]) {
    test(`check-in earnings display rewards and send site/account filters at ${width}px`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: 1000 });
        const state = await mockApp(page, 'checkin', { sites: [{ ...makeCheckinSite(), accounts: [account] }, makeCheckinSite(2, 'Beta site')] });
        const queries: URL[] = [];
        await page.route('**/api/v1/site/checkin-stats**', async route => {
            queries.push(new URL(route.request().url()));
            await route.fulfill({ json: { code: 200, data: stats } });
        });
        await page.goto('/');
        const panel = page.getByRole('region', { name: '签到收益', exact: true });
        await expect(panel.getByText('1.25', { exact: true })).toBeVisible();
        await expect(panel.getByText('7.5', { exact: true })).toBeVisible();
        await expect(panel.getByText('25.5', { exact: true })).toBeVisible();
        await expect(panel.getByText('80.25', { exact: true }).first()).toBeVisible();
        await expect(panel).toContainText('缺少奖励 1 条，无效数字奖励 2 条');
        await expect(panel).toContainText('30 次执行 · 成功 26 · 失败 3 · 跳过 1');
        expect(queries.at(-1)?.searchParams.get('timezone')).toBeTruthy();
        await panel.getByRole('combobox', { name: '收益统计站点' }).click();
        await page.getByRole('option', { name: 'Alpha site', exact: true }).click();
        await panel.getByRole('combobox', { name: '收益统计账号' }).click();
        await page.getByRole('option', { name: 'Alpha site / Primary account', exact: true }).click();
        await expect.poll(() => queries.at(-1)?.searchParams.get('account_id')).toBe('11');
        expect(queries.at(-1)?.searchParams.get('site_id')).toBe('1');
        await panel.getByRole('combobox', { name: '收益统计站点' }).click();
        await page.getByRole('option', { name: 'Beta site', exact: true }).click();
        await expect.poll(() => queries.at(-1)?.searchParams.get('site_id')).toBe('2');
        expect(queries.at(-1)?.searchParams.has('account_id')).toBe(false);
        await panel.getByText('按站点查看明细', { exact: true }).click();
        await expect(panel.getByRole('table')).toContainText('Alpha site');
        const bounds = await panel.boundingBox();
        expect(bounds!.x).toBeGreaterThanOrEqual(0);
        expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width + 1);
        await panel.screenshot({ path: testInfo.outputPath('checkin-earnings.png') });
        expect(state.unexpectedRequests).toEqual([]);
        expect(state.pageErrors).toEqual([]);
    });
}

test('earnings refresh after batch completion and recover from request errors', async ({ page }) => {
    const state = await mockApp(page, 'checkin');
    let finished = false;
    let failing = false;
    await page.route('**/api/v1/site/checkin-batches/latest', route => route.fulfill({ json: { code: 200, data: {
        id: '800001', status: finished ? 'completed' : 'running', trigger: 'manual', total: 1,
        attempted: finished ? 1 : 0, success: finished ? 1 : 0, partial: 0, failed: 0, skipped: 0,
        warnings: 0, canceled: false, duration_ms: 0, started_at: '2026-09-27T01:00:00Z', updated_at: '2026-09-27T01:00:00Z',
    } } }));
    await page.route('**/api/v1/site/checkin-stats**', route => route.fulfill(failing
        ? { status: 503, json: { code: 503, message: 'Temporarily unavailable' } }
        : { json: { code: 200, data: { ...stats, today_reward: finished ? 2.5 : 1.25 } } }));
    await page.goto('/');
    const panel = page.getByRole('region', { name: '签到收益', exact: true });
    await expect(panel.getByText('1.25', { exact: true })).toBeVisible();
    finished = true;
    await page.getByTestId('site-checkin-batch-status').getByRole('button', { name: '刷新任务状态', exact: true }).click();
    await expect(panel.getByText('2.5', { exact: true })).toBeVisible();
    failing = true;
    await panel.getByRole('button', { name: '刷新签到收益' }).click();
    await expect(panel.getByRole('alert')).toHaveText('签到收益加载失败，请刷新重试。', { timeout: 20000 });
    failing = false;
    await panel.getByRole('button', { name: '刷新签到收益' }).click();
    await expect(panel.getByRole('alert')).toHaveCount(0);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('check-in notification settings save values and roll back rejected edits', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 1000 });
    const settings = [
        { key: 'checkin_notify_enabled', value: 'false' }, { key: 'checkin_notify_webhook_url', value: '' },
        { key: 'checkin_notify_cooldown_seconds', value: '3600' }, { key: 'checkin_low_balance_threshold', value: '0' },
        { key: 'checkin_notify_success_enabled', value: 'false' }, { key: 'checkin_notify_manual_enabled', value: 'false' },
    ];
    const state = await mockApp(page, 'setting', { settings, mutate: request => {
        if (request.path !== '/api/v1/setting/set') return { status: 500, message: 'Unexpected mutation' };
        const body = request.body as { key: string; value: string };
        if (body.key === 'checkin_notify_cooldown_seconds' && body.value === '-1') {
            return { status: 400, message: 'Invalid cooldown' };
        }
        const setting = settings.find(setting => setting.key === body.key)!;
        setting.value = body.value;
        return { data: body };
    } });
    await page.goto('/');
    await page.getByRole('group', { name: '设置分类' }).getByRole('button', { name: '连接与任务' }).click();
    const card = page.locator('.page-card').filter({ has: page.getByRole('heading', { name: '签到结果通知', exact: true }) });
    await card.scrollIntoViewIfNeeded();
    await expect(card.getByRole('switch', { name: '启用签到结果通知' })).not.toBeChecked();
    await card.getByRole('switch', { name: '启用签到结果通知' }).click();
    await expect.poll(() => settings[0].value).toBe('true');
    await card.getByRole('switch', { name: '通知签到成功' }).click();
    await expect.poll(() => settings[4].value).toBe('true');
    await card.getByRole('switch', { name: '包含手动签到' }).click();
    await expect.poll(() => settings[5].value).toBe('true');
    const cooldown = card.getByRole('spinbutton', { name: '通知冷却时间（秒）' });
    await cooldown.fill('60');
    await cooldown.blur();
    await expect.poll(() => settings[2].value).toBe('60');
    const threshold = card.getByRole('spinbutton', { name: '低余额阈值（USD）' });
    await threshold.fill('0.5');
    await threshold.blur();
    await expect.poll(() => settings[3].value).toBe('0.5');
    await cooldown.fill('-1');
    await cooldown.blur();
    await expect(cooldown).toHaveValue('60');
    await card.screenshot({ path: testInfo.outputPath('checkin-notifications.png') });
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
