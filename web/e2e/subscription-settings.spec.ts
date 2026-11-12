import { test, expect } from "./fixtures";
import { SEEDED_USER } from "./env";
import type { SubscriptionAppearanceSnapshot } from "../src/lib/api/generated/types.gen";

test("customize a subscription and preview the draft before publishing", async ({ page, browser, login }) => {
  await login();
  const endpoint = "/api/settings/subscription";
  const initial = await page.request.get(endpoint);
  expect(initial.ok()).toBe(true);
  const original = await initial.json() as SubscriptionAppearanceSnapshot;
  const anonymous = await browser.newContext({ locale: "ru-RU" });
  try {
    const link = await (await page.request.get(`/api/users/${SEEDED_USER}/sublink`)).json();
    const publicPage = await anonymous.newPage();
    expect((await publicPage.goto(link.url))?.status()).toBe(200);
    await expect(publicPage.getByRole("heading", { level: 1 })).toContainText(SEEDED_USER);

    await page.goto("/panel");
    await page.getByRole("button", { name: "Настроить страницу", exact: true }).click();
    const form = page.getByRole("dialog", { name: "Страница подписки" });
    await form.getByRole("textbox", { name: "Заголовок" }).fill("Прокси моей команды");
    await form.getByRole("combobox", { name: "Цвет акцента" }).selectOption("violet");
    await form.getByRole("checkbox", { name: "QR-коды", exact: true }).uncheck();
    await form.getByRole("textbox", { name: "Адрес поддержки" }).fill("http://example.test/support");
    await expect(form.getByRole("button", { name: "Сохранить страницу" })).toBeDisabled();
    await form.getByRole("textbox", { name: "Адрес поддержки" }).fill("https://example.test/support");
    await form.getByRole("combobox", { name: "Состояние подписки" }).selectOption("expired");
    const preview = form.frameLocator('iframe[title="Предпросмотр"]');
    await expect(preview.getByRole("heading", { level: 1 })).toHaveText("Прокси моей команды");
    await expect(preview.locator(".status-expired")).toBeVisible();
    await expect(preview.locator("img.qr")).toHaveCount(0);
    await expect(publicPage.getByRole("heading", { level: 1 })).toContainText(SEEDED_USER);
    expect(await form.evaluate((dialog) => dialog.scrollWidth - dialog.clientWidth)).toBeLessThanOrEqual(1);

    await form.getByRole("button", { name: "Сохранить страницу" }).click();
    await expect(form).not.toBeVisible();
    await publicPage.reload();
    await expect(publicPage.getByRole("heading", { level: 1 })).toHaveText("Прокси моей команды");
    await expect(publicPage.getByRole("link", { name: "Поддержка" })).toHaveAttribute("href", "https://example.test/support");
    await expect(publicPage.locator("img.qr")).toHaveCount(0);
    expect((await anonymous.request.get(new URL(endpoint, link.url).href)).status()).toBe(404);

    await page.reload();
    await page.getByRole("button", { name: "Настроить страницу", exact: true }).click();
    await expect(form.getByRole("textbox", { name: "Заголовок" })).toHaveValue("Прокси моей команды");
    await form.getByRole("button", { name: "Вернуть стандартное" }).click();
    await form.getByRole("button", { name: "Сохранить страницу" }).click();
    await expect(form).not.toBeVisible();
    await publicPage.reload();
    await expect(publicPage.getByRole("heading", { level: 1 })).toContainText(SEEDED_USER);
  } finally {
    await anonymous.close();
    const latest = await (await page.request.get(endpoint)).json() as SubscriptionAppearanceSnapshot;
    const restored = await page.request.put(endpoint, {
      data: { settings: original.settings, revision: latest.revision },
      headers: { "Sec-Fetch-Site": "same-origin" },
    });
    expect(restored.ok()).toBe(true);
  }
});
