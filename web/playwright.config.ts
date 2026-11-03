import { defineConfig, devices } from "@playwright/test";

// scripts/test-browser.sh starts the servers and sets BROWSER_RULES_URL and BROWSER_FAILING_URL.
export default defineConfig({
  testDir: "browser",
  outputDir: "report-browser",
  timeout: 45_000,
  expect: { timeout: 10_000 },
  retries: 0,
  workers: 1,
  reporter: "list",
  use: { ...devices["Desktop Chrome"], trace: "retain-on-failure" },
});
