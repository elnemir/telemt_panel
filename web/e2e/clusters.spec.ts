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

 await page.getByRole("button",{name:"Балансировка подключений",exact:true}).click();
 const routingSheet=page.getByRole("dialog",{name:"Балансировка подключений",exact:true});
 await expect(routingSheet.getByLabel("Порт WEB HTTPS",{exact:true})).toHaveValue("443");
 await expect(routingSheet.getByLabel("Порт WEB HTTPS",{exact:true})).toHaveAttribute("readonly", "");
 await expect(routingSheet.getByLabel("Порт Native / FakeTLS",{exact:true})).toHaveValue("8443");
 await routingSheet.getByLabel("Домен подключения",{exact:true}).fill("proxy.example.test");
 for(let i=0;i<2;i++){
  await routingSheet.getByLabel("Адрес Native на ноде (IP:порт)",{exact:true}).nth(i).fill(`192.0.2.${i+1}:443`);
  await routingSheet.getByLabel("Адрес WEB на ноде (IP:порт)",{exact:true}).nth(i).fill(`192.0.2.${i+1}:18080`);
 }
 await routingSheet.getByLabel("Вес ноды",{exact:true}).first().fill("25");
 await routingSheet.getByLabel("Не назначать новые подключения (drain)",{exact:true}).first().check();
 await routingSheet.getByRole("button",{name:"Сохранить",exact:true}).click();
 await expect(routingSheet.getByRole("status")).toContainText("Правила сохранены");
 const pendingDownload=page.waitForEvent("download");
 await routingSheet.getByRole("button",{name:"Скачать конфигурацию HAProxy",exact:true}).click();
 const downloaded=await pendingDownload;const downloadedPath=await downloaded.path();expect(downloadedPath).toBeTruthy();
 const exported=await readFile(downloadedPath!,"utf8");expect(exported).toContain("send-proxy-v2");expect(exported).toContain("weight 0");expect(exported).toContain("cookie TELEMT_INGRESS");
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
 expect(target.max_tcp_conns).toBe(7);const original=target.links.classic[0];
 const source=await(await page.request.get("/api/users/"+username)).json();
 expect(new URL(source.links.classic[0]).searchParams.get("secret")).toBe(new URL(original).searchParams.get("secret"));
 expect((await page.request.patch(userPath,{headers,data:{enabled:false}})).status()).toBe(409);
 await page.getByRole("region",{name:"Синхронизация",exact:true}).scrollIntoViewIfNeeded();
 await page.screenshot({path:info.outputPath("cluster-delivery.png"),fullPage:true});
 page.on("dialog",d=>void d.accept());
 await page.getByRole("button",{name:"Изменить пользователя",exact:true}).click();
 sheet=page.getByRole("dialog",{name:"Изменить пользователя",exact:true});
 await sheet.getByLabel("Заменить секрет на всех нодах",{exact:true}).check();
 await sheet.getByRole("button",{name:"Сохранить",exact:true}).click();await expect(sheet).not.toBeVisible();
 await expect.poll(async()=>{const data=await(await page.request.get(userPath)).json();return data.links.classic[0];}).not.toBe(original);
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
