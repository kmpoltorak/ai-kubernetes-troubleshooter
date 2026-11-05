import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

const rules = process.env.BROWSER_RULES_URL ?? "";
const live = process.env.BROWSER_LIVE_URL ?? "";
if (!rules || !live) throw new Error("run through scripts/test-browser.sh");

// WCAG 2.1 A/AA rules axe can check automatically; manual review is still needed.
async function expectNoViolations(page: Page, state: string, include?: string) {
  const axe = new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]);
  const { violations } = await (include ? axe.include(include) : axe).analyze();
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
    // Each open explanation, in the form and in the report, is checked on its own: a
    // top-layer bubble covers part of the page, which axe cannot weigh in a page-wide
    // contrast check, and the page itself is checked above without bubbles.
    for (const name of ["What is Kind?", "What is the verdict and severity?", "What is evidence support?"]) {
      await page.getByRole("button", { name }).click();
      await expectNoViolations(page, `hint ${name}`, ":popover-open");
      await page.keyboard.press("Escape");
    }
    await page.goto(live);
    await expect(page.getByText("live ·")).toBeVisible();
    await expectNoViolations(page, "live");
  });
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`a long unbroken title does not widen the page at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto(live);
    // "_" keeps the strings unbreakable and outside the base64 blob redaction.
    await page.getByLabel("Title (optional)").fill("x_".repeat(100));
    await page.getByLabel("Description").fill("y_".repeat(150));
    await page.getByLabel("Namespace").selectOption("payments");
    await page.getByLabel("Deployment name").fill("checkout");
    await page.getByRole("button", { name: "Create incident" }).click();
    await expect(page.getByRole("heading", { level: 2, name: /^(x_)+$/ })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(viewport.width);
  });
}
