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
    await expect(publicPage.locator("img.qr:visible")).toHaveCount(1);
    await expect(publicPage.getByRole("radio")).toHaveCount(0);

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

test("subscription preview switches the QR and actions together", async ({ page, login }) => {
  await login();
  await page.goto("/panel");
  await page.getByRole("button", { name: "Настроить страницу", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Страница подписки" });
  const preview = dialog.frameLocator('iframe[title="Предпросмотр"]');
  const picker = preview.getByRole("group", { name: "Вариант подключения" });
  const qr = picker.locator("img.qr:visible");
  const telegram = picker.getByRole("link", { name: "Подключить в Telegram" });
  const tls = picker.getByRole("radio", { name: /^TLS/ });
  await expect(tls).toBeChecked();
  await expect(qr).toHaveCount(1);
  await expect(telegram).toHaveAttribute("href", /[&?]secret=ee/);
  const tlsQR = await qr.evaluate((image: HTMLImageElement) => image.src);

  await tls.press("ArrowRight");
  await expect(picker.getByRole("radio", { name: "Secure" })).toBeChecked();
  await expect(qr).toHaveCount(1);
  await expect(qr).not.toHaveAttribute("src", tlsQR);
  await expect(telegram).toHaveAttribute("href", /[&?]secret=dd/);
  await picker.locator(".variant:visible summary").click();
  await expect(picker.locator("code:visible").last()).toHaveText("dd000102030405060708090a0b0c0d0e0f");

  await picker.locator("label").filter({ hasText: /^WEB$/ }).click();
  await expect(picker.getByRole("radio", { name: "WEB" })).toBeChecked();
  await expect(qr).toHaveCount(1);
  await expect(telegram).toHaveAttribute("href", /^tg:\/\/webproxy\?/);
  await expect(picker.getByRole("link", { name: "Открыть через t.me" })).toHaveCount(0);
  expect(await picker.evaluate((field) => field.scrollWidth - field.clientWidth)).toBeLessThanOrEqual(1);
});

test("uploaded logo is available on anonymous login and subscription pages", async ({ page, browser, login }) => {
  await login();
  const brandingEndpoint = "/api/settings/branding";
  const original = await (await page.request.get(brandingEndpoint)).json();
  const anonymous = await browser.newContext({ locale: "ru-RU" });
  try {
    const link = await (await page.request.get(`/api/users/${SEEDED_USER}/sublink`)).json();
    const visitor = await anonymous.newPage();
    const subscription = await anonymous.newPage();
    await page.goto("/panel");
    await page.getByRole("button", { name: "Настроить оформление", exact: true }).click();
    const form = page.getByRole("dialog", { name: "Оформление панели" });
    await form.getByLabel("Название панели", { exact: true }).fill("Панель с логотипом");
    await form.getByRole("combobox", { name: "Логотип" }).selectOption("upload");
    const save = form.getByRole("button", { name: "Сохранить оформление", exact: true });
    await expect(save).toBeDisabled();
    await form.getByLabel("Файл логотипа", { exact: true }).setInputFiles("src/assets/logo-menu.webp");
    await expect(form.getByText("logo-menu.webp", { exact: true })).toBeVisible();
    await save.click();
    await expect(form).not.toBeVisible();

    await visitor.goto(new URL("/login", page.url()).href);
    await expect(visitor.getByRole("heading", { name: "Панель с логотипом" })).toBeVisible();
    const loginLogo = visitor.locator('img[src*="/api/branding/logo"]');
    await expect.poll(() => loginLogo.evaluate((image: HTMLImageElement) => image.naturalWidth)).toBeGreaterThan(0);
    const icon = await page.request.get("/api/branding/icon");
    expect(icon.ok()).toBe(true);
    const embedded = "data:image/png;base64," + (await icon.body()).toString("base64");

    expect((await subscription.goto(link.url))?.status()).toBe(200);
    await expect(subscription.locator("img.page-logo")).toHaveAttribute("src", embedded);
    await page.getByRole("button", { name: "Настроить страницу", exact: true }).click();
    const preview = page.getByRole("dialog", { name: "Страница подписки" }).frameLocator('iframe[title="Предпросмотр"]');
    await expect(preview.locator("img.page-logo")).toHaveAttribute("src", embedded);
  } finally {
    await anonymous.close();
    const restored = await page.request.put(brandingEndpoint, {
      data: { title: original.title, logo_mode: original.logo_mode, logo_path: original.logo_path },
      headers: { "Sec-Fetch-Site": "same-origin" },
    });
    expect(restored.ok()).toBe(true);
  }
});
