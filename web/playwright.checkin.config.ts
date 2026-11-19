import { defineConfig } from '@playwright/test';
import base from './playwright.config';

const baseURL = 'http://127.0.0.1:4174';

export default defineConfig({
    ...base,
    testIgnore: [],
    testMatch: '**/independent-checkin*.spec.ts',
    use: { ...base.use, baseURL },
    webServer: {
        command: 'pnpm exec next dev --hostname 127.0.0.1 --port 4174',
        url: baseURL,
        reuseExistingServer: false,
        timeout: 180_000,
        env: { NEXT_PUBLIC_API_BASE_URL: baseURL, NEXT_PUBLIC_CHECKIN_V2: '1', NEXT_TELEMETRY_DISABLED: '1' },
    },
});
