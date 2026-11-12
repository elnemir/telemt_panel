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
