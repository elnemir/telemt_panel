import { expect, test } from "./fixtures";
import { ADMIN_PASSWORD, ADMIN_USERNAME } from "./env";

test("Panel is a global destination in both navigation layouts", async ({ page, login }) => {
  await login();
  const viewport = page.viewportSize();
  if (viewport && viewport.width >= 1180) {
    await page.getByTestId("full-sidebar").getByRole("link", { name: "Панель", exact: true }).click();
    await expect(page.getByTestId("full-sidebar").getByRole("link", { name: "Панель", exact: true })).toHaveAttribute("aria-current", "page");
  } else {
    await page.getByRole("button", { name: "Ещё", exact: true }).click();
    await page.getByRole("menuitem", { name: "Панель", exact: true }).click();
  }
  await expect(page).toHaveURL(/\/panel\/?$/);
  await expect(page.getByRole("heading", { name: "Панель", level: 1 })).toBeVisible();
  await expect(page.getByRole("region", { name: "Конфигурация панели" })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Нода Telemt" })).toHaveCount(0);
  await expect(page.getByTestId("settings-storage")).toHaveCount(0);

  await page.goto("/server/settings?node=missing#geoip");
  await expect(page).toHaveURL(/\/panel\/?\?node=missing#geoip$/);
  await expect(page.locator("#geoip")).toBeVisible();
  await expect(page.getByRole("region", { name: "Конфигурация панели" })).toBeVisible();
  await page.getByRole("link", { name: "Обновления панели", exact: true }).click();
  if (process.env["CONTROL_PLANE_ONLY"] === "1") {
    await expect(page.getByText("На этой установке обновление панели выполняется на её хосте.")).toBeVisible();
  } else {
    await expect(page.getByRole("link", { name: "Открыть локальную ноду" })).toHaveAttribute("href", /\/panel\/updates\?node=default$/);
  }
});

test("node settings and update targets keep their own destinations", async ({ page, login }) => {
  test.skip(process.env["CONTROL_PLANE_ONLY"] === "1", "requires the local default node");
  await login();
  await page.goto("/server/settings#storage");
  await expect(page).toHaveURL(/\/server\/node-settings#storage$/);
  await expect(page.getByTestId("settings-storage")).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Нода Telemt" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Администраторы" })).toHaveCount(0);

  await page.goto("/server/updates");
  await expect(page.locator('[data-update-target="telemt"]')).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('[data-update-target="panel"]')).toHaveCount(0);
  await page.getByRole("link", { name: "Автообновление", exact: true }).click();
  await expect(page).toHaveURL(/\/panel\/updates#auto-update$/);
  await expect(page.locator('[data-update-target="panel"]')).toBeVisible();
  await expect(page.locator('[data-update-target="telemt"]')).toHaveCount(0);
});


test("a signed-out visitor returns to the original settings anchor", async ({ page }) => {
  await page.goto("/server/settings#storage");
  await page.getByLabel("Имя пользователя").fill(ADMIN_USERNAME);
  await page.getByLabel("Пароль").fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(page).toHaveURL(/\/server\/node-settings#storage$/);
});
