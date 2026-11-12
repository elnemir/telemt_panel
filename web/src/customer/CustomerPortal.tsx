import { PageHeader } from "../ui/PageHeader";
import {useEffect,useState} from "react";
import {useMutation,useQuery,useQueryClient} from "@tanstack/react-query";
import {useStrings} from "../i18n";
import {apiErrorCode,apiErrorMessage} from "../people/apiError";
import {Button} from "../ui/Button";
import {Input} from "../ui/Input";
import {Select} from "../ui/Select";
import {getCustomerAuthMethods,getCustomerMe,customerLogin,customerLogout,customerRegister,customerRecover,completeCustomerEmail,linkCustomerEmail,startCustomerTelegram,listCustomerSubscriptions,getCustomerSubscription,listCustomerPeriods,listCustomerPlans,listCustomerPaymentOptions,listCustomerInvoices,createCustomerInvoice,customerCheckout} from "../lib/api/generated/sdk.gen";
import type {PortalCustomer,CustomerAuthMethods,CustomerInvoiceInput,CustomerCheckoutForm} from "../lib/api/generated/types.gen";

const sessionKey=["customer-session"];
function readProof(){
 const q=new URLSearchParams(window.location.hash.slice(1));
 const token=q.get("email_token")??"",purpose=q.get("purpose")??"";
 return {token:token.length===43?token:"",purpose:["register","reset","link"].includes(purpose)?purpose:"",telegramError:q.get("auth_error")==="telegram"};
}
export function CustomerPortal(){
 const s=useStrings(),t=s.customer,cache=useQueryClient();
 const [proof,setProof]=useState(readProof);
 useEffect(()=>{
  const acceptFragment=()=>{if(!window.location.hash)return;setProof(readProof());window.history.replaceState(window.history.state,"",window.location.pathname+window.location.search);};
  acceptFragment();window.addEventListener("hashchange",acceptFragment);return()=>window.removeEventListener("hashchange",acceptFragment);
 },[]);
 const methods=useQuery({queryKey:["customer-methods"],queryFn:async({signal})=>(await getCustomerAuthMethods({signal,throwOnError:true})).data,retry:false});
 const me=useQuery({queryKey:sessionKey,queryFn:async({signal})=>{try{return(await getCustomerMe({signal,throwOnError:true})).data;}catch(e){if(apiErrorCode(e)==="customer_auth_failed")return null;throw e;}},enabled:methods.data?.enabled===true,retry:false,refetchInterval:30000});
 const setAccount=(account:PortalCustomer|null)=>{cache.removeQueries({queryKey:["customer-data"]});cache.setQueryData(sessionKey,account);};
 const logout=useMutation({mutationFn:async()=>{await customerLogout({throwOnError:true});},onSuccess:()=>setAccount(null)});
 const err=methods.error??me.error??logout.error;
 return <main className="mx-auto min-h-dvh w-full max-w-5xl space-y-6 p-4 pb-16 sm:p-8">
  <PageHeader title={t.title} description={t.description} actions={me.data&&
    <Button variant="secondary" disabled={logout.isPending} onClick={()=>logout.mutate()}>{t.logout}</Button>}/>
  {err&&<p role="alert" className="text-warn">{apiErrorMessage(err as {code?:unknown},s)}</p>}
  {methods.isPending&&<p>{t.loading}</p>}
  {methods.data&&!methods.data.enabled&&<p>{t.unavailable}</p>}
  {methods.data?.enabled&&<>
   {proof.telegramError&&<p role="alert" className="text-warn">{t.telegramError}</p>}
   {proof.token&&proof.purpose?<EmailProof token={proof.token} reset={proof.purpose==="reset"} complete={account=>{setProof({token:"",purpose:"",telegramError:false});setAccount(account);}}/>:me.isPending?<p>{t.loading}</p>:me.data?<CustomerHome key={me.data.id} account={me.data} methods={methods.data} expired={()=>setAccount(null)}/>:<CustomerLogin methods={methods.data} complete={setAccount}/>}
  </>}
 </main>;
}
function EmailProof({token,reset,complete}:{token:string;reset:boolean;complete:(c:PortalCustomer)=>void}){
 const s=useStrings(),t=s.customer;const[password,setPassword]=useState("");
 const work=useMutation({retry:false,mutationFn:async()=>(await completeCustomerEmail({body:{token,...(reset?{password}:{})},throwOnError:true})).data,onSuccess:complete});
 return <form className="workspace-card space-y-4" onSubmit={e=>{e.preventDefault();work.mutate();}}>
  <h2 className="text-xl font-semibold">{reset?t.reset:t.verify}</h2><p>{t.proofHint}</p>
  {reset&&<label className="block">{t.newPassword}<Input required type="password" autoComplete="new-password" minLength={12} maxLength={72} value={password} onChange={e=>setPassword(e.target.value)}/></label>}
  {work.error&&<p role="alert" className="text-warn">{apiErrorMessage(work.error as {code?:unknown},s)}</p>}
  <Button disabled={work.isPending}>{work.isPending?t.working:reset?t.reset:t.verify}</Button>
 </form>;
}
function CustomerLogin({methods,complete}:{methods:CustomerAuthMethods;complete:(c:PortalCustomer)=>void}){
 const s=useStrings(),t=s.customer;const[mode,setMode]=useState<"login"|"register"|"recover">("login"),[email,setEmail]=useState(""),[password,setPassword]=useState(""),[sent,setSent]=useState(false);
 const work=useMutation({retry:false,mutationFn:async()=>{
  if(mode==="login"){complete((await customerLogin({body:{email,password},throwOnError:true})).data);return;}
  if(mode==="register")await customerRegister({body:{email,password},throwOnError:true});else await customerRecover({body:{email},throwOnError:true});
  setPassword("");setSent(true);
 }});
 const telegram=useMutation({retry:false,mutationFn:async()=>{const{data}=await startCustomerTelegram({body:{link:false},throwOnError:true});window.location.assign(data.url);}});
 return <section className="mx-auto max-w-lg workspace-card space-y-4">
  {methods.email&&<>
   <nav className="flex flex-wrap gap-2">{(["login","register","recover"] as const).map(m=><Button type="button" key={m} variant={m===mode?"primary":"secondary"} disabled={work.isPending||(m!=="login"&&!methods.email_delivery)} onClick={()=>{setMode(m);setSent(false);work.reset();setPassword("");}}>{t[m]}</Button>)}</nav>
   {!methods.email_delivery&&<p className="text-sm text-text-muted">{t.deliveryUnavailable}</p>}
   <form className="space-y-4" onSubmit={e=>{e.preventDefault();work.mutate();}}><fieldset disabled={work.isPending} className="space-y-4">
    <label className="block">{t.email}<Input required type="email" autoComplete="username" maxLength={254} value={email} onChange={e=>setEmail(e.target.value)}/></label>
    {mode!=="recover"&&<><label className="block">{t.password}<Input required type="password" autoComplete={mode==="login"?"current-password":"new-password"} minLength={12} maxLength={72} value={password} onChange={e=>setPassword(e.target.value)}/></label>{mode==="register"&&<p className="text-sm text-text-muted">{t.passwordHint}</p>}</>}
    <Button>{work.isPending?t.working:mode==="login"?t.login:t.send}</Button>
   </fieldset></form>
   {sent&&<p role="status">{t.sent}</p>}
  </>}
  {methods.telegram&&<Button type="button" variant="secondary" disabled={telegram.isPending} onClick={()=>telegram.mutate()}>{t.telegram}</Button>}
  {(work.error||telegram.error)&&<p role="alert" className="text-warn">{apiErrorMessage((work.error??telegram.error) as {code?:unknown},s)}</p>}
 </section>;
}
function CustomerHome({account,methods,expired}:{account:PortalCustomer;methods:CustomerAuthMethods;expired:()=>void}){
 const s=useStrings(),t=s.customer;
 const [subID,setSubID]=useState(""),[planID,setPlanID]=useState(""),[accountID,setAccountID]=useState("");
 const [subOffset,setSubOffset]=useState(0),[invoiceOffset,setInvoiceOffset]=useState(0),[periodOffset,setPeriodOffset]=useState(0),[paymentMethod,setPaymentMethod]=useState<"AC"|"PC">("AC");
 const [attempt,setAttempt]=useState<CustomerInvoiceInput|null>(()=>readAttempt(account.id));
 const [created,setCreated]=useState(false),[linkEmailOpen,setLinkEmailOpen]=useState(false);
 const subs=useQuery({queryKey:["customer-data",account.id,"subscriptions",subOffset],queryFn:async({signal})=>(await listCustomerSubscriptions({query:{offset:subOffset},signal,throwOnError:true})).data,retry:false,refetchInterval:30000});
 const detail=useQuery({queryKey:["customer-data",account.id,"subscription",subID],queryFn:async({signal})=>(await getCustomerSubscription({path:{subscription_id:subID},signal,throwOnError:true})).data,enabled:!!subID,retry:false,refetchInterval:15000});
 const periods=useQuery({queryKey:["customer-data",account.id,"periods",subID,periodOffset],queryFn:async({signal})=>(await listCustomerPeriods({path:{subscription_id:subID},query:{offset:periodOffset},signal,throwOnError:true})).data,enabled:!!subID,retry:false});
 const plans=useQuery({queryKey:["customer-data",account.id,"plans"],queryFn:async({signal})=>(await listCustomerPlans({signal,throwOnError:true})).data,retry:false});
 const options=useQuery({queryKey:["customer-data",account.id,"payment-options"],queryFn:async({signal})=>(await listCustomerPaymentOptions({signal,throwOnError:true})).data,retry:false});
 const invoices=useQuery({queryKey:["customer-data",account.id,"invoices",invoiceOffset],queryFn:async({signal})=>(await listCustomerInvoices({query:{offset:invoiceOffset},signal,throwOnError:true})).data,retry:false,refetchInterval:15000});
 const selectedSub=subs.data?.find(x=>x.id===subID),selectedPlan=plans.data?.find(x=>x.id===planID);
 const compatibleAccounts=options.data?.filter(a=>!selectedPlan||a.currency===selectedPlan.terms.currency&&selectedPlan.terms.currency_scale===2);
 const canQuote=!!selectedPlan&&!!compatibleAccounts?.some(a=>a.id===accountID);
 const refresh=async()=>{await Promise.all([subs.refetch(),invoices.refetch(),...(subID?[detail.refetch(),periods.refetch()]:[])]);};
 const remember=(value:CustomerInvoiceInput|null)=>{setAttempt(value);try{if(value)sessionStorage.setItem(attemptKey(account.id),JSON.stringify(value));else sessionStorage.removeItem(attemptKey(account.id));}catch{/* In-memory idempotency remains available when storage is disabled. */}};
 const quote=useMutation({retry:false,mutationFn:async()=>{
  const input=attempt??{subscription_id:subID,plan_id:planID,account_id:accountID,request_key:crypto.randomUUID()};remember(input);
  return(await createCustomerInvoice({body:input,throwOnError:true})).data;
 },onSuccess:async invoice=>{remember(null);setCreated(true);setSubID(invoice.subscription_id);setInvoiceOffset(0);await refresh();}});
 const pay=useMutation({retry:false,mutationFn:async(id:string)=>{const{data}=await customerCheckout({path:{invoice_id:id},body:{method:paymentMethod},throwOnError:true});submitCheckout(data);}});
 const telegram=useMutation({retry:false,mutationFn:async()=>{const{data}=await startCustomerTelegram({body:{link:true},throwOnError:true});window.location.assign(data.url);}});
 const error=subs.error??detail.error??periods.error??plans.error??options.error??invoices.error??quote.error??pay.error??telegram.error;
 const authFailed=apiErrorCode(error)==="customer_auth_failed";
 useEffect(()=>{if(authFailed)expired();},[authFailed,expired]);
 const date=(seconds:number)=>new Date(seconds*1000).toLocaleString(s.locale);
 const bytes=(value:string)=>`${BigInt(value).toLocaleString(s.locale)} ${t.bytes}`;
 return <div className="space-y-6">
  {error&&<p role="alert" className="text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
  <section className="workspace-card space-y-3"><h2 className="text-xl font-semibold">{t.account}</h2>{account.email&&<p className="break-all">{account.email}</p>}
   {account.telegram_linked?<p>{t.telegramLinked}</p>:methods.telegram&&<Button variant="secondary" disabled={telegram.isPending} onClick={()=>telegram.mutate()}>{t.linkTelegram}</Button>}
   {!account.email&&methods.email_delivery&&<Button variant="secondary" onClick={()=>setLinkEmailOpen(!linkEmailOpen)}>{t.linkEmail}</Button>}
   {((!account.telegram_linked&&methods.telegram)||(!account.email&&methods.email_delivery))&&<p className="text-sm text-text-muted">{t.freshHint}</p>}
   {linkEmailOpen&&<LinkEmail/>}
  </section>
  <p className="rounded-xl border border-border p-4 text-text-muted">{t.setupBoundary}</p>
  <section className="workspace-card space-y-4"><div className="flex flex-wrap justify-between gap-3"><h2 className="text-xl font-semibold">{t.services}</h2><Button variant="secondary" onClick={()=>void refresh()}>{t.refresh}</Button></div>
   {!subs.isPending&&subs.data?.length===0&&<p className="rounded-lg bg-[rgb(var(--lavender))] p-4 font-semibold">{t.noServices}</p>}
   <label className="block">{t.chooseService}<Select aria-label={t.chooseService} disabled={!!attempt} value={subID} onChange={e=>{setSubID(e.target.value);setPlanID("");setPeriodOffset(0);setCreated(false);}}><option value="">{t.newService}</option>{subs.data?.map(x=><option key={x.id} value={x.id}>{t.service} {x.id.slice(-8)}</option>)}</Select></label>
   <div className="flex gap-2"><Button variant="secondary" disabled={subOffset===0||subs.isFetching||!!attempt} onClick={()=>setSubOffset(Math.max(0,subOffset-100))}>{t.previous}</Button><Button variant="secondary" disabled={subs.data?.length!==100||subs.isFetching||!!attempt} onClick={()=>setSubOffset(subOffset+100)}>{t.next}</Button></div>
   {detail.data&&<div className="space-y-2"><p>{t.paidThrough}: {detail.data.paid_through?date(detail.data.paid_through):t.unpaid}</p><p>{detail.data.access_status==="configured"?t.accessConfigured:detail.data.access_status==="disabled"?t.disabled:detail.data.access_status==="unpaid"?t.noCurrent:t.accessPending}</p>
    {detail.data.access.usage?<div className="rounded-lg border border-border p-3 space-y-2">
     <p>{t.observedTraffic}: {detail.data.access.usage.last_reported_at!==null?bytes(detail.data.access.usage.observed_bytes):t.noReport}</p>
     <p>{t.quota}: {detail.data.access.usage.quota_bytes===null?t.unlimited:bytes(detail.data.access.usage.quota_bytes)}</p>
     <p>{t.reservedTraffic}: {bytes(detail.data.access.usage.granted_bytes)}</p><p>{t.unknownTraffic}: {bytes(detail.data.access.usage.unknown_bytes)}</p>
     {detail.data.access.usage.last_reported_at!==null&&<p>{t.usageUpdated}: {date(detail.data.access.usage.last_reported_at)}</p>}
     <p className="text-sm text-text-muted">{t.reserveHint}</p>
    </div>:<p className="text-text-muted">{t.usagePending}</p>}
    {detail.data.access.nodes.length>0&&<><h3 className="font-semibold">{t.servers}</h3><ul className="space-y-2">{detail.data.access.nodes.map(node=><li key={node.node_id} className="rounded-lg border border-border p-3 space-y-2"><p>{t.server} {node.node_id.slice(-8)}: {t.nodeStates[node.status]}</p><div className="flex flex-wrap gap-3">{node.links.map(link=><a key={link.url} href={link.url} className="text-accent underline break-all">{t.connect} · {link.mode}</a>)}</div></li>)}</ul></>}

    {detail.data.current_period?<p>{t.currentPeriod}: {t.from} {date(detail.data.current_period.starts_at)} {t.until} {date(detail.data.current_period.ends_at)}</p>:<p>{t.noCurrent}</p>}
    <h3 className="font-semibold">{t.periods}</h3><ul className="space-y-2">{periods.data?.map(p=><li key={p.id} className="rounded-lg border border-border p-3"><p>{date(p.starts_at)} → {date(p.ends_at)}</p><p>{p.terms.quota_bytes===null?t.unlimited:`${t.quota}: ${p.terms.quota_bytes} ${t.bytes}`}</p></li>)}</ul>
    {!periods.isPending&&periods.data?.length===0&&<p>{t.noPeriods}</p>}
    <div className="flex gap-2"><Button variant="secondary" disabled={periodOffset===0||periods.isFetching} onClick={()=>setPeriodOffset(Math.max(0,periodOffset-100))}>{t.previous}</Button><Button variant="secondary" disabled={periods.data?.length!==100||periods.isFetching} onClick={()=>setPeriodOffset(periodOffset+100)}>{t.next}</Button></div>
   </div>}
   <form className="space-y-3" onSubmit={e=>{e.preventDefault();if(attempt||canQuote)quote.mutate();}}><fieldset disabled={quote.isPending||!!attempt} className="space-y-3">
    <label className="block">{t.tariff}<Select required aria-label={t.tariff} value={planID} onChange={e=>{setPlanID(e.target.value);setAccountID("");}}><option value="">{t.chooseTariff}</option>{plans.data?.filter(p=>!subID||p.cluster_id===selectedSub?.cluster_id).map(p=><option key={p.id} value={p.id}>{p.name} · {formatMoney(p.terms.price_minor,p.terms.currency_scale)} {p.terms.currency}</option>)}</Select></label>
    {selectedPlan&&<p>{selectedPlan.terms.duration_seconds} {t.seconds} · {selectedPlan.terms.quota_bytes===null?t.unlimited:`${selectedPlan.terms.quota_bytes} ${t.bytes}`}</p>}
    <label className="block">{t.paymentOption}<Select required aria-label={t.paymentOption} value={accountID} onChange={e=>setAccountID(e.target.value)}><option value="">{t.choosePayment}</option>{compatibleAccounts?.map(a=><option key={a.id} value={a.id}>{a.name}</option>)}</Select></label>
   </fieldset>
   {!options.isPending&&compatibleAccounts?.length===0&&<p>{t.noPayment}</p>}
   <p className="text-sm text-text-muted">{t.renewHint}</p>
   {attempt&&<p role="status">{t.quotePending}</p>}
   <Button disabled={quote.isPending||(!attempt&&!canQuote)}>{quote.isPending?t.working:attempt?t.retryQuote:t.quote}</Button>
   {attempt&&!quote.isPending&&<Button type="button" variant="secondary" onClick={()=>{if(window.confirm(t.discardQuote)){remember(null);quote.reset();void refresh();}}}>{t.newQuote}</Button>}
   {created&&<p role="status">{t.quoteCreated}</p>}
  </form>
  </section>
  <section className="space-y-4"><h2 className="text-xl font-semibold">{t.invoices}</h2><p className="text-sm text-text-muted">{t.payHint}</p>
   <label className="block">{t.paymentOption}<Select aria-label={t.paymentOption+" YooMoney"} value={paymentMethod} onChange={e=>setPaymentMethod(e.target.value as "AC"|"PC")}><option value="AC">{t.card}</option><option value="PC">{t.wallet}</option></Select></label>
   {!invoices.isPending&&invoices.data?.length===0&&<p>{t.noInvoices}</p>}
   <ul className="grid gap-4 sm:grid-cols-2">{invoices.data?.map(i=><li key={i.id} className="min-w-0 workspace-card space-y-2"><h3 className="font-semibold">{t.invoice} #{i.number}</h3><p>{t[i.status]}{i.test?` · ${t.test}`:""}</p><p>{formatMoney(i.amount_minor,i.currency_scale)} {i.currency}</p><p className="text-sm text-text-muted">{date(i.created_at)}</p>{i.status==="pending"&&!i.test&&<Button disabled={pay.isPending} onClick={()=>pay.mutate(i.id)}>{t.pay}</Button>}</li>)}</ul>
   <div className="flex gap-2"><Button variant="secondary" disabled={invoiceOffset===0||invoices.isFetching} onClick={()=>setInvoiceOffset(Math.max(0,invoiceOffset-100))}>{t.previous}</Button><Button variant="secondary" disabled={invoices.data?.length!==100||invoices.isFetching} onClick={()=>setInvoiceOffset(invoiceOffset+100)}>{t.next}</Button></div>
  </section>
 </div>;
}
function LinkEmail(){
 const s=useStrings(),t=s.customer;const[email,setEmail]=useState(""),[password,setPassword]=useState("");
 const work=useMutation({retry:false,mutationFn:async()=>{await linkCustomerEmail({body:{email,password},throwOnError:true});setPassword("");}});
 return <form className="space-y-3" onSubmit={e=>{e.preventDefault();work.mutate();}}><label className="block">{t.email}<Input required type="email" value={email} onChange={e=>setEmail(e.target.value)}/></label><label className="block">{t.password}<Input required type="password" autoComplete="new-password" minLength={12} maxLength={72} value={password} onChange={e=>setPassword(e.target.value)}/></label><Button disabled={work.isPending}>{t.send}</Button>{work.isSuccess&&<p role="status">{t.sent}</p>}{work.error&&<p role="alert" className="text-warn">{apiErrorMessage(work.error as {code?:unknown},s)}</p>}</form>;
}
function formatMoney(minor:string,scale:number){const digits=minor.padStart(scale+1,"0");return scale===0?digits:digits.slice(0,-scale)+"."+digits.slice(-scale);}
function attemptKey(customer:string){return "telemt-customer:invoice:"+customer;}
function readAttempt(customer:string):CustomerInvoiceInput|null{
 try{const raw=sessionStorage.getItem(attemptKey(customer));if(!raw||raw.length>2048)return null;const value:unknown=JSON.parse(raw);if(!value||typeof value!=="object")return null;const v=value as Record<string,unknown>;if(typeof v["plan_id"]!=="string"||typeof v["account_id"]!=="string"||typeof v["request_key"]!=="string"||!/^[A-Za-z0-9_-]{16,128}$/.test(v["request_key"])||(v["subscription_id"]!==undefined&&typeof v["subscription_id"]!=="string"))return null;return{plan_id:v["plan_id"],account_id:v["account_id"],request_key:v["request_key"],subscription_id:v["subscription_id"] as string|undefined};}catch{return null;}
}
function submitCheckout(checkout:CustomerCheckoutForm){
 if(checkout.method!=="POST"||!["https://auth.robokassa.ru/Merchant/Index.aspx","https://yoomoney.ru/quickpay/confirm"].includes(checkout.action))throw new Error("Invalid checkout destination");
 const form=document.createElement("form");form.method="POST";form.action=checkout.action;
 for(const[name,value]of Object.entries(checkout.fields)){const input=document.createElement("input");input.type="hidden";input.name=name;input.value=value;form.appendChild(input);}
 document.body.appendChild(form);HTMLFormElement.prototype.submit.call(form);
}
