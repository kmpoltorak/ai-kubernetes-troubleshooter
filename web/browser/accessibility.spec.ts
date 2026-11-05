import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

const rules = process.env.BROWSER_RULES_URL ?? "";
const live = process.env.BROWSER_LIVE_URL ?? "";
if (!rules || !live) throw new Error("run through scripts/test-browser.sh");

// WCAG 2.1 A/AA rules axe can check automatically; manual review is still needed.
async function expectNoViolations(page: Page, state: string) {
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(violations.map((v) => `${state}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

for (const colorScheme of ["light", "dark"] as const) {
  test(`main states meet WCAG A/AA automated checks in ${colorScheme} mode`, async ({ page }) => {
    await page.emulateMedia({ colorScheme });
    await page.goto(rules);
    await expect(page.getByText("simulation ·")).toBeVisible();
    await expectNoViolations(page, "start");
    await page.getByRole("button", { name: "OOM" }).click();
    await page.getByRole("button", { name: "Create incident" }).click();
    await page.getByRole("button", { name: "Run investigation" }).click();
    await expect(page.getByRole("heading", { name: /OOMKilled/ })).toBeVisible();
    await expectNoViolations(page, "report");
    await page.goto(live);
    await expect(page.getByText("live ·")).toBeVisible();
    await expectNoViolations(page, "live");
  });
}

{
  const viewport = { width: 390, height: 844 }; // the phone width where long titles overflowed
  test("a long unbroken title does not widen the page on a phone", async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto(live);
    // "_" keeps the strings unbreakable and outside the base64 blob redaction.
    await page.getByLabel("Title (optional)").fill("x_".repeat(100));
    await page.getByLabel("Description").fill("y_".repeat(150));
    await page.getByLabel("Cluster").selectOption("browser-live");
    await page.getByLabel("Namespace").selectOption("payments");
    await page.getByLabel("Name", { exact: true }).fill("checkout");
    await page.getByRole("button", { name: "Create incident" }).click();
    await expect(page.getByRole("heading", { level: 2, name: /^(x_)+$/ })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(viewport.width);
  });
}
