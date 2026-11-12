import { expect, test } from "./fixtures";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { killAndWait } from "./stack";

let scratch = "";
const processes: ChildProcess[] = [];
const urls = ["http://127.0.0.1:48192", "http://127.0.0.1:48193"];
test.beforeAll(async () => {
  scratch = await mkdtemp(path.join(tmpdir(), "telemt-nodes-e2e-"));
  const binary = path.join(scratch, "mock");
  const root = fileURLToPath(new URL("../../", import.meta.url));
  execFileSync("go", ["build", "-o", binary, "./cmd/telemt-mock"], {cwd:root});
  for (const url of urls) {
    const proc = spawn(binary,["-listen", new URL(url).host,"-scenario","full","-telemt-version","3.5.14"],{stdio:"ignore"});
    processes.push(proc);
    await expect.poll(async () => {if(proc.exitCode!==null)throw Error("mock startup failed");try{return (await fetch(url+"/v1/health")).ok;}catch{return false;}}).toBe(true);
  }
});
test.afterAll(async () => {await Promise.all(processes.map(killAndWait));if(scratch)await rm(scratch,{recursive:true,force:true});});

test("registry UI, document selection, isolated users/SSE and TOML save", async ({page, login}, info) => {
  test.setTimeout(90_000);
  await login();
  const names=[`A-${info.project.name}`,`B-${info.project.name}`];
  for(let i=0;i<2;i++) {
    await page.getByRole("button",{name:"Добавить ноду",exact:true}).click();
    const dialog=page.getByRole("dialog");
    if (i === 0) {
      const guide = dialog.locator("details").filter({ hasText: "Как настроить Telemt для подключения" });
      await expect(guide.locator("code").first()).toBeHidden();
      await guide.locator("summary").click();
      await expect(guide.locator("code").first()).toContainText('[server.api]');
      await expect(guide.locator("code").first()).toContainText('whitelist = ["10.42.0.10/32"]');
      await expect(guide.locator("code").last()).toContainText('TELEMT_NODE_A_AUTH="Bearer REPLACE_WITH_RANDOM_TOKEN"');
      await expect(guide.getByRole("button", { name: "Скопировать TOML", exact: true })).toBeVisible();
      await expect(dialog.getByLabel("Адрес API", { exact: true })).toHaveValue("");
      await guide.locator("summary").click();

      const agent = dialog.getByRole("group", { name: "Агент управления хостом (необязательно)" });
      const agentAddress = agent.getByLabel("Адрес агента", { exact: true });
      const agentIdentity = agent.getByLabel("Идентификатор агента", { exact: true });
      const agentEnv = agent.getByLabel("Переменная окружения для Authorization агента", { exact: true });
      await expect(agent).toContainText("telemt-agent — отдельная служба на сервере Telemt");
      await expect(agentAddress).not.toHaveAttribute("required");
      await agentIdentity.fill("telemt-agent-example_01");
      await expect(agentAddress).toHaveAttribute("required", "");
      await expect(agentEnv).toHaveAttribute("required", "");
      expect(await agentIdentity.evaluate((input: HTMLInputElement) => input.checkValidity())).toBe(true);
      await agentIdentity.clear();
      await expect(agentAddress).not.toHaveAttribute("required");
    }
    await dialog.getByLabel("Название",{exact:true}).fill(names[i]!);
    await dialog.getByLabel("Адрес API",{exact:true}).fill(urls[i]!);
    await dialog.getByRole("button",{name:"Сохранить",exact:true}).click();
    await expect(dialog).toBeHidden({timeout:15_000});
  }
  const registry=await (await page.request.get("/api/nodes")).json() as {nodes:Array<{id:string;name:string}>};
  const a=registry.nodes.find(n=>n.name===names[0])!.id;
  const b=registry.nodes.find(n=>n.name===names[1])!.id;
  const selector=()=>page.getByRole("combobox",{name:"Нода Telemt",exact:true});
  const select=async(id:string)=>{
    page.once("dialog",dialog=>dialog.accept());
    await selector().selectOption(id);
    await expect(page).toHaveURL(new RegExp("node="+id));
    await expect(selector()).toHaveValue(id);
    await expect(selector()).toBeInViewport();
    expect((await selector().boundingBox())!.width).toBeGreaterThan(140);
  };
  await select(a);
  await expect(page.getByTestId("user-card-alice")).toBeVisible();
  const mutationPaths:string[]=[];
  page.on("request",req=>{if(["POST","PUT","PATCH","DELETE"].includes(req.method()))mutationPaths.push(new URL(req.url()).pathname);});
  const username="isolation_"+info.project.name;
  await page.getByRole("button",{name:"Создать",exact:true}).click();
  await page.getByTestId("user-form-username").fill(username);
  await page.getByTestId("user-form-submit").click();
  await expect(page.getByText("Пользователь создан",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"Готово",exact:true}).click();
  await page.getByRole("link",{name:"Назад",exact:true}).click();
  await expect(page.getByTestId(`user-card-${username}`)).toBeVisible();
  expect(new URL(page.url()).searchParams.get("node")).toBe(a);
  await select(b);
  await expect(page.getByTestId("user-card-alice")).toBeVisible();
  await expect(page.getByTestId(`user-card-${username}`)).toHaveCount(0);
  expect((await page.request.get(`/api/nodes/${b}/manage/users/${username}`)).status()).toBe(404);
  expect((await page.request.get(`/api/users/${username}`)).status()).toBe(404);
  await page.goto(`/server/config?node=${a}`);
  await page.getByRole("tab",{name:"TOML",exact:true}).click();
  const phone=info.project.name==="mobile";
  if(phone)await page.getByRole("button",{name:"Открыть редактор",exact:true}).first().click();
  const editor=page.locator('.cm-content[contenteditable="true"]').last();
  await expect(editor).toBeVisible();
  const config=await (await page.request.get(`/api/nodes/${a}/manage/telemt/config/toml`)).json() as {toml_projection:string};
  await editor.fill(config.toml_projection.replace(/log_level\s*=\s*"[^"]*"/,'log_level = "debug"'));
  const preview=page.waitForResponse(r=>r.url().includes(`/api/nodes/${a}/manage/telemt/config/toml/preview`));
  await page.getByRole("button",{name:phone?"Готово и проверить":"Проверить",exact:true}).click();
  expect((await preview).ok()).toBeTruthy();
  const save=page.getByRole("button",{name:"Сохранить проверенное",exact:true});
  await expect(save).toBeEnabled();
  const saved=page.waitForResponse(r=>r.url().includes(`/api/nodes/${a}/manage/telemt/config/toml`)&&r.request().method()==="PATCH");
  await save.click();expect((await saved).ok()).toBeTruthy();
  expect(mutationPaths.length).toBeGreaterThan(1);
  expect(mutationPaths.every(p=>p.startsWith(`/api/nodes/${a}/manage/`))).toBe(true);
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
  await page.screenshot({path:info.outputPath("node-config.png"),fullPage:true});
  // A second tab/document has its own target; it cannot change the first tab.
  const other=await page.context().newPage();
  await other.goto(`/people?node=${b}`);
  await expect(other.getByTestId("user-card-alice")).toBeVisible();
  await expect(selector()).toHaveValue(a);
  await other.close();
  await page.goto(`/people?node=${b}`);
  await page.getByRole("button",{name:"Настроить",exact:true}).click();
  const settings=page.getByRole("dialog");
  await settings.getByLabel("Включена",{exact:true}).uncheck();
  await settings.getByRole("button",{name:"Сохранить",exact:true}).click();
  await expect(settings).toBeHidden({timeout:15_000});
  await expect(page.getByRole("alert")).toContainText("Выбранная нода недоступна");
  await expect(selector()).toBeInViewport();
  await expect(page.getByTestId("user-card-alice")).toHaveCount(0);
  await page.screenshot({path:info.outputPath("node-disabled.png"),fullPage:true});
  // Delete only registry records; this also retires their collectors before
  // the next project starts fresh mock processes on the same test ports.
  for(const id of [b,a]) {
    if(id!==b)await select(id);
    await page.getByRole("button",{name:"Настроить",exact:true}).click();
    page.once("dialog",dialog=>dialog.accept());
    await page.getByRole("dialog").getByRole("button",{name:"Удалить ноду",exact:true}).click();
    await expect(page.getByRole("dialog")).toBeHidden({timeout:15_000});
    await expect(page.getByRole("alert")).toContainText("Выбранная нода недоступна");
    expect(new URL(page.url()).searchParams.get("node")).toBe(id);
    await expect(selector()).toBeInViewport();
  }
});

test("SSH installer checks the host before confirming and tracks registry admission", async ({ page, login }) => {
  await page.route("**/api/nodes", async route => {
    const response = await route.fetch();
    await route.fulfill({ response, json: { ...await response.json(), ssh_install_available: true } });
  });
  await login();
  let starts = 0;
  let checks = 0;
  const id = "11111111111111111111111111111111";
  const input = {
    name: "Fresh node", host: "192.0.2.20", ssh_port: 22, ssh_user: "root",
    host_fingerprint: "SHA256:" + "a".repeat(43), proxy_port: 443,
    management_port: 9443, with_agent: true,
  };
  const plan = { ...input, architecture: "x86_64", panel_peer: "192.0.2.10", release: "3.5.17", api_url: "https://192.0.2.20:9443" };
  await page.route("**/api/node-installs/preview", async route => {
    expect(route.request().postDataJSON()).toEqual(input);
    await route.fulfill({ json: { id, plan, state: "prepared" } });
  });
  await page.route("**/api/node-installs/" + id + "/apply", async route => {
    starts++;
    await route.fulfill({ status: 202, json: { id, plan, state: "checking" } });
  });
  await page.route("**/api/node-installs/" + id, async route => {
    const state = starts ? (++checks > 1 ? "installed" : "checking") : "prepared";
    await route.fulfill({ json: { id, plan, state, ...(state === "installed" ? { node_id: "n_new" } : {}) } });
  });
  await page.getByRole("button", { name: "Установить по SSH", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Установить по SSH" });
  await dialog.getByLabel("Название", { exact: true }).fill(input.name);
  await dialog.getByLabel("IP-адрес сервера", { exact: true }).fill(input.host);
  await dialog.getByLabel("Отпечаток ED25519 ключа SSH-хоста", { exact: true }).fill(input.host_fingerprint);
  await expect(dialog.getByRole("checkbox", { name: /Установить telemt-agent/ })).toBeChecked();
  await dialog.getByRole("button", { name: "Проверить хост", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText("План установки");
  await expect(dialog.getByText("Настройки доступны через Control API.", { exact: false })).toBeVisible();
  expect(starts).toBe(0);
  await dialog.getByRole("button", { name: "Начать установку", exact: true }).click();
  await expect(dialog.getByRole("status")).toHaveText("Нода добавлена");
  expect(starts).toBe(1);
  await dialog.getByRole("button", { name: "Готово", exact: true }).click();
  await expect(dialog).toBeHidden();
});

test("SSH update screen follows the selected node and reports rollback", async ({ page, login }, info) => {
  await login();
  const name = "Upgrade-" + info.project.name;
  const headers = { "Sec-Fetch-Site": "same-origin" };
  const created = await page.request.post("/api/nodes", {
    headers, data: { name, api_url: urls[0], enabled: true },
  });
  expect(created.status()).toBe(201);
  const node = await created.json() as { id: string; revision: number };
  const route = `/api/nodes/${node.id}/upgrade`;
  const screen = `/server/updates?node=${node.id}`;
  try {
    await page.goto(screen);
    await expect(page.getByText("Нужны активная управляемая нода с агентом и установочный SSH-ключ в настройках панели.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Запустить обновление" })).toHaveCount(0);

    // The SSH/systemd transaction is exercised separately in Linux.
    // Inject just its statuses to test the actual form, selected node and polling.
    const versions = { available: true, telemt_version: "3.5.17", agent_version: "1.0.0" };
    let requestID = "";
    const fingerprint = "SHA256:" + "abc+/".repeat(8) + "abc";
    await page.route("**" + route, async intercepted => {
      if (intercepted.request().method() === "POST") {
        const body = intercepted.request().postDataJSON();
        expect(body).toMatchObject({ ssh_port: 22, ssh_user: "root", host_fingerprint: fingerprint, telemt: true, agent: true });
        expect(body.request_id).toMatch(/^[a-f0-9]{32}$/);
        requestID = body.request_id;
        await intercepted.fulfill({ status: 202, json: { ...versions, job: { state: "checking", request_id: requestID } } });
      } else {
        await intercepted.fulfill({
          json: requestID ? { ...versions, job: { state: "rolled_back", request_id: requestID, code: "node_upgrade_verify" } } : versions,
        });
      }
    });
    await page.reload();
    const key = page.getByLabel("Отпечаток ED25519 ключа SSH-хоста", { exact: true });
    const submit = page.getByRole("button", { name: "Запустить обновление", exact: true });
    await key.fill("SHA256:too-short");
    await submit.click();
    expect(requestID).toBe("");
    expect(await key.evaluate((input: HTMLInputElement) => input.checkValidity())).toBe(false);
    await key.fill(fingerprint);
    expect(await key.evaluate((input: HTMLInputElement) => input.checkValidity())).toBe(true);
    await submit.click();
    await expect(page.getByText("Обновление не прошло. Прежние файлы восстановлены, оба API отвечают")).toBeVisible();
    await expect(page.getByText("Проверка версий, API или конфигурации после обновления не прошла.")).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1);
  } finally {
    const removed = await page.request.delete(`/api/nodes/${node.id}`, {
      headers: { ...headers, "If-Match": String(node.revision) },
    });
    expect(removed.status()).toBe(204);
  }
});
