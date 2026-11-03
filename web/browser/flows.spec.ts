import { expect, test, type Page } from "@playwright/test";

const rules = process.env.BROWSER_RULES_URL ?? "";
const failing = process.env.BROWSER_FAILING_URL ?? "";
if (!rules || !failing) throw new Error("run through scripts/test-browser.sh");

async function createSample(page: Page, base: string, sample: string) {
  await page.goto(base);
  await page.getByRole("group", { name: "Sample incidents" }).getByRole("button", { name: sample }).click();
  await page.getByRole("button", { name: "Create incident" }).click();
  await expect(page.getByRole("button", { name: "Run investigation" })).toBeEnabled();
}

async function tabTo(page: Page, name: string) {
  for (let i = 0; i < 60; i++) {
    await page.keyboard.press("Tab");
    if (await page.evaluate((n) => document.activeElement?.textContent?.trim() === n, name)) return;
  }
  throw new Error(`${name} is not reachable with Tab`);
}

const jobStatus = (page: Page) => page.getByRole("region", { name: "Investigation job" });

test("a sample investigation links citations to evidence and survives a reload", async ({ page }) => {
  await createSample(page, rules, "OOM");
  await page.getByRole("button", { name: "Run investigation" }).click();
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
  await expect(page.getByText("Evidence support:")).toBeVisible();

  const citation = page.locator('a[href^="#evidence-"]').first();
  const target = await citation.getAttribute("href");
  await citation.click();
  await expect(page.locator(target!)).toBeInViewport();

  await page.reload();
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
  await expect(page.getByText("Latest successful report:")).toBeVisible();
});

test("a reload during analysis resumes the job and keeps the failed attempt's evidence", async ({ page }) => {
  await createSample(page, failing, "CrashLoop");
  await page.getByRole("button", { name: "Run investigation" }).click();
  await expect(jobStatus(page)).toContainText("Stage: analyzing");

  await page.reload();
  await expect(jobStatus(page)).toContainText("Stage: analyzing");
  await expect(jobStatus(page)).toContainText("Job: failed", { timeout: 30_000 });

  const attempt = page.getByRole("region", { name: "Selected investigation" });
  await expect(attempt).toContainText("Status: failed");
  await expect(attempt.getByRole("alert")).not.toBeEmpty();
  await expect(page.getByRole("row", { name: /get_pods/ })).toBeVisible();
  await expect(page.getByText("Latest successful report:")).toHaveCount(0);
});

test("the main flow works with the keyboard only", async ({ page }) => {
  await page.goto(rules);
  await tabTo(page, "Service");
  await page.keyboard.press("Enter");
  await tabTo(page, "Create incident");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Run investigation" })).toBeEnabled();
  await tabTo(page, "Run investigation");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: /selector/i })).toBeVisible();
});

async function failStatusReads(page: Page, status: number) {
  const state = { fail: true };
  await page.route("**/api/v1/incidents/*/jobs/*", (route) =>
    state.fail
      ? route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ error: { code: "unavailable", message: "status read blocked by test" }, request_id: "browser" }) })
      : route.continue());
  return state;
}

test("a transient status error recovers without user action", async ({ page }) => {
  await createSample(page, rules, "OOM");
  const reads = await failStatusReads(page, 503);
  await page.getByRole("button", { name: "Run investigation" }).click();
  await expect(jobStatus(page)).toContainText("Status unavailable");
  reads.fail = false;
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
});

test("a rejected status read stops polling until the user retries", async ({ page }) => {
  await createSample(page, rules, "OOM");
  const reads = await failStatusReads(page, 401);
  await page.getByRole("button", { name: "Run investigation" }).click();
  await expect(jobStatus(page)).toContainText("Status unavailable");
  reads.fail = false;
  // Longer than one 2s poll interval: a stopped watcher must not recover by itself.
  await page.waitForTimeout(3000);
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toHaveCount(0);
  await page.getByRole("button", { name: "Retry status check" }).click();
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
});
