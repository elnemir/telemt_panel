import { ADMIN_PASSWORD, BASE_URL } from "./env";
import { expect, LOCALE_STORAGE_KEY, test } from "./fixtures";

test("administrators can add accounts, rotate passwords, and revoke access", async ({ browser, page, login }, info) => {
  const username = `admin-${info.project.name}`;
  const firstPassword = "synthetic-browser-password";
  const nextPassword = "synthetic-browser-replacement";
  await login();
  await page.goto("/panel");
  const accounts = page.getByRole("region", { name: "Администраторы", exact: true });
  await accounts.getByRole("button", { name: "Добавить администратора" }).click();
  const form = page.getByRole("dialog");
  await form.getByLabel("Имя пользователя", { exact: true }).fill(username);
  await form.getByLabel("Ваш текущий пароль", { exact: true }).fill(ADMIN_PASSWORD);
  await form.getByLabel("Новый пароль", { exact: true }).fill(firstPassword);
  await form.getByLabel("Повторите новый пароль", { exact: true }).fill(nextPassword);
  await form.getByRole("button", { name: "Добавить администратора" }).click();
  await expect(form.getByRole("alert")).toHaveText("Пароли не совпадают.");
  await form.getByLabel("Повторите новый пароль", { exact: true }).fill(firstPassword);
  await form.getByRole("button", { name: "Добавить администратора" }).click();
  await expect(form).toBeHidden();
  await expect(accounts.getByText(username, { exact: true })).toBeVisible();

  const context = await browser.newContext({ baseURL: BASE_URL, viewport: page.viewportSize() });
  try {
    await context.addInitScript((key) => localStorage.setItem(key, "ru"), LOCALE_STORAGE_KEY);
    const managed = await context.newPage();
    async function signIn(password: string) {
      await managed.goto("/login");
      await managed.getByLabel("Имя пользователя", { exact: true }).fill(username);
      await managed.getByLabel("Пароль", { exact: true }).fill(password);
      await managed.getByRole("button", { name: "Войти", exact: true }).click();
      await expect(managed).toHaveURL(/\/people$/);
    }
    await signIn(firstPassword);
    await managed.goto("/panel");
    const managedAccounts = managed.getByRole("region", { name: "Администраторы", exact: true });
    await expect(managedAccounts.getByText("Вы", { exact: true })).toBeVisible();
    await expect(managed.getByRole("button", { name: "Добавить passkey", exact: true })).toHaveCount(0);
    await expect(managedAccounts.getByRole("button", { name: `Удалить ${username}`, exact: true })).toHaveCount(0);
    await managedAccounts.getByRole("button", { name: "Сменить пароль" }).click();
    const change = managed.getByRole("dialog");
    await change.getByLabel("Ваш текущий пароль", { exact: true }).fill(firstPassword);
    await change.getByLabel("Новый пароль", { exact: true }).fill(nextPassword);
    await change.getByLabel("Повторите новый пароль", { exact: true }).fill(nextPassword);
    await change.getByRole("button", { name: "Сменить пароль" }).click();
    await expect(managed).toHaveURL(/\/login$/);
    await signIn(nextPassword);

    await accounts.getByRole("button", { name: `Удалить ${username}`, exact: true }).click();
    await expect(form).toContainText(username);
    await form.getByLabel("Ваш текущий пароль", { exact: true }).fill(ADMIN_PASSWORD);
    await form.getByRole("button", { name: "Удалить", exact: true }).click();
    await expect(form).toBeHidden();
    await expect(accounts.getByText(username, { exact: true })).toHaveCount(0);
    await managed.goto("/panel");
    await expect(managed).toHaveURL(/\/login(?:\?|$)/);
  } finally {
    await context.close();
  }
});
