// Opt-in: real panel + real agent + the modified Telemt CLI (never starts proxy).
// TELEMT_CONFIG_E2E_BINARY may point to an installed binary or a local Docker
// wrapper preserving absolute scratch paths. Agent credentials are test-only.
import {expect,test} from "./fixtures";
import {spawn,type ChildProcess} from "node:child_process";
import {mkdtemp,readFile,rm,writeFile} from "node:fs/promises";
import {killAndWait,PANEL_BINARY} from "./stack";
import {MOCK_URL} from "./env";

const binary=process.env["TELEMT_CONFIG_E2E_BINARY"];
const agentURL="http://127.0.0.1:48194";
const agentID="config-editor-browser-agent-01";
const token="Bearer config-editor-browser-test-token-0123456789";
let scratch="", root="", agent:ChildProcess|undefined;
test.beforeAll(async()=>{
  if(!binary)return;
  scratch=await mkdtemp("/private/tmp/telemt-config-e2e-");
  root=scratch+"/telemt.toml";
  const config=scratch+"/agent.toml";
  await writeFile(config,`listen = "127.0.0.1:48194"
data_dir = "${scratch}/state"
[agent]
enabled = true
id = "${agentID}"
authorization_env = "TELEMT_CONFIG_E2E_AUTH"
config_path = "${root}"
config_binary = "${binary}"
[telemt]
url = "${MOCK_URL}"
[host]
service_manager = "none"
[privileges]
mode = "manual"
[store]
driver = "memory"
`);
  agent=spawn(PANEL_BINARY,["--config",config],{env:{...process.env,TELEMT_CONFIG_E2E_AUTH:token},stdio:"ignore"});
  await expect.poll(async()=>{
    if(agent?.exitCode!==null)throw Error("agent startup failed");
    try{return (await fetch(agentURL+"/v1/agent/health",{headers:{Authorization:token,"X-Telemt-Agent-ID":agentID}})).status;}catch{return 0;}
  },{timeout:15000}).toBe(200);
});
test.afterAll(async()=>{if(agent)await killAndWait(agent);if(scratch)await rm(scratch,{recursive:true,force:true});});

test("typed source edit, durable backup, conflict and recovery with real CLI",async({page,login},info)=>{
  test.skip(!binary,"requires TELEMT_CONFIG_E2E_BINARY and TELEMT_CONFIG_E2E_AUTH in panel environment");
  test.setTimeout(180000);
  const original='[general]\nlog_level = "normal" # keep this comment\n[access.users]\nalice = "0123456789abcdef0123456789abcdef"\n';
  await writeFile(root,original,{mode:0o600});
  await login();
  const created=await page.request.post("/api/nodes",{headers:{"Sec-Fetch-Site":"same-origin"},data:{name:"Files-"+info.project.name,api_url:MOCK_URL,enabled:true,agent_url:agentURL,agent_id:agentID,agent_auth_env:"TELEMT_CONFIG_E2E_AUTH"}});
  expect(created.status()).toBe(201);
  const node=await created.json() as {id:string;revision:number};
  const api=`/api/nodes/${node.id}/manage/telemt/config/files`;
  await page.goto(`/server/files?node=${node.id}`);
  await expect(page.getByRole("combobox",{name:"Исходный файл"})).toBeVisible({timeout:30000});
  const initial=await (await page.request.get(api)).json();
  const section=page.locator("details").filter({has:page.locator(":scope > summary",{hasText:/^general /})});
  await section.locator("summary").first().click();
  await section.getByRole("button",{name:"Изменить",exact:true}).click();
  const sheet=page.getByRole("dialog");
  await sheet.getByLabel("Значение",{exact:true}).fill("debug");
  await sheet.getByRole("button",{name:"Применить к черновику"}).click();
  await expect(sheet).toBeHidden({timeout:30000});
  await page.getByRole("button",{name:"Сравнить исходный файл и изменения",exact:true}).click();
  const comparison=page.locator("pre");
  await expect(comparison.first()).toContainText('log_level = "normal"');
  await expect(comparison.last()).toContainText('log_level = "debug"');
  const save=page.getByRole("button",{name:"Сохранить файл",exact:true});
  await expect(save).toBeDisabled();
  await page.getByRole("button",{name:"Проверить",exact:true}).click();
  await expect(save).toBeEnabled({timeout:30000});
  expect(await readFile(root,"utf8")).toBe(original);
  page.once("dialog",d=>d.accept());
  await save.click();
  await expect(page.getByRole("status")).toContainText("Файл сохранён",{timeout:45000});
  expect(await readFile(root,"utf8")).toBe(original.replace('"normal"','"debug"'));
  const stale=await page.request.post(api+"/validate",{headers:{"Sec-Fetch-Site":"same-origin"},data:{revision:initial.revision,source_id:initial.sources[0].id,content:original}});
  expect(stale.status()).toBe(409);
  await page.getByRole("button",{name:"Открыть для восстановления",exact:true}).first().click();
  await expect(page.getByRole("status")).toContainText("Копия открыта как черновик");
  await page.getByRole("button",{name:"Проверить",exact:true}).click();
  await expect(save).toBeEnabled({timeout:30000});
  page.once("dialog",d=>d.accept());
  await save.click();
  await expect(page.getByRole("status")).toContainText("Файл сохранён",{timeout:45000});
  expect(await readFile(root,"utf8")).toBe(original);
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
  await page.getByRole("button",{name:"Разделы и поля",exact:true}).click();
  await expect(section).toBeVisible({timeout:30000});
  await section.locator("summary").first().click();
  await page.screenshot({path:info.outputPath("full-config-editor.png"),fullPage:true});
  // Removing the registry record must not remove remote source files/backups.
  expect((await page.request.delete("/api/nodes/"+node.id,{headers:{"If-Match":String(node.revision),"Sec-Fetch-Site":"same-origin"}})).status()).toBe(204);
  expect(await readFile(root,"utf8")).toBe(original);
});
