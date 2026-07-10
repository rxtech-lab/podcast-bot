import { defineConfig, devices } from "@playwright/test";

const dashboardPort = Number(process.env.E2E_DASHBOARD_PORT ?? 3001);
const backendPort = Number(process.env.E2E_BACKEND_PORT ?? 8000);
const backendURL = `http://127.0.0.1:${backendPort}`;

export default defineConfig({
  testDir: "./e2e",
  timeout: 45_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: `http://127.0.0.1:${dashboardPort}`,
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: [
    {
      command: `E2E_PORT=${backendPort} ../scripts/admin-e2e-server.sh`,
      url: backendURL,
      timeout: 120_000,
      reuseExistingServer: false,
      stdout: "pipe",
      stderr: "pipe",
    },
    {
      command: [
        "E2E_MODE=true",
        "NEXT_DIST_DIR=.next-e2e",
        `ENGINE_BASE_URL=${backendURL}`,
        "AUTH_SECRET=admin-e2e-secret-admin-e2e-secret",
        "AUTH_ISSUER=http://127.0.0.1:9",
        "AUTH_CLIENT_ID=e2e",
        "AUTH_CLIENT_SECRET=e2e",
        `bun run dev --hostname 127.0.0.1 --port ${dashboardPort}`,
      ].join(" "),
      url: `http://127.0.0.1:${dashboardPort}/admin/subscription-permissions`,
      timeout: 120_000,
      reuseExistingServer: false,
      stdout: "pipe",
      stderr: "pipe",
    },
  ],
});
