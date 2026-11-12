import { test, expect } from "./fixtures";

test("redesign keeps live node data, themes and platform navigation accessible", async ({ page, login }) => {
  await login();
  for (const width of [1440, 768, 360]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/overview");
    await expect(page.getByTestId("status-banner")).toBeVisible();
    await expect(page.getByTestId("kpi-grid").getByRole("link")).toHaveCount(4);
    await expect(page.getByText("Состояние выбранной ноды и действия, которым нужно внимание.")).toBeVisible();
    for (const theme of ["light", "dark", "mocha", "parchment"]) {
      await page.evaluate((value) => document.documentElement.dataset["theme"] = value, theme);
      await page.evaluate(() => document.fonts.ready);
      await expect(page.locator(".overview-health-hero .tabular-nums").first()).toHaveCSS("color", theme === "mocha" ? "rgb(33, 30, 26)" : theme === "parchment" ? "rgb(255, 251, 244)" : "rgb(255, 255, 255)");
      await expect(page.locator(".overview-health-hero")).toHaveCSS("color", theme === "mocha" ? "rgb(33, 30, 26)" : theme === "parchment" ? "rgb(255, 251, 244)" : "rgb(255, 255, 255)");
      expect(await page.evaluate(() => document.fonts.check('700 16px "Manrope"'))).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1);
      await page.screenshot({ animations: "disabled", path: test.info().outputPath(`overview-${width}-${theme}.png`) });
    }
    await page.getByRole("link", { name: "Открыть диагностику", exact: true }).click();
    await expect(page).toHaveURL(/\/pulse$/);
    await expect(page.getByTestId("hub-card-dc")).toBeVisible();
    if (width < 1180) {
      await page.getByRole("button", { name: "Ещё", exact: true }).click();
      await page.getByRole("menuitem", { name: "Кластеры", exact: true }).click();
    } else {
      await page.getByTestId("full-sidebar").getByRole("link", { name: "Кластеры", exact: true }).click();
    }
    await expect(page).toHaveURL(/\/clusters$/);
  }
});

test("shared controls and typography reflow across existing task screens", async ({ page, login }) => {
  await login();
  for (const width of [1440, 360]) {
    await page.setViewportSize({ width, height: 900 });
    for (const route of ["/nodes", "/people", "/server/config", "/server/settings", "/server/updates", "/geography", "/journal", "/clusters", "/billing", "/portal"]) {
      await page.goto(route);
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), `${route} at ${width}`).toBeLessThanOrEqual(1);
      await page.screenshot({ animations: "disabled", path: test.info().outputPath(`screen-${width}-${route.replaceAll("/", "-")}.png`) });
    }
  }
});


test("node registry preserves document scope and diagnostics search uses the live snapshot", async ({ page, login }) => {
  await login();
  await page.goto("/nodes");
  await expect(page.getByRole("link", { name: "Открыть сводку" }).first()).toHaveAttribute("href", /\/overview\?node=default$/);
  await page.getByRole("searchbox", { name: "Поиск по имени" }).fill("no-matching-node");
  await expect(page.getByText("Ноды не найдены")).toBeVisible();
  await page.goto("/pulse");
  await page.getByRole("button", { name: "Показать поля" }).click();
  await page.getByRole("combobox", { name: "Раздел", exact: true }).selectOption("dcs");
  const search = page.getByRole("searchbox", { name: "Поиск по полному пути" });
  await expect(page.getByRole("table", { name: "" }).filter({ has: page.getByRole("columnheader", { name: "Полный путь" }) })).toBeVisible();
  await search.fill("no-matching-field");
  await expect(page.getByText("Совпадений нет")).toBeVisible();
});

test.describe("English layout", () => {
  test.use({ uiLocale: "en" });

  test("settings and journal reflow and support keyboard navigation", async ({ page, login }) => {
    await login();
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto("/server/settings");
    await expect(page.getByTestId("settings-interface")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(361);
    await page.goto("/journal");
    const tabs = page.getByRole("tablist");
    const logs = tabs.getByRole("tab").first();
    const actions = tabs.getByRole("tab").last();
    await logs.focus();
    await page.keyboard.press("ArrowRight");
    await expect(actions).toBeFocused();
    await expect(actions).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Home");
    await expect(logs).toBeFocused();
    await expect(logs).toHaveAttribute("aria-selected", "true");
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(361);
  });
});
