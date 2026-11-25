import { expect, test, type Page } from '@playwright/test';
import { emptyStats, makeChannel, mockApp } from './fixtures';

const channels = [
    makeChannel(1, 'OpenAI Primary'),
    makeChannel(2, 'Anthropic Direct', { type: 2, model: 'claude-sonnet-4,claude-opus-4' }),
    makeChannel(3, 'Gemini Flash', { type: 3, model: 'gemini-2.5-flash,gemini-2.5-pro' }),
    makeChannel(4, 'OpenAI Backup', { enabled: false }),
    makeChannel(5, 'Embeddings', { type: 5, model: 'text-embedding-3-large' }),
    makeChannel(6, 'Responses', { type: 1 }),
];

function dailyStats() {
    return Array.from({ length: 30 }, (_, index) => {
        const date = new Date();
        date.setDate(date.getDate() - 29 + index);
        const volume = 200 + (index * 317) % 1400;
        return { ...emptyStats, date: `${date.getFullYear()}${String(date.getMonth() + 1).padStart(2, '0')}${String(date.getDate()).padStart(2, '0')}`,
            request_success: volume, request_failed: index % 7, input_token: volume * 300,
            output_token: volume * 110, input_cost: volume / 500, output_cost: volume / 800, wait_time: volume * 1400 };
    });
}

async function expectNoPageOverflow(page: Page) {
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
}

async function capture(page: Page, path: string) {
    // Allow the app shell's entrance animation to settle before taking a visual reference.
    await page.waitForTimeout(500);
    await page.screenshot({ path, fullPage: true, animations: 'disabled' });
}

for (const { width, theme } of [{ width: 1440, theme: 'light' }, { width: 390, theme: 'dark' }] as const) {
    test(`${width}px ${theme} home shows period metrics, chart and channel rankings`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: 1000 });
        await page.addInitScript(theme => localStorage.setItem('theme', theme), theme);
        const state = await mockApp(page, 'home', {
            channels, statsDaily: dailyStats(),
            statsHourly: [{ ...emptyStats, hour: 10, date: '20260925', request_success: 8, input_cost: 1.25 }],
            statsTotal: { ...emptyStats, id: 1, request_success: 24860, input_token: 8700000, output_token: 3200000, input_cost: 248.6, output_cost: 160.2, wait_time: 38000000 },
        });
        await page.goto('/');
        await expect(page.getByRole('heading', { name: '用量概览' })).toBeVisible();
        await expect(page.getByRole('heading', { name: '费用趋势' })).toBeVisible();
        await page.getByRole('tab', { name: '今日', exact: true }).click();
        await expect(page.getByRole('region', { name: '用量概览' })).toContainText('1.25');
        await page.getByRole('tab', { name: '30 天', exact: true }).click();
        await expect(page.getByRole('tab', { name: '30 天', exact: true })).toHaveAttribute('aria-selected', 'true');
        await expectNoPageOverflow(page);
        await capture(page, testInfo.outputPath('home-overview.png'));
        const rank = page.getByRole('region', { name: '排行榜' });
        await rank.getByRole('tab', { name: '次数', exact: true }).click();
        await expect(rank.getByText('成功率:', { exact: false }).first()).toBeVisible();
        await expect(rank.getByText('Responses', { exact: true })).toHaveCount(1);
        await rank.getByRole('tab', { name: 'Tokens', exact: true }).click();
        await expect(rank.getByText('2.22', { exact: false })).toBeVisible();
        expect(state.pageErrors).toEqual([]);
        expect(state.unexpectedRequests).toEqual([]);
    });

    test(`${width}px ${theme} channel cards preserve details, search and layout changes`, async ({ page }, testInfo) => {
        await page.setViewportSize({ width, height: 1000 });
        await page.addInitScript(theme => localStorage.setItem('theme', theme), theme);
        const state = await mockApp(page, 'channel', { channels });
        await page.goto('/');
        const card = page.getByRole('button', { name: '查看 OpenAI Primary', exact: true });
        await expect(card).toBeVisible();
        await expect(card).toContainText('2 个模型');
        await capture(page, testInfo.outputPath('manual-channels.png'));
        await card.click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await page.keyboard.press('Escape');
        await expect(page.getByRole('dialog')).not.toBeVisible();
        await page.getByRole('button', { name: '搜索', exact: true }).click();
        await page.getByRole('textbox', { name: '搜索', exact: true }).fill('no-such-channel');
        await expect(page.getByText('没有找到匹配的渠道', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: '清空搜索', exact: true }).click();
        await page.getByRole('button', { name: '视图选项' }).click();
        await page.getByRole('button', { name: '列表', exact: true }).click();
        await page.keyboard.press('Escape');
        await expect(card).toBeVisible();
        await expectNoPageOverflow(page);
        await capture(page, testInfo.outputPath('manual-channels-list.png'));
        expect(state.mutations).toEqual([]);
        expect(state.pageErrors).toEqual([]);
        expect(state.unexpectedRequests).toEqual([]);
    });
}

test('a keyboard toggle updates the channel without opening its detail dialog', async ({ page }) => {
    const channel = makeChannel();
    const state = await mockApp(page, 'channel', { channels: [channel], mutate: request => {
        expect(request.path).toBe('/api/v1/channel/enable');
        expect(request.body).toEqual({ id: 1, enabled: false });
        channel.enabled = false;
        return { data: null };
    } });
    await page.goto('/');
    const toggle = page.getByRole('switch', { name: '启用 OpenAI Primary' });
    await toggle.focus();
    await page.keyboard.press('Space');
    await expect(toggle).not.toBeChecked();
    await expect(page.getByRole('dialog')).not.toBeVisible();
    expect(state.mutations).toHaveLength(1);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('empty usage and channels show useful empty states', async ({ page }) => {
    const state = await mockApp(page, 'home');
    await page.goto('/');
    await expect(page.getByText('还没有用量数据', { exact: true })).toBeVisible();
    await expect(page.getByText('暂无排行数据', { exact: true })).toBeVisible();
    await page.getByRole('navigation').getByRole('button', { name: '渠道', exact: true }).click();
    await expect(page.getByText('连接你的第一个渠道', { exact: true })).toBeVisible();
    expect(state.pageErrors).toEqual([]);
    expect(state.unexpectedRequests).toEqual([]);
});
