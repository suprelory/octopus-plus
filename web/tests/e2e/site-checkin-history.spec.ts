import { expect, test } from '@playwright/test';
import { SiteCredentialType, type SiteAccount } from '../../src/api/endpoints/site-types';
import type { SiteCheckinLog } from '../../src/api/endpoints/site-checkin';
import { makeCheckinSite, mockApp } from './fixtures';

const account: SiteAccount = {
    id: 11, site_id: 1, name: 'Primary account', credential_type: SiteCredentialType.AccessToken,
    username: '', password: '', access_token: 'test-token', api_key: '', refresh_token: '',
    token_expires_at: 0, proxy_mode: 'inherit', enabled: true, auto_sync: false,
    auto_checkin: true, random_checkin: false,
    checkin_random_window_minutes: 0, checkin_failure_count: 0,
    last_sync_status: 'idle', last_checkin_status: 'idle', last_sync_message: '',
    last_checkin_message: '', balance: 0, balance_used: 0, today_income: 0,
    tokens: [], user_groups: [], models: [], channel_bindings: [],
};

const history: SiteCheckinLog[] = Array.from({ length: 21 }, (_, index) => {
    const id = 120 - index;
    return {
        id, site_id: 1, account_id: 11, site_name: 'Alpha site', account_name: 'Primary account', platform: 'one-api',
        source: id >= 118 ? 'manual' : 'scheduled', status: id >= 119 ? 'success' : id === 118 ? 'skipped' : 'failed',
        reason: id === 120 ? 'checked_in' : id === 119 ? 'already_checked_in' : id === 118 ? 'already_running' : 'upstream_http_error',
        message: id === 100 ? 'Older attempt' : id >= 118 ? '' : 'HTTP 429 rate limited',
        reward: id === 120 ? '12.5' : '', duration_ms: 250,
        started_at: '2026-09-27T01:00:00Z', finished_at: '2026-09-27T01:00:00.250Z',
    };
});

test('check-in history shows outcomes and rewards, paginates, and sends filters', async ({ page }) => {
    const state = await mockApp(page, 'checkin', { sites: [{ ...makeCheckinSite(), accounts: [account] }] });
    const queries: URL[] = [];
    await page.route('**/api/v1/site/checkin-logs**', async route => {
        const url = new URL(route.request().url());
        queries.push(url);
        const q = url.searchParams;
        const rows = history.filter(row =>
            (!q.get('before_id') || row.id < Number(q.get('before_id'))) &&
            (!q.get('status') || row.status === q.get('status')) &&
            (!q.get('source') || row.source === q.get('source')) &&
            (!q.get('site_id') || row.site_id === Number(q.get('site_id'))) &&
            (!q.get('account_id') || row.account_id === Number(q.get('account_id'))));
        const limit = Number(q.get('limit'));
        await route.fulfill({ json: { code: 200, data: {
            items: rows.slice(0, limit), next_before_id: rows.length > limit ? rows[limit - 1].id : undefined,
        } } });
    });
    await page.goto('/');
    await page.getByRole('button', { name: '签到记录', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '签到记录' });
    await expect(dialog.getByText('12.5', { exact: true })).toBeVisible();
    await expect(dialog.getByRole('row').filter({ hasText: '今日已签到' })).toContainText('—');
    await expect(dialog.getByRole('row').filter({ hasText: '正在执行，已跳过' })).toContainText('跳过');
    await dialog.getByRole('button', { name: '下一页', exact: true }).click();
    await expect(dialog.getByText('Older attempt', { exact: true })).toBeVisible();
    await expect(dialog.getByRole('button', { name: '下一页', exact: true })).toBeDisabled();
    expect(queries.at(-1)?.searchParams.get('before_id')).toBe('101');
    await dialog.getByRole('button', { name: '上一页', exact: true }).click();
    await expect(dialog.getByText('12.5', { exact: true })).toBeVisible();
    for (const [label, option] of [['执行状态', '失败'], ['触发方式', '定时'], ['站点', 'Alpha site'], ['账号', 'Alpha site / Primary account']]) {
        await dialog.getByRole('combobox', { name: label, exact: true }).click();
        await page.getByRole('option', { name: option, exact: true }).click();
    }
    await expect.poll(() => queries.at(-1)?.searchParams.get('account_id')).toBe('11');
    const filter = queries.at(-1)!.searchParams;
    expect(filter.get('status')).toBe('failed');
    expect(filter.get('source')).toBe('scheduled');
    expect(filter.get('site_id')).toBe('1');
    expect(filter.has('before_id')).toBe(false);
    await dialog.getByLabel('开始日期', { exact: true }).fill('2026-09-27');
    await dialog.getByLabel('结束日期', { exact: true }).fill('2026-09-26');
    await expect(dialog.getByRole('alert')).toHaveText('结束日期不能早于开始日期');
    await dialog.getByLabel('结束日期', { exact: true }).fill('2026-09-27');
    await expect.poll(() => queries.at(-1)?.searchParams.has('until')).toBe(true);
    await expect(dialog.getByRole('alert')).toHaveCount(0);
    await dialog.getByRole('button', { name: '关闭', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('check-in history remains usable on mobile and recovers from a query failure', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const state = await mockApp(page, 'checkin');
    let failing = false;
    await page.route('**/api/v1/site/checkin-logs**', async route => {
        await route.fulfill(failing
            ? { status: 503, json: { code: 503, message: 'History temporarily unavailable' } }
            : { json: { code: 200, data: { items: [] } } });
    });
    await page.goto('/');
    await page.getByRole('button', { name: '签到记录', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '签到记录' });
    await expect(dialog.getByText('没有符合条件的签到记录。')).toBeVisible();
    const bounds = await dialog.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(391);
    failing = true;
    await dialog.getByRole('button', { name: '刷新', exact: true }).click();
    await expect(dialog.getByRole('alert')).toContainText('History temporarily unavailable', { timeout: 20000 });
    failing = false;
    await dialog.getByRole('button', { name: '刷新', exact: true }).click();
    await expect(dialog.getByText('没有符合条件的签到记录。')).toBeVisible();
    await expect(dialog.getByRole('alert')).toHaveCount(0);
    await dialog.getByRole('button', { name: '关闭', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});
