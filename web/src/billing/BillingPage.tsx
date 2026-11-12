import {useState} from "react";
import {useBlocker} from "@tanstack/react-router";
import {useMutation,useQuery} from "@tanstack/react-query";
import {useStrings} from "../i18n";
import {apiErrorMessage} from "../people/apiError";
import {Button} from "../ui/Button";
import {Input} from "../ui/Input";
import {Select} from "../ui/Select";
import {Sheet} from "../ui/Sheet";
import {PageHeader} from "../ui/PageHeader";
import {getBasePath} from "../lib/base-path";
import {getPlatformStatusOptions,listClustersOptions,listBillingPlansOptions,listPaymentAccountsOptions,listBillingInvoicesOptions,listBillingPaymentsOptions} from "../lib/api/generated/@tanstack/react-query.gen";
import {createBillingPlan,updateBillingPlan,createPaymentAccount,updatePaymentAccount} from "../lib/api/generated/sdk.gen";
import type {BillingPlan,BillingPlanTerms,Cluster,PaymentAccount,PaymentAccountConfig} from "../lib/api/generated/types.gen";

export function BillingPage(){
 const s=useStrings(),t=s.billing;
 const status=useQuery(getPlatformStatusOptions()),enabled=status.data?.enabled===true;
 const plans=useQuery({...listBillingPlansOptions(),enabled});
 const accounts=useQuery({...listPaymentAccountsOptions(),enabled});
 const clusters=useQuery({...listClustersOptions(),enabled});
 const [plan,setPlan]=useState<BillingPlan|"new">(),[account,setAccount]=useState<PaymentAccount|"new">();
 const err=status.error??plans.error??accounts.error??clusters.error;
 return <main className="mx-auto w-full max-w-6xl space-y-6 p-4 pb-24">
  <PageHeader title={t.title} description={t.description}/>
  {err&&<p role="alert" className="text-warn">{apiErrorMessage(err as {code?:unknown},s)}</p>}
  {status.isPending&&<p>{s.clusters.loading}</p>}
  {status.data&&!enabled&&<p>{status.data.available?s.clusters.setupFull:s.clusters.setupLite}</p>}
  {enabled&&<>
   <p className="rounded-xl border border-border bg-surface p-4 text-text-muted">{t.boundary}</p>
   <section className="space-y-3" aria-label={t.plans}>
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-xl font-semibold">{t.plans}</h2><Button disabled={!clusters.data?.length} onClick={()=>setPlan("new")}>{t.newPlan}</Button></div>
    {!plans.isPending&&plans.data?.length===0&&<p>{t.noPlans}</p>}
    <ul className="grid gap-4 md:grid-cols-2">{plans.data?.map(p=><li key={p.id} className="min-w-0 rounded-xl border border-border bg-surface p-4 space-y-2">
     <h3 className="break-words font-semibold">{p.name}</h3><p>{clusters.data?.find(c=>c.id===p.cluster_id)?.name??p.cluster_id} · {p.enabled?t.active:t.disabled}</p>
     <p className="break-words">{formatPrice(p.terms.price_minor,p.terms.currency_scale)} {p.terms.currency}</p>
     <p>{p.terms.duration_seconds} {t.period} · {t.version} {p.version}</p>
     <p className="break-words">{p.terms.quota_bytes===null?t.unlimited:`${t.quota}: ${p.terms.quota_bytes}`}</p>
     <Button variant="secondary" onClick={()=>setPlan(p)}>{t.editPlan}</Button>
    </li>)}</ul>
   </section>
   <section className="space-y-3" aria-label={t.accounts}>
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-xl font-semibold">{t.accounts}</h2><Button onClick={()=>setAccount("new")}>{t.newAccount}</Button></div>
    {!accounts.isPending&&accounts.data?.length===0&&<p>{t.noAccounts}</p>}
    <ul className="grid gap-4 md:grid-cols-2">{accounts.data?.map(a=><li key={a.id} className="min-w-0 rounded-xl border border-border bg-surface p-4 space-y-2">
     <h3 className="break-words font-semibold">{a.name}</h3><p>{a.provider} · {a.test?t.test:t.live} · {a.enabled?t.active:t.disabled}</p>
     <Button variant="secondary" onClick={()=>setAccount(a)}>{t.edit}</Button>
    </li>)}</ul>
   </section>
   <FinancialHistory/>
  </>}
  {plan&&<PlanEditor plan={plan==="new"?undefined:plan} clusters={clusters.data??[]} close={()=>setPlan(undefined)} saved={async()=>{setPlan(undefined);await plans.refetch();}}/>}
  {account&&<AccountEditor account={account==="new"?undefined:account} close={()=>setAccount(undefined)} saved={async()=>{setAccount(undefined);await accounts.refetch();}}/>}
 </main>;
}
const newTerms:BillingPlanTerms={currency:"RUB",currency_scale:2,price_minor:"",duration_seconds:"",quota_bytes:"",overrun_bytes:"",node_policy:{enabled:true,max_tcp_conns:null,max_unique_ips:null,rate_limit_up_bps:null,rate_limit_down_bps:null,expiration_rfc3339:"",user_ad_tag:""}};
function PlanEditor({plan,clusters,close,saved}:{plan?:BillingPlan;clusters:Cluster[];close:()=>void;saved:()=>Promise<void>}){
 const s=useStrings(),t=s.billing;
 const [name,setName]=useState(plan?.name??""),[cluster,setCluster]=useState(plan?.cluster_id??"");
 const [enabled,setEnabled]=useState(plan?.enabled??false),[terms,setTerms]=useState(plan?.terms??newTerms);
 const dirty=name!==(plan?.name??"")||cluster!==(plan?.cluster_id??"")||enabled!==(plan?.enabled??false)||JSON.stringify(terms)!==JSON.stringify(plan?.terms??newTerms);
 const work=useMutation({retry:false,mutationFn:async()=>{
  if(plan)await updateBillingPlan({path:{plan_id:plan.id},headers:{"If-Match":String(plan.revision)},body:{name,enabled,terms},throwOnError:true});
  else await createBillingPlan({body:{name,cluster_id:cluster,enabled,terms},throwOnError:true});
 },onSuccess:saved});
 const cancel=()=>{if(!work.isPending&&(!dirty||window.confirm(t.discard)))close();};
 useBlocker({shouldBlockFn:()=>work.isPending||(dirty&&!window.confirm(t.discard)),enableBeforeUnload:dirty||work.isPending});
 return <Sheet open title={plan?t.editPlan:t.newPlan} onClose={cancel}>
  <form className="space-y-4" onSubmit={e=>{e.preventDefault();work.mutate();}}><fieldset disabled={work.isPending} className="space-y-4">
   <p className="text-sm text-text-muted">{t.planHint}</p>
   <label className="block">{t.name}<Input required maxLength={128} value={name} onChange={e=>setName(e.target.value)}/></label>
   <label className="block">{t.cluster}<Select required aria-label={t.cluster} disabled={!!plan} value={cluster} onChange={e=>setCluster(e.target.value)}><option value="">{t.chooseCluster}</option>{clusters.map(c=><option key={c.id} value={c.id}>{c.name}</option>)}</Select></label>
   <label className="flex gap-2"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>{t.enabled}</label>
   <div className="grid grid-cols-2 gap-3">
    <label>{t.currency}<Input required pattern="[A-Z]{3}" maxLength={3} value={terms.currency} onChange={e=>setTerms({...terms,currency:e.target.value.toUpperCase()})}/></label>
    <label>{t.scale}<Input required type="number" min={0} max={6} step={1} value={terms.currency_scale} onChange={e=>setTerms({...terms,currency_scale:Number(e.target.value)})}/></label>
   </div>
   <label className="block">{t.price}<Input required inputMode="numeric" pattern="[0-9]{1,19}" value={terms.price_minor} onChange={e=>setTerms({...terms,price_minor:e.target.value})}/></label><p className="text-sm text-text-muted">{t.priceHint}</p>
   <label className="block">{t.duration}<Input required inputMode="numeric" pattern="[0-9]{1,12}" value={terms.duration_seconds} onChange={e=>setTerms({...terms,duration_seconds:e.target.value})}/></label><p className="text-sm text-text-muted">{t.durationHint}</p>
   <label className="flex gap-2"><input type="checkbox" checked={terms.quota_bytes===null} onChange={e=>setTerms({...terms,quota_bytes:e.target.checked?null:"",overrun_bytes:e.target.checked?null:""})}/>{t.unlimited}</label>
   {terms.quota_bytes!==null&&<><label className="block">{t.quota}<Input required inputMode="numeric" pattern="[0-9]{1,19}" value={terms.quota_bytes} onChange={e=>setTerms({...terms,quota_bytes:e.target.value})}/></label><label className="block">{t.overrun}<Input required inputMode="numeric" pattern="[0-9]{1,19}" value={terms.overrun_bytes??""} onChange={e=>setTerms({...terms,overrun_bytes:e.target.value})}/></label></>}
   <p className="text-sm text-text-muted">{t.quotaHint}</p><h3 className="font-semibold">{t.nodeLimits}</h3><p className="text-sm text-text-muted">{t.limitsHint}</p>
   {(["max_tcp_conns","max_unique_ips","rate_limit_up_bps","rate_limit_down_bps"] as const).map(field=><label key={field} className="block">{s.clusters.fields[field]}<Input type="number" min={0} step={1} max={field.startsWith("rate_")?100000000000:Number.MAX_SAFE_INTEGER} value={terms.node_policy[field]??""} onChange={e=>{const value=e.target.value===""?null:Number(e.target.value);if(value===null||Number.isSafeInteger(value))setTerms({...terms,node_policy:{...terms.node_policy,[field]:value}});}}/></label>)}
   <label className="block">{s.clusters.fields.user_ad_tag}<Input pattern="[a-fA-F0-9]{32}|" maxLength={32} value={terms.node_policy.user_ad_tag} onChange={e=>setTerms({...terms,node_policy:{...terms.node_policy,user_ad_tag:e.target.value}})}/></label>
  </fieldset>
  {work.error&&<p role="alert" className="text-warn">{apiErrorMessage(work.error as {code?:unknown},s)}</p>}
  <div className="flex gap-3"><Button disabled={work.isPending}>{work.isPending?t.saving:t.save}</Button><Button type="button" variant="secondary" disabled={work.isPending} onClick={cancel}>{t.cancel}</Button></div></form>
 </Sheet>;
}
const emptyConfig:PaymentAccountConfig={merchant:"",algorithm:"SHA256",password1_env:"",password2_env:"",wallet:"",notification_secret_env:"",receipt:null};
function AccountEditor({account,close,saved}:{account?:PaymentAccount;close:()=>void;saved:()=>Promise<void>}){
 const s=useStrings(),t=s.billing;
 const [name,setName]=useState(account?.name??""),[enabled,setEnabled]=useState(account?.enabled??false);
 const [provider,setProvider]=useState<"robokassa"|"yoomoney">(account?.provider??"robokassa"),[test,setTest]=useState(account?.test??true);
 const [config,setConfig]=useState<PaymentAccountConfig>(account?.config??emptyConfig);
 const dirty=name!==(account?.name??"")||enabled!==(account?.enabled??false)||provider!==(account?.provider??"robokassa")||test!==(account?.test??true)||JSON.stringify(config)!==JSON.stringify(account?.config??emptyConfig);
 const work=useMutation({retry:false,mutationFn:async()=>{
  if(account)await updatePaymentAccount({path:{account_id:account.id},headers:{"If-Match":String(account.revision)},body:{name,enabled},throwOnError:true});
  else await createPaymentAccount({body:{name,enabled,provider,test,config},throwOnError:true});
 },onSuccess:saved});
 const cancel=()=>{if(!work.isPending&&(!dirty||window.confirm(t.discard)))close();};
 useBlocker({shouldBlockFn:()=>work.isPending||(dirty&&!window.confirm(t.discard)),enableBeforeUnload:dirty||work.isPending});
 return <Sheet open title={account?t.editAccount:t.newAccount} onClose={cancel}>
  <form className="space-y-4" onSubmit={e=>{e.preventDefault();work.mutate();}}><fieldset disabled={work.isPending} className="space-y-4">
   <label className="block">{t.name}<Input required maxLength={128} value={name} onChange={e=>setName(e.target.value)}/></label>
   <label className="flex gap-2"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>{t.enabled}</label>
   <p className="text-sm text-text-muted">{t.accountHint}</p>
   {account&&<p>{t.readOnly}</p>}
   <fieldset disabled={!!account} className="space-y-4">
    <label className="block">{t.provider}<Select aria-label={t.provider} value={provider} onChange={e=>{const value=e.target.value as "robokassa"|"yoomoney";setProvider(value);setConfig({...emptyConfig,algorithm:value==="robokassa"?"SHA256":""});}}><option value="robokassa">Robokassa</option><option value="yoomoney">YooMoney</option></Select></label>
    <label className="flex gap-2"><input type="checkbox" checked={test} onChange={e=>setTest(e.target.checked)}/>{t.sandbox}</label>
    <p className="text-sm text-text-muted">{t.envHint}</p>
    {provider==="robokassa"?<>
     <label className="block">{t.merchant}<Input required maxLength={128} pattern="[A-Za-z0-9_.-]+" value={config.merchant??""} onChange={e=>setConfig({...config,merchant:e.target.value})}/></label>
     <label className="block">{t.algorithm}<Select aria-label={t.algorithm} value={config.algorithm??"SHA256"} onChange={e=>setConfig({...config,algorithm:e.target.value as "SHA256"|"MD5"})}><option value="SHA256">SHA256</option><option value="MD5">MD5</option></Select></label>
     {(["password1_env","password2_env"] as const).map((key,index)=><label className="block" key={key}>{index===0?t.password1:t.password2}<Input required autoComplete="off" pattern="TELEMT_PAYMENT_[A-Z0-9_]{1,96}" value={config[key]??""} onChange={e=>setConfig({...config,[key]:e.target.value})}/></label>)}
     <label className="flex gap-2"><input type="checkbox" checked={!!config.receipt} onChange={e=>setConfig({...config,receipt:e.target.checked?{name:"",tax:"",payment_method:"",tax_system:""}:null})}/>{t.receipt}</label>
     {config.receipt&&<>
      <label className="block">{t.receiptName}<Input required maxLength={128} value={config.receipt.name} onChange={e=>setConfig({...config,receipt:{...config.receipt!,name:e.target.value}})}/></label>
      {([{key:"tax",label:t.tax,values:["none","vat0","vat5","vat7","vat10","vat20","vat22","vat105","vat107","vat110","vat120","vat122"]},{key:"payment_method",label:t.paymentMethod,values:["full_prepayment","full_payment","prepayment","advance","partial_payment","credit","credit_payment"]},{key:"tax_system",label:t.taxSystem,values:["osn","usn_income","usn_income_outcome","esn","patent"]}] as const).map(({key,label,values})=><label key={key} className="block">{label}<Select aria-label={label} required={key!=="tax_system"} value={config.receipt![key]} onChange={e=>setConfig({...config,receipt:{...config.receipt!,[key]:e.target.value}})}><option value="">{key==="tax_system"?t.merchantDefault:t.choose}</option>{values.map(v=><option key={v} value={v}>{t.receiptValues[v]}</option>)}</Select></label>)}
     </>}
     <p className="text-sm text-text-muted">{t.receiptHint}</p>
    </>:<>
     <label className="block">{t.wallet}<Input required inputMode="numeric" pattern="[0-9]{11,20}" value={config.wallet??""} onChange={e=>setConfig({...config,wallet:e.target.value})}/></label>
     <label className="block">{t.notificationSecret}<Input required autoComplete="off" pattern="TELEMT_PAYMENT_[A-Z0-9_]{1,96}" value={config.notification_secret_env??""} onChange={e=>setConfig({...config,notification_secret_env:e.target.value})}/></label>
     <p className="text-sm text-text-muted">{t.yooHint}</p>
    </>}
   </fieldset>
   {account&&<><label className="block">{t.callback}<Input readOnly value={window.location.origin+getBasePath()+"/api/payments/"+account.id+"/notify"} onFocus={e=>e.target.select()}/></label><p className="text-sm text-text-muted">{t.callbackHint}</p></>}
  </fieldset>
  {work.error&&<p role="alert" className="text-warn">{apiErrorMessage(work.error as {code?:unknown},s)}</p>}
  <div className="flex gap-3"><Button disabled={work.isPending}>{work.isPending?t.saving:t.save}</Button><Button type="button" variant="secondary" disabled={work.isPending} onClick={cancel}>{t.cancel}</Button></div></form>
 </Sheet>;
}

