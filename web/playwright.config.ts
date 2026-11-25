import { defineConfig, devices } from '@playwright/test';

const baseURL = 'http://127.0.0.1:4173';

export default defineConfig({
    testDir: './tests/e2e',
    fullyParallel: true,
    forbidOnly: !!process.env.CI,
    retries: 0,
    workers: 2,
    timeout: 45_000,
    expect: { timeout: 10_000 },
    reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
    use: {
        baseURL,
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        contextOptions: { reducedMotion: 'reduce' },
    },
    projects: [{
        name: 'chromium',
        use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 1000 } },
    }],
    webServer: {
        command: 'pnpm exec next dev --hostname 127.0.0.1 --port 4173',
        url: baseURL,
        reuseExistingServer: false,
        timeout: 180_000,
        env: { NEXT_PUBLIC_API_BASE_URL: baseURL, NEXT_TELEMETRY_DISABLED: '1' },
    },
});
