import {expect,test} from "./fixtures";
import {createHash} from "node:crypto";
import {CUSTOMER_PAYMENT_PASSWORD2} from "./customer-fixture";
const headers={"Sec-Fetch-Site":"same-origin"};
const password="Customer-e2e-password-123!";
test("customer email proof, isolated invoices, lost-response recovery and verified payment",async({page,login},info)=>{
 test.skip(process.env["TELEMT_CUSTOMER_E2E"]!=="1","requires portal and local TLS SMTP fixture");test.setTimeout(90000);
 await login();
 const clusterResponse=await page.request.post("/api/clusters",{headers,data:{name:"Portal "+info.project.name}});expect(clusterResponse.status()).toBe(201);const cluster=await clusterResponse.json();
 const planResponse=await page.request.post("/api/billing/plans",{headers,data:{cluster_id:cluster.id,name:"Portal tariff "+info.project.name,enabled:true,terms:{currency:"RUB",currency_scale:2,price_minor:"9900",duration_seconds:"86400",quota_bytes:"1073741824",overrun_bytes:"1048576",node_policy:{enabled:true,max_tcp_conns:null,max_unique_ips:null,rate_limit_up_bps:null,rate_limit_down_bps:null,expiration_rfc3339:"",user_ad_tag:""}}}});expect(planResponse.status()).toBe(201);const plan=await planResponse.json();
 const accountResponse=await page.request.post("/api/billing/payment-accounts",{headers,data:{name:"Portal merchant "+info.project.name,provider:"robokassa",test:false,enabled:true,config:{merchant:"fixture",algorithm:"SHA256",password1_env:"TELEMT_PAYMENT_E2E_PASSWORD1",password2_env:"TELEMT_PAYMENT_E2E_PASSWORD2"}}});expect(accountResponse.status()).toBe(201);const merchant=await accountResponse.json();
 // Administrative and customer authentication are independent even in one browser.
 await page.goto("/portal");await expect(page.getByLabel("Email",{exact:true})).toBeVisible();
 expect((await page.request.get("/api/customer/auth/me")).status()).toBe(401);
 expect((await page.request.post("/api/auth/logout",{headers})).status()).toBe(204);
 const signup=async(email:string)=>{
  await page.goto("/portal");await page.getByRole("button",{name:"Зарегистрироваться",exact:true}).click();
  await page.getByLabel("Email",{exact:true}).fill(email);await page.getByLabel("Пароль",{exact:true}).fill(password);
  await page.getByRole("button",{name:"Отправить ссылку",exact:true}).click();await expect(page.getByRole("status")).toContainText("Запрос принят");
  let link="";
  await expect.poll(async()=>{const records=await(await page.request.get("http://127.0.0.1:48198/?to="+encodeURIComponent(email))).json();link=records.at(-1)?.link??"";return link;}).not.toBe("");
  await page.goto(link);await expect(page).toHaveURL(/\/portal$/);
  await page.getByRole("button",{name:"Подтвердить email",exact:true}).click();
  await expect(page.getByText(email,{exact:true})).toBeVisible();
 };
 const alice="alice-"+info.project.name+"@example.test";await signup(alice);
 expect((await page.request.get("/api/billing/invoices")).status()).toBe(401);
 await expect(page).toHaveURL(/\/portal$/); // A customer 401 must not trigger admin /login.
 await page.getByLabel("Тариф",{exact:true}).selectOption(plan.id);
 await page.getByLabel("Способ оплаты",{exact:true}).selectOption(merchant.id);
 let createdInvoice="";
 await page.route("**/api/customer/invoices",async route=>{
  if(route.request().method()!=="POST"){await route.continue();return;}
  const response=await route.fetch();expect(response.status()).toBe(201);createdInvoice=(await response.json()).id;
  await route.abort("failed"); // DB committed but the browser lost the reply.
 });
 await page.getByRole("button",{name:"Выставить счёт",exact:true}).click();
 await expect(page.getByRole("button",{name:"Повторить запрос",exact:true})).toBeEnabled();
 await page.unroute("**/api/customer/invoices");await page.reload();
 await page.getByRole("button",{name:"Повторить запрос",exact:true}).click();
 await expect(page.getByText("Счёт выставлен. Проверьте сумму и перейдите к оплате.",{exact:true})).toBeVisible();
 const invoices=await(await page.request.get("/api/customer/invoices")).json();expect(invoices).toHaveLength(1);expect(invoices[0].id).toBe(createdInvoice);
 const invoice=invoices[0];
 let providerForm="";
 await page.route("https://auth.robokassa.ru/Merchant/Index.aspx",async route=>{providerForm=route.request().postData()??"";await route.fulfill({contentType:"text/html",body:"<h1>Local checkout fixture</h1>"});});
 await page.getByRole("button",{name:"Перейти к оплате",exact:true}).click();
 await expect(page.getByRole("heading",{name:"Local checkout fixture"})).toBeVisible();
 const fields=new URLSearchParams(providerForm);expect(fields.get("InvId")).toBe(invoice.number);expect(fields.get("OutSum")).toBe("99.00");
 await page.goto("/portal");await expect(page.getByText("Ожидает оплаты",{exact:true})).toBeVisible();
 const signature=createHash("sha256").update(`99.00:${invoice.number}:${CUSTOMER_PAYMENT_PASSWORD2}`).digest("hex");
 for(let i=0;i<2;i++){const response=await page.request.post(`/api/payments/${merchant.id}/notify`,{form:{OutSum:"99.00",InvId:invoice.number,SignatureValue:signature}});expect(response.status()).toBe(200);expect(await response.text()).toBe("OK"+invoice.number);}
 await page.getByRole("button",{name:"Обновить",exact:true}).click();
 await expect(page.getByText("Оплачен",{exact:true})).toBeVisible();
 await page.getByLabel("Новая услуга или продление",{exact:true}).selectOption(invoice.subscription_id);
 await expect(page.getByText(/Оплачено до:/)).toBeVisible();
 await expect(page.getByText("Доступ ещё не выдан",{exact:true})).toBeVisible();
 const periods=await(await page.request.get(`/api/customer/subscriptions/${invoice.subscription_id}/periods`)).json();expect(periods).toHaveLength(1);
 // Display acceptance uses an explicit read-model fixture; runtime quota is
 // exercised separately against the real Rust process and platform worker.
 const servicePath=`/api/customer/subscriptions/${invoice.subscription_id}`;
 const originalDetail=await(await page.request.get(servicePath)).json();
 const fixtureLink="tg://proxy?server=proxy.example.test&port=443&secret=0123456789abcdef0123456789abcdef";
 await page.route("**"+servicePath,async route=>{
  const now=Math.floor(Date.now()/1000);
  await route.fulfill({json:{...originalDetail,access_status:"configured",usage_available:true,access:{status:"configured",nodes:[{node_id:"node-fixture",status:"configured",checked_at:now,budget_until:now+55,links:[]}],links:[{mode:"secure",url:fixtureLink}],usage:{period_id:periods[0].id,quota_bytes:"1073741824",overrun_bytes:"1048576",granted_bytes:"1048576",observed_bytes:"4096",unknown_bytes:"1044480",allocations:1,last_reported_at:now}}}});
 });
 await page.getByRole("button",{name:"Обновить",exact:true}).click();
 await expect(page.getByText("Доступ настроен",{exact:true})).toBeVisible();
 await expect(page.getByText(/Учтённый трафик: 4\s096 байт/)).toBeVisible();
 await expect(page.getByText(/Резерв и неподтверждённый расход: 1\s044\s480 байт/)).toBeVisible();
 await expect(page.getByRole("link",{name:"Подключиться · secure",exact:true})).toHaveAttribute("href",fixtureLink);

 await page.getByRole("heading",{name:"Мои услуги",exact:true}).scrollIntoViewIfNeeded();await page.screenshot({path:info.outputPath("customer-paid-period.png"),fullPage:true});
 await page.unroute("**"+servicePath);
 await login();
 let managedID="";
 await expect.poll(async()=>{const rows=await(await page.request.get(`/api/clusters/${cluster.id}/identities`)).json();managedID=rows[0]?.username??"";return rows[0]?.billing_managed;}).toBe(true);
 await page.goto("/clusters");await page.getByRole("button",{name:new RegExp("^Portal "+info.project.name+"\\b")}).click();
 await page.getByRole("button",{name:new RegExp(managedID)}).click();
 await page.getByRole("button",{name:"Изменить пользователя",exact:true}).click();
 const managedSheet=page.getByRole("dialog",{name:"Изменить пользователя",exact:true});
 await expect(managedSheet.getByLabel("Лимит TCP-соединений",{exact:true})).toBeDisabled();
 await expect(managedSheet.getByLabel("Заменить секрет на всех нодах",{exact:true})).toBeEnabled();
 await managedSheet.getByRole("button",{name:"Отмена",exact:true}).click();
 await page.goto("/portal");

 await page.getByRole("button",{name:"Выйти",exact:true}).click();
 await expect(page.getByLabel("Email",{exact:true})).toBeVisible();
 const bob="bob-"+info.project.name+"@example.test";await signup(bob);
 await expect(page.getByText("Счетов пока нет",{exact:true})).toBeVisible();
 expect((await page.request.get("/api/customer/invoices/"+invoice.id)).status()).toBe(404);
 expect((await page.request.get(`/api/customer/subscriptions/${invoice.subscription_id}/periods`)).status()).toBe(404);
 await expect(page.getByText(alice,{exact:true})).toHaveCount(0);
 await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth-innerWidth)).toBeLessThanOrEqual(1);
});