function formatPrice(minor:string,scale:number):string{
 const digits=minor.padStart(scale+1,"0");
 return scale===0?digits:digits.slice(0,-scale)+"."+digits.slice(-scale);
}

function FinancialHistory(){
 const s=useStrings(),t=s.billing;
 const [invoiceOffset,setInvoiceOffset]=useState(0),[paymentOffset,setPaymentOffset]=useState(0);
 const invoices=useQuery({...listBillingInvoicesOptions({query:{offset:invoiceOffset}}),refetchInterval:30000});
 const payments=useQuery({...listBillingPaymentsOptions({query:{offset:paymentOffset}}),refetchInterval:30000});
 const date=(seconds:number)=>new Date(seconds*1000).toLocaleString(s.locale);
 return <section className="space-y-4" aria-label={t.payments}>
  <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-xl font-semibold">{t.payments}</h2><Button variant="secondary" onClick={()=>{void invoices.refetch();void payments.refetch();}}>{t.refresh}</Button></div>
  <p className="text-sm text-text-muted">{t.historyHint}</p>
  {(invoices.error||payments.error)&&<p role="alert" className="text-warn">{apiErrorMessage((invoices.error??payments.error) as {code?:unknown},s)}</p>}
  <ul className="grid gap-4 md:grid-cols-2">{payments.data?.map(p=><li key={p.id} className="min-w-0 rounded-xl border border-border bg-surface p-4 space-y-1">
   <strong className={p.kind==="unallocated_payment"?"text-warn":""}>{t[p.kind]}</strong><p>{p.provider} · {p.test?t.test:t.live}</p>
   <p>{t.gross}: {formatPrice(p.gross_minor,2)} {p.currency}</p>{p.net_minor!==null&&<p>{t.net}: {formatPrice(p.net_minor,2)} {p.currency}</p>}
   <p className="break-all">{t.invoice}: {p.invoice_id}</p><p className="break-all">{t.customer}: {p.customer_id}</p>
   <p className="text-sm text-text-muted">{p.time_source==="provider"?t.providerTime:t.notificationTime}: {date(p.payment_at)}</p><p className="text-sm text-text-muted">{t.received}: {date(p.received_at)}</p>
  </li>)}</ul>
  {!payments.isPending&&payments.data?.length===0&&<p>{t.noPayments}</p>}
  <div className="flex gap-2"><Button variant="secondary" disabled={paymentOffset===0||payments.isFetching} onClick={()=>setPaymentOffset(Math.max(0,paymentOffset-100))}>{s.clusters.previous}</Button><Button variant="secondary" disabled={payments.data?.length!==100||payments.isFetching} onClick={()=>setPaymentOffset(paymentOffset+100)}>{s.clusters.next}</Button></div>
  <h2 className="text-xl font-semibold">{t.invoices}</h2>
  <ul className="grid gap-4 md:grid-cols-2">{invoices.data?.map(i=><li key={i.id} className="min-w-0 rounded-xl border border-border bg-surface p-4 space-y-1">
   <strong>{t.invoice} #{i.number} · {t[i.status]}</strong><p>{formatPrice(i.amount_minor,i.currency_scale)} {i.currency} · {i.test?t.test:t.live}</p>
   <p className="break-all">{t.customer}: {i.customer_id}</p><p className="text-sm text-text-muted">{t.created}: {date(i.created_at)}</p>
  </li>)}</ul>
  {!invoices.isPending&&invoices.data?.length===0&&<p>{t.noInvoices}</p>}
  <div className="flex gap-2"><Button variant="secondary" disabled={invoiceOffset===0||invoices.isFetching} onClick={()=>setInvoiceOffset(Math.max(0,invoiceOffset-100))}>{s.clusters.previous}</Button><Button variant="secondary" disabled={invoices.data?.length!==100||invoices.isFetching} onClick={()=>setInvoiceOffset(invoiceOffset+100)}>{s.clusters.next}</Button></div>
 </section>;
}
