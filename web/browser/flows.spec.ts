import { readFileSync } from "node:fs";
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

test("live mode hides simulation controls and fills the configured cluster", async ({ page }) => {
  await page.goto(live);
  await expect(page.getByText("live · browser-live · rules/rules-v2")).toBeVisible();
  await expect(page.getByRole("group", { name: "Sample incidents" })).toHaveCount(0);
  await expect(page.getByLabel("Cluster")).toHaveValue("browser-live");
  await expect(page.getByLabel("Cluster")).not.toBeEditable();
  await expect(page.getByLabel("Namespace").locator("option")).toHaveText(["Choose…", "payments", "checkout"]);
  await page.getByLabel("Description").fill("Checkout returns 503 after the release.");
  await page.getByLabel("Namespace").selectOption("payments");
  // This server's cluster is unreachable: listing fails visibly and typing a name still works.
  await expect(page.getByText(/Could not list names .*Type the name\./)).toBeVisible();
  // The kinds come from the server; a StatefulSet incident passes the API and the database.
  await expect(page.getByLabel("Kind").locator("option")).toHaveText(["Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Service", "Pod", "Ingress", "PersistentVolumeClaim", "HorizontalPodAutoscaler"]);
  await page.getByLabel("Kind").selectOption("statefulset");
  await page.getByLabel("StatefulSet name").fill("checkout-db");
  await page.getByRole("button", { name: "Create incident" }).click();
  await expect(page.getByRole("button", { name: "Run investigation" })).toBeEnabled();
  await expect(page.getByText("statefulset/checkout-db").first()).toBeVisible();
  await expect(page.getByLabel("Scenario")).toHaveCount(0);
});

async function investigateSample(page: Page) {
  await createSample(page, rules, "OOM");
  await page.getByRole("button", { name: "Run investigation" }).click();
  await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
}

async function exported(page: Page, label: string) {
  const [file] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: label }).click()]);
  return { name: file.suggestedFilename(), body: readFileSync((await file.path())!, "utf8") };
}

test("attempts export as Markdown and JSON, and an older attempt is marked as older", async ({ page }) => {
  await investigateSample(page);
  const md = await exported(page, "Export Markdown");
  expect(md.name).toMatch(/^incident-[0-9a-f]{8}-attempt-[0-9a-f]{8}\.md$/);
  expect(md.body).toContain("# settlement restarts under load");
  expect(md.body).toContain("**Container OOMKilled: memory exhaustion**");
  expect(md.body).toMatch(/- Evidence support: \w+ \(describes the evidence/);
  const first = JSON.parse((await exported(page, "Export JSON")).body);
  expect(first.export.schema_version).toBe(2);
  expect(first.evidence.length).toBeGreaterThan(0);

  const history = page.getByRole("region", { name: "Investigation history" });
  await page.getByRole("button", { name: "Run investigation" }).click();
  await history.getByText("Investigation history").click();
  await expect(history.getByRole("button", { name: /^completed/ })).toHaveCount(2);
  await expect(page.getByRole("note")).toHaveCount(0);
  // Newest first: the second attempt is the one exported above.
  await history.getByRole("button", { name: /^completed/ }).nth(1).click();
  await expect(page.getByRole("note")).toContainText("Older attempt");
  expect(JSON.parse((await exported(page, "Export JSON")).body).investigation.id).toBe(first.investigation.id);
  await history.getByRole("button", { name: /Latest attempt/ }).click();
  await expect(page.getByRole("note")).toHaveCount(0);
});

test("copying a command reports success and a denied clipboard", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await investigateSample(page);
  const line = page.getByRole("listitem").filter({ has: page.locator("code") }).first();
  const command = await line.locator("code").innerText();
  await line.getByRole("button", { name: "copy" }).click();
  await expect(line.getByRole("button", { name: "copied" })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command);

  await page.evaluate(() => { navigator.clipboard.writeText = () => Promise.reject(new DOMException("denied", "NotAllowedError")); });
  await line.getByRole("button", { name: "copy" }).click();
  await expect(line.getByRole("button", { name: "copy failed: select the command" })).toBeVisible();
});

test("the incident list pages through older incidents", async ({ page, request }) => {
  for (let i = 0; i < 21; i++) {
    const data = { title: `paging ${i}`, description: "Paging check", cluster: "browser-live", namespace: "payments", resource_type: "deployment", resource_name: `app-${i}` };
    for (let tries = 0; ; tries++) {
      const res = await request.post(`${live}/api/v1/incidents`, { data });
      if (res.status() === 201) break;
      expect(res.status() === 429 && tries < 10, `create incident: ${res.status()}`).toBe(true);
      await page.waitForTimeout(1000);
    }
  }
  await page.goto(live);
  const items = page.locator("li > button[aria-current]");
  await expect(items).toHaveCount(20);
  await expect(page.getByText("Page 1", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Newer incidents" })).toBeDisabled();
  await page.getByRole("button", { name: "Older incidents" }).click();
  await expect(page.getByText("Page 2", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Newer incidents" })).toBeEnabled();
  await expect(items.first()).toBeVisible();
  await page.getByRole("button", { name: "Newer incidents" }).click();
  await expect(items).toHaveCount(20);
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

test("hints explain confusing fields and the name field follows the kind", async ({ page }) => {
  await page.goto(rules);
  const hint = page.getByRole("button", { name: "What is Cluster?" });
  await expect(hint).toHaveAttribute("aria-expanded", "false");
  const bubble = page.getByText(/KUBERNETES_CLUSTER_NAME/);
  await expect(bubble).toBeHidden();
  await hint.click();
  await expect(bubble).toBeVisible();
  await expect(hint).toHaveAttribute("aria-expanded", "true");
  // A bubble over the page: the form below does not move, and it stays inside the viewport.
  const box = (await bubble.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await page.keyboard.press("Escape");
  await expect(bubble).toBeHidden();
  await expect(hint).toHaveAttribute("aria-expanded", "false");
  await hint.click();
  await page.getByRole("heading", { name: "New incident" }).click(); // a click elsewhere closes it
  await expect(bubble).toBeHidden();
  // The label still names its control, not the "?" next to it.
  await expect(page.getByLabel("Cluster")).not.toBeEditable();
  await page.getByLabel("Deployment name").fill("payment-service");
  await page.getByLabel("Kind").selectOption("job");
  await expect(page.getByLabel("Job name")).toHaveValue("payment-service");
  await page.getByRole("button", { name: "What is Job name?" }).click();
  await expect(page.getByText("The name of the Job in the chosen namespace", { exact: false })).toBeVisible();
  // On a phone the right-hand field's bubble is pulled back inside the screen.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "What is Job name?" }).click();
  const phone = (await page.getByText("The name of the Job in the chosen namespace", { exact: false }).boundingBox())!;
  expect(phone.x).toBeGreaterThanOrEqual(0);
  expect(phone.x + phone.width).toBeLessThanOrEqual(390);
});
