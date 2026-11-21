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
pnpm test:e2e:v2
```

The legacy compatibility runner explicitly sets `NEXT_PUBLIC_V2_UI=0` and starts
its server on `127.0.0.1:4173`. The default v2 suite uses `127.0.0.1:4174` and
includes independent check-in and workspace tests. Run the suites and production
build sequentially because they share `.next`. Both use Chromium with two workers
and no retries, as configured in [playwright.config.ts](../playwright.config.ts)
and [playwright.v2.config.ts](../playwright.v2.config.ts).

Legacy coverage retained until P6 includes:

- Site/account editing, deletion, status filters and Sub2API credential conflicts.
- Channel draft isolation, parameter validation, pending saves and settings retry.
- Check-in credentials, subscription tokens, status confirmation, batch tasks,
  history, rewards, time zones and capability settings.
- Notification channels, templates and check-in result settings.
- Authentication renewal, API key accessibility, routing previews and dashboards.
- Request contents, response status, trace tools and workspace navigation.

The default v2 suite covers independent sites/accounts/credentials, secret and
version handling, confirmation without resubmission, batch polling, notification
retry confirmation, string IDs, three languages and responsive layouts. Workspace
tests cover old navigation recovery, ordinary channels/groups, retired API
isolation, proxy reference navigation, settings and ZIP backup upload/export.

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
