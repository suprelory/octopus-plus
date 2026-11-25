import { expect, test, type Page } from '@playwright/test';
import { mockApp } from './fixtures';

async function storedToken(page: Page) {
    return page.evaluate(() => JSON.parse(localStorage.getItem('auth-storage') ?? '{}').state?.token);
}

function credentials() {
    const day = 24 * 60 * 60;
    const now = Math.floor(Date.now() / 1000);
    const claims = {
        ver: 2, sid: 'browser-session', auth_time: now - 25 * day, renewable: true,
        iat: now - 25 * day, nbf: now - 25 * day, exp: now + 5 * day,
    };
    return {
        token: `${Buffer.from('{"alg":"HS256"}').toString('base64url')}.${Buffer.from(JSON.stringify(claims)).toString('base64url')}.signature`,
        expireAt: new Date(claims.exp * 1000).toISOString(),
        claims,
    };
}

test('temporary verification failure preserves credentials and offers a working retry', async ({ page }) => {
    const state = await mockApp(page, 'group');
    let unavailable = true;
    let bootstrapRequests = 0;
    page.on('request', request => {
        if (new URL(request.url()).pathname === '/api/v1/user/bootstrap') bootstrapRequests++;
    });
    await page.route('**/api/v1/user/status', route => route.fulfill({
        status: unavailable ? 503 : 200,
        json: { code: unavailable ? 503 : 200, message: unavailable ? 'Temporary outage' : 'ok', data: null },
    }));
    await page.goto('/');
    await expect(page.getByRole('heading', { name: '暂时无法验证登录状态' })).toBeVisible();
    expect(await storedToken(page)).toBe('browser-test-token');
    expect(bootstrapRequests).toBe(0);
    unavailable = false;
    await page.getByRole('button', { name: '重新验证' }).click();
    await expect(page.getByRole('heading', { name: '分组概览' })).toBeVisible();
    expect(await storedToken(page)).toBe('browser-test-token');
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('real interaction renews once without remounting an open search field', async ({ page }) => {
    const original = credentials();
    const now = Math.floor(Date.now() / 1000);
    const renewedClaims = { ...original.claims, iat: now, nbf: now, exp: now + 30 * 24 * 60 * 60 };
    const renewed = {
        token: `${Buffer.from('{"alg":"HS256"}').toString('base64url')}.${Buffer.from(JSON.stringify(renewedClaims)).toString('base64url')}.signature`,
        expire_at: new Date(renewedClaims.exp * 1000).toISOString(),
    };
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    const state = await mockApp(page, 'group', { credentials: original, mutate: async request => {
        expect(request.path).toBe('/api/v1/user/refresh');
        expect(request.body).toEqual({});
        await gate;
        return { data: renewed };
    } });
    await page.goto('/');
    await expect(page.getByRole('heading', { name: '分组概览' })).toBeVisible();
    expect(state.mutations).toEqual([]);
    await page.getByRole('button', { name: '搜索', exact: true }).click();
    const search = page.getByRole('textbox', { name: '搜索', exact: true });
    await search.fill('unsaved-filter');
    await search.evaluate(element => { element.setAttribute('data-renewal-test', 'original-input'); });
    await expect.poll(() => state.mutations.length).toBe(1);
    release();
    await expect.poll(() => storedToken(page)).toBe(renewed.token);
    await expect(search).toHaveValue('unsaved-filter');
    await expect(search).toHaveAttribute('data-renewal-test', 'original-input');
    expect(state.mutations).toHaveLength(1);
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
});

test('logout in another tab promptly returns this tab to the login screen', async ({ page, context }) => {
    const state = await mockApp(page, 'group');
    await page.goto('/');
    await expect(page.getByRole('heading', { name: '分组概览' })).toBeVisible();
    const other = await context.newPage();
    await mockApp(other, 'group');
    await other.goto('/');
    await expect(other.getByRole('heading', { name: '分组概览' })).toBeVisible();
    await other.evaluate(() => localStorage.removeItem('auth-storage'));
    await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible();
    expect(await storedToken(page)).toBeNull();
    expect(state.unexpectedRequests).toEqual([]);
    expect(state.pageErrors).toEqual([]);
    await other.close();
});
