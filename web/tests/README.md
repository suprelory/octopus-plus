# Frontend regression tests

Run commands in this document from `web/`, using Node.js 24 and the exact pnpm
version declared by `packageManager` in [package.json](../package.json). The helper
tests in `tests/*.test.mjs` run with `pnpm test`; some transpile TypeScript fixtures.
Use a build-capable development environment or the repository's remote CI.

Browser tests exercise the real Next.js UI, React Query mutations and dialogs.
They use isolated browser contexts, synthetic authentication and intercepted API
responses; they require no running Go server or provider credentials.

```sh
pnpm install --frozen-lockfile
pnpm exec playwright install chromium
pnpm test:e2e
```

Playwright starts the management UI on `127.0.0.1:4173`. Run the browser suite
and production build sequentially because they share `.next`. The suite uses
Chromium with two workers and no retries, as configured in
[playwright.config.ts](../playwright.config.ts).

The workspace suite covers the following workflows.

- Independent check-in sites, accounts, credential and balance configuration,
  version conflicts, history, status confirmation and batch tasks.
- Notification channels, templates and check-in result settings.
- Authentication renewal, API key accessibility, routing previews and dashboards.
- Request contents, response status, trace tools and workspace navigation.
- Ordinary channels and groups, legacy navigation recovery, retired API isolation,
  proxy reference navigation, settings and ZIP backup upload/export.
- Simplified Chinese, Traditional Chinese and English labels, plus desktop and mobile layouts.

Unexpected API requests and browser errors fail the tests. Screenshots and traces
are retained under `test-results/` on failure; inspect a trace with
`pnpm exec playwright show-trace <trace.zip>`.

The [CI frontend job](../../.github/workflows/ci.yml) uses the prebuilt
`mcr.microsoft.com/playwright:v1.62.1-noble` image, matching `@playwright/test` in
[package.json](../package.json), so it does not install Chromium separately.
When browser tests fail, CI uploads `test-results/` and `playwright-report/` as
`playwright-<run_id>-<run_attempt>`, retained for seven days.

Run `pnpm exec tsc --noEmit`, `pnpm lint` and `pnpm build` for type, lint and
production build validation.
