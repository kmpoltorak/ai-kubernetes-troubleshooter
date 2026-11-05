import { expect, test, type Page } from "@playwright/test";

const rules = process.env.BROWSER_RULES_URL ?? "";
const failing = process.env.BROWSER_FAILING_URL ?? "";
const live = process.env.BROWSER_LIVE_URL ?? "";
if (!rules || !failing || !live) throw new Error("run through scripts/test-browser.sh");

async function createSample(page: Page, base: string, sample: string) {
  await page.goto(base);
  await page.getByRole("group", { name: "Sample incidents" }).getByRole("button", { name: sample }).click();
  await page.getByRole("button", { name: "Create incident" }).click();
  await expect(page.getByRole("button", { name: "Run investigation" })).toBeEnabled();
}

const jobStatus = (page: Page) => page.getByRole("region", { name: "Investigation job" });

test("a sample investigation links citations to evidence and survives a reload", async ({ page }) => {
  await createSample(page, rules, "OOM");
  await expect(page.getByText("simulation · local · rules/rules-v2")).toBeVisible();
  await expect(page.getByLabel("Scenario")).toHaveValue("oom_killed");
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

test("live mode with two clusters shows their state and each cluster's namespaces", async ({ page }) => {
  await page.goto(live);
  await expect(page.getByText("live · 2 clusters · rules/rules-v2")).toBeVisible();
  await expect(page.getByRole("group", { name: "Sample incidents" })).toHaveCount(0);
  // Both clusters' API servers refuse connections: the header names them.
  await expect(page.getByText("browser-live, browser-edge unreachable · 0/2 reachable")).toBeVisible();
  await expect(page.getByLabel("Cluster").locator("option")).toHaveText(["Choose…", "browser-live (unreachable)", "browser-edge (unreachable)"]);
  await page.getByLabel("Cluster").selectOption("browser-edge");
  await expect(page.getByLabel("Namespace")).toHaveValue("orders");
  await page.getByLabel("Cluster").selectOption("browser-live");
  await expect(page.getByLabel("Namespace").locator("option")).toHaveText(["Choose…", "payments", "checkout"]);
  await page.getByLabel("Description").fill("Checkout returns 503 after the release.");
  await page.getByLabel("Namespace").selectOption("payments");
  // This server's cluster is unreachable: listing fails visibly and typing a name still works.
  await expect(page.getByText(/Could not list names .*Type the name\./)).toBeVisible();
  // The kinds come from the server; a StatefulSet incident passes the API and the database.
  await expect(page.getByLabel("Kind").locator("option")).toHaveText(["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Service", "Pod", "Ingress", "PersistentVolumeClaim", "HorizontalPodAutoscaler"]);
  await page.getByLabel("Kind").selectOption("statefulset");
  await page.getByLabel("Name", { exact: true }).fill("checkout-db");
  await page.getByRole("button", { name: "Create incident" }).click();
  await expect(page.getByRole("button", { name: "Run investigation" })).toBeEnabled();
  await expect(page.getByText("statefulset/checkout-db").first()).toBeVisible();
  await expect(page.getByLabel("Scenario")).toHaveCount(0);
});

test("incidents are deleted one at a time and in bulk after confirmation", async ({ page, request }) => {
  const titles = ["delete one", "delete two", "delete three"];
  for (const title of titles) {
    const data = { title, description: "Deletion check", cluster: "local", namespace: "payments", resource_type: "deployment", resource_name: "payment-service" };
    for (let tries = 0; ; tries++) {
      const res = await request.post(`${rules}/api/v1/incidents`, { data });
      if (res.status() === 201) break;
      expect(res.status() === 429 && tries < 10, `create incident: ${res.status()}`).toBe(true);
      await page.waitForTimeout(1000);
    }
  }
  await page.goto(rules);
  const row = (title: string) => page.getByRole("button", { name: new RegExp(`^${title}`) });
  // Bulk: two checked rows; the dialog names the count.
  await page.getByRole("checkbox", { name: "Select delete one" }).check();
  await page.getByRole("checkbox", { name: "Select delete two" }).check();
  let message = "";
  page.once("dialog", (d) => { message = d.message(); void d.accept(); });
  await page.getByRole("button", { name: "Delete selected (2)" }).click();
  expect(message).toContain("Delete 2 incidents");
  await expect(page.getByRole("status").filter({ hasText: "2 deleted." })).toBeVisible();
  await expect(row("delete one")).toHaveCount(0);
  await expect(row("delete two")).toHaveCount(0);

  // Single, from the detail view: dismissing keeps it, accepting removes it and clears the selection.
  await row("delete three").click();
  page.once("dialog", (d) => void d.dismiss());
  await page.getByRole("button", { name: "Delete incident" }).click();
  await expect(row("delete three")).toHaveCount(1);
  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Delete incident" }).click();
  await expect(page.getByRole("status").filter({ hasText: "1 deleted." })).toBeVisible();
  await expect(row("delete three")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Select or create an incident" })).toBeVisible();
  expect(new URL(page.url()).search).toBe("");
});
