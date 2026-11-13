import {expect,test} from "./fixtures";
import {execFileSync,spawn,type ChildProcess} from "node:child_process";
import {mkdtemp,rm,readFile} from "node:fs/promises";
import {fileURLToPath} from "node:url";
import {killAndWait} from "./stack";
let scratch="",mock:ChildProcess|undefined;
const url="http://127.0.0.1:48196",headers={"Sec-Fetch-Site":"same-origin"};
test.beforeAll(async()=>{
 if(process.env["TELEMT_PLATFORM_E2E"]!=="1")return;
 scratch=await mkdtemp("/private/tmp/telemt-clusters-e2e-");
 execFileSync("go",["build","-o",scratch+"/mock","./cmd/telemt-mock"],{cwd:fileURLToPath(new URL("../../",import.meta.url))});
 mock=spawn(scratch+"/mock",["-listen","127.0.0.1:48196","-scenario","full"],{stdio:"ignore"});
 await expect.poll(async()=>{try{return(await fetch(url+"/v1/health")).ok;}catch{return false;}}).toBe(true);
});
test.afterAll(async()=>{if(mock)await killAndWait(mock);if(scratch)await rm(scratch,{recursive:true,force:true});});
test("cluster membership, identity delivery, guarded node writes, rotation and removal",async({page,login},info)=>{
 test.skip(process.env["TELEMT_PLATFORM_E2E"]!=="1"||process.env["CONTROL_PLANE_ONLY"]==="1","requires platform and default mock");
 test.setTimeout(90000);await login();
 const name="Cluster "+info.project.name,username="cluster-"+info.project.name;
 const res=await page.request.post("/api/nodes",{headers,data:{name:"Cluster target "+info.project.name,api_url:url,enabled:true}});
 expect(res.status()).toBe(201);const node=await res.json();
 await page.goto("/clusters");
 await page.getByLabel("Название кластера",{exact:true}).fill(name);
 await page.getByRole("button",{name:"Создать кластер",exact:true}).click();
 await expect(page.getByRole("heading",{name,exact:true})).toBeVisible();
 for(const id of ["default",node.id]){
  await page.getByLabel("Нода",{exact:true}).selectOption(id);
  await page.getByRole("button",{name:"Добавить в кластер",exact:true}).click();
  await expect(page.getByLabel("Нода",{exact:true})).toBeEnabled();
  await expect(page.getByLabel("Нода",{exact:true})).toHaveValue("");
 }

 await page.getByRole("button",{name:"Создать шаблон",exact:true}).click();
 const configuration=page.getByRole("dialog",{name:"Общие настройки нод",exact:true});
 await configuration.getByLabel("Нода-источник",{exact:true}).selectOption(node.id);
 await configuration.getByRole("checkbox",{name:"Таймаут рукопожатия, с",exact:true}).check();
 await configuration.getByRole("button",{name:"Предпросмотр",exact:true}).click();
 await expect(configuration.getByRole("definition").filter({hasText:"10"})).toBeVisible();
 await configuration.getByRole("button",{name:"Сохранить и применить ко всем нодам",exact:true}).click();
 await expect(configuration).not.toBeVisible();
 const shared=page.getByRole("region",{name:"Общие настройки нод",exact:true});
 await expect(shared.getByText("Таймаут рукопожатия, с",{exact:true})).toBeVisible();
 await expect(shared.getByRole("listitem")).toHaveCount(2);

 await page.route("**/routing/suggestions",route=>route.fulfill({json:[
  {node_id:"default",native:["192.0.2.1:51974"],web:["192.0.2.1:18080"],status:"read"},
  {node_id:node.id,native:["192.0.2.2:35643"],web:["192.0.2.2:18080"],status:"read"},
 ]}));
 let ingressJob: {revision:number; host:string; state:string; max_connections?:number} | undefined;
 await page.route("**/routing/install",async route=>{
  const request=route.request();
  if(request.method()==="GET"){
   await route.fulfill({json:{available:true,job:ingressJob}});return;
  }
  const revision=Number(request.headers()["if-match"]);
  const path=new URL(request.url()).pathname.replace(/\/install$/,"");
  const saved=await(await page.request.get(path)).json();
  expect(revision).toBe(saved.revision);
  expect(request.postDataJSON()).toMatchObject({
   host:"192.0.2.10",ssh_port:22,ssh_user:"root",host_fingerprint:"SHA256:"+"A".repeat(43),
   request_id:expect.stringMatching(/^[a-f0-9]{32}$/),
  });
  ingressJob={revision,host:"192.0.2.10",state:"validating"};
  await route.fulfill({status:202,json:ingressJob});
 });
 await page.getByRole("button",{name:"Балансировка подключений",exact:true}).click();
 const routingSheet=page.getByRole("dialog",{name:"Балансировка подключений",exact:true});
 await expect(routingSheet.getByLabel("Включить WEB по HTTPS",{exact:true})).not.toBeChecked();
 await expect(routingSheet.getByLabel("Порт WEB HTTPS",{exact:true})).toHaveCount(0);
 await routingSheet.getByLabel("Включить WEB по HTTPS",{exact:true}).check();
 await expect(routingSheet.getByLabel("Порт WEB HTTPS",{exact:true})).toHaveValue("443");
 await expect(routingSheet.getByLabel("Порт WEB HTTPS",{exact:true})).toHaveAttribute("readonly", "");
 await expect(routingSheet.getByLabel("Порт Native / FakeTLS",{exact:true})).toHaveValue("8443");
 await routingSheet.getByLabel("Домен подключения",{exact:true}).fill("proxy.example.test");
 await expect(routingSheet.getByLabel("Адрес Native на ноде (IP:порт)",{exact:true}).nth(0)).toHaveValue("192.0.2.1:51974");
 await expect(routingSheet.getByLabel("Адрес Native на ноде (IP:порт)",{exact:true}).nth(1)).toHaveValue("192.0.2.2:35643");
 await expect(routingSheet.getByLabel("Адрес WEB на ноде (IP:порт)",{exact:true}).nth(1)).toHaveValue("192.0.2.2:18080");
 await routingSheet.getByLabel("Вес ноды",{exact:true}).first().fill("25");
 await routingSheet.getByLabel("Не назначать новые подключения (drain)",{exact:true}).first().check();
 await routingSheet.getByRole("button",{name:"Сохранить",exact:true}).click();
 await expect(routingSheet.getByText("Правила сохранены.",{exact:false})).toBeVisible();
 const pendingDownload=page.waitForEvent("download");
 await routingSheet.getByRole("button",{name:"Скачать конфигурацию HAProxy",exact:true}).click();
 const downloaded=await pendingDownload;const downloadedPath=await downloaded.path();expect(downloadedPath).toBeTruthy();
 const exported=await readFile(downloadedPath!,"utf8");expect(exported).toContain("send-proxy-v2");expect(exported).toContain("weight 0");expect(exported).toContain("cookie TELEMT_INGRESS");
 await routingSheet.getByLabel("IP-адрес балансировщика",{exact:true}).fill("192.0.2.10");
 await routingSheet.getByLabel("Отпечаток ED25519 ключа хоста",{exact:true}).fill("SHA256:"+"A".repeat(43));
 await routingSheet.getByRole("button",{name:"Применить по SSH",exact:true}).click();
 await expect(routingSheet.getByText("Проверяем конфигурацию…",{exact:true})).toBeVisible();
 await expect(routingSheet.getByRole("button",{name:"Применить по SSH",exact:true})).toBeDisabled();
 ingressJob={...ingressJob!,state:"applied",max_connections:4096};
 await expect(routingSheet.getByText("Конфигурация применена. HAProxy работает.",{exact:true})).toBeVisible();
 await expect(routingSheet.getByText("Лимит HAProxy на один рабочий процесс — 4096 соединений.",{exact:true})).toBeVisible();
 await routingSheet.screenshot({path:info.outputPath("routing-editor.png")});
 await routingSheet.getByRole("button",{name:"Отмена",exact:true}).click();
 await page.getByRole("button",{name:"Создать пользователя",exact:true}).click();
 let sheet=page.getByRole("dialog",{name:"Создать пользователя",exact:true});
 await sheet.getByLabel("Имя пользователя",{exact:true}).fill(username);
 await sheet.getByLabel("Лимит TCP-соединений",{exact:true}).fill("7");
 await sheet.getByLabel("Секрет пользователя (необязательно)",{exact:false}).fill("0123456789abcdef0123456789abcdef");
 await sheet.getByRole("button",{name:"Сохранить",exact:true}).click();
 await expect(sheet).not.toBeVisible();
 await expect(page.getByText("Сохранено и проверено",{exact:true})).toHaveCount(2,{timeout:15000});
 const userPath=`/api/nodes/${node.id}/manage/users/${username}`;
 const target=await(await page.request.get(userPath)).json();
 expect(target.max_tcp_conns).toBe(7);
 expect(target.cluster_managed).toBe(true);
 // The browser mocked installation, so no applied endpoint is recorded.
 expect(target.links.classic).toEqual([]);
 await expect(page.getByText("Ссылка появится после применения маршрута кластера и подтверждения подключения пользователя на ноде.")).toBeVisible();
 expect((await page.request.patch(userPath,{headers,data:{enabled:false}})).status()).toBe(409);
 await page.getByRole("region",{name:"Синхронизация",exact:true}).scrollIntoViewIfNeeded();
 await page.screenshot({path:info.outputPath("cluster-delivery.png"),fullPage:true});
 page.on("dialog",d=>void d.accept());
 await page.getByRole("button",{name:"Изменить пользователя",exact:true}).click();
 sheet=page.getByRole("dialog",{name:"Изменить пользователя",exact:true});
 await sheet.getByLabel("Заменить секрет на всех нодах",{exact:true}).check();
 await sheet.getByRole("button",{name:"Сохранить",exact:true}).click();await expect(sheet).not.toBeVisible();
 await expect(page.getByText("Сохранено и проверено",{exact:true})).toHaveCount(2,{timeout:15000});
 await page.getByRole("button",{name:"Изменить пользователя",exact:true}).click();
 sheet=page.getByRole("dialog",{name:"Изменить пользователя",exact:true});
 await sheet.getByLabel("Безвозвратно отозвать учётную запись",{exact:true}).check();
 await sheet.getByRole("button",{name:"Сохранить",exact:true}).click();await expect(sheet).not.toBeVisible();
 await expect.poll(async()=>{const data=await(await page.request.get(userPath)).json();return data.enabled;}).toBe(false);
 await expect(page.getByRole("button",{name:"Изменить пользователя",exact:true})).toBeDisabled();
 await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
 // Retirement must finish on both nodes before registry deletion is allowed.
 const clusters=await(await page.request.get("/api/clusters")).json();const cluster=clusters.find((c:{name:string})=>c.name===name);
 for(const id of [node.id,"default"]){
  const detail=await(await page.request.get(`/api/clusters/${cluster.id}`)).json();
  expect((await page.request.delete(`/api/clusters/${cluster.id}/members/${id}`,{headers:{...headers,"If-Match":String(detail.cluster.revision)}})).status()).toBe(202);
  await expect.poll(async()=>{const d=await(await page.request.get(`/api/clusters/${cluster.id}`)).json();return d.members.some((m:{node_id:string})=>m.node_id===id);}).toBe(false);
 }
 expect((await page.request.delete(`/api/nodes/${node.id}`,{headers:{...headers,"If-Match":String(node.revision)}})).status()).toBe(204);
});
