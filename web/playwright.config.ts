import { defineConfig, devices } from "@playwright/test";

const MOCK_PORT = 4010;
const WEB_PORT = 3200;

/**
 * End-to-end tests run the production build against the Prism mock of
 * api/openapi/qavia.yaml (`make mock`), so they need no running backend and
 * cannot pass against an API shape the contract does not describe.
 */
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://localhost:${WEB_PORT}`,
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } },
    },
  ],
  webServer: [
    {
      command: `pnpm exec prism mock ../api/openapi/qavia.yaml --port ${MOCK_PORT} --errors=false`,
      url: `http://localhost:${MOCK_PORT}/healthz`,
      reuseExistingServer: !process.env.CI,
      stdout: "ignore",
    },
    {
      command: `pnpm build && pnpm start --port ${WEB_PORT}`,
      url: `http://localhost:${WEB_PORT}/login`,
      env: { NEXT_PUBLIC_API_BASE_URL: `http://localhost:${MOCK_PORT}` },
      reuseExistingServer: !process.env.CI,
      timeout: 180_000,
    },
  ],
});
