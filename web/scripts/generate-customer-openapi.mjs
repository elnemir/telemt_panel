import fs from 'node:fs';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url),{load,dump}=require('js-yaml');
const file=new URL('../../api/openapi.yaml',import.meta.url);let source=fs.readFileSync(file,'utf8');
const ref=name=>({$ref:'#/components/schemas/'+name}),string={type:'string'},boolean={type:'boolean'},integer={type:'integer',format:'int64'},array=items=>({type:'array',items});
const object=(properties,required=Object.keys(properties))=>({type:'object',required,additionalProperties:false,properties});
const response=(description,schema)=>({description,...(schema?{content:{'application/json':{schema}}}:{})});
const errors=Object.fromEntries(['400','401','403','404','409','429','503'].map(code=>[code,response('Rejected, expired or unavailable.',ref('Error'))]));
const param=name=>({name,in:'path',required:true,schema:string}),offset={name:'offset',in:'query',schema:{type:'integer',minimum:0}};
const op=(id,parameters,body,code,schema,description,publicRoute=false)=>({operationId:id,tags:['customer'],security:publicRoute?[]:[{customerSession:[]}],description,parameters,...(body?{requestBody:{required:true,content:{'application/json':{schema:body}}}}:{}),responses:{[code]:response(description,schema),...errors}});
const email={type:'string',format:'email',maxLength:254};
const password={type:'string',minLength:12,maxLength:72,description:'12–72 UTF-8 bytes. No truncation. Never logged or stored in plaintext.'};
const paths={
 '/api/customer/auth/methods':{get:op('getCustomerAuthMethods',[],null,200,ref('CustomerAuthMethods'),'Enabled portal methods. Email delivery readiness is separate from existing password login.',true)},
 '/api/customer/auth/login':{post:op('customerLogin',[],object({email,password}),200,ref('PortalCustomer'),'Password login; issues separate HttpOnly customer cookie. Admin sessions never authorize this API. Same-origin CSRF required.',true)},
 '/api/customer/auth/register':{post:op('customerRegister',[],object({email,password}),202,null,'Request mailbox verification. Generic response for existing accounts; delivery failure is not reported as success. Customer is created only after proof.',true)},
 '/api/customer/auth/recover':{post:op('customerRecover',[],object({email}),202,null,'Generic recovery request. SMTP must be configured; unknown or disabled accounts are not disclosed.',true)},
 '/api/customer/auth/email/complete':{post:op('completeCustomerEmail',[],object({token:{type:'string',minLength:43,maxLength:43},password},['token']),200,ref('PortalCustomer'),'Consume one-time proof. Password is required for reset, omitted for register/link. Reset revokes previous customer sessions.',true)},
 '/api/customer/auth/email/link':{post:op('linkCustomerEmail',[],object({email,password}),202,null,'Add email/password to a Telegram-only customer. Requires fresh customer authentication within five minutes; no merging of existing accounts.')},
 '/api/customer/auth/logout':{post:op('customerLogout',[],null,204,null,'Durably revoke current customer cookie; administrator sessions are unchanged.')},
 '/api/customer/auth/me':{get:op('getCustomerMe',[],null,200,ref('PortalCustomer'),'Current customer; no password hash, Telegram subject or session token.')},
 '/api/customer/auth/telegram/start':{post:op('startCustomerTelegram',[],object({link:boolean}),200,object({url:string}),'Start Telegram code + PKCE + nonce flow; sets HttpOnly Lax browser-binding cookie. link=true requires fresh customer session.',true)},
 '/api/customer/auth/telegram/callback':{get:{...op('completeCustomerTelegram',[{name:'state',in:'query',schema:string},{name:'code',in:'query',schema:string},{name:'error',in:'query',schema:string}],null,303,null,'Provider redirect; consumes browser-bound state and verifies fixed Telegram issuer, RS256, audience, expiry and nonce. Redirects to configured portal only.',true),responses:{'303':{description:'Redirect to configured portal with a customer cookie on success, or generic auth_error fragment on failure.'},...errors}}},
 '/api/customer/plans':{get:op('listCustomerPlans',[],null,200,array(ref('BillingPlan')),'Enabled tariffs only. Price/currency compatibility is checked when creating an invoice.')},
 '/api/customer/payment-options':{get:op('listCustomerPaymentOptions',[],null,200,array(ref('CustomerPaymentOption')),'Enabled live payment choices without merchant configuration or credential references.')},
 '/api/customer/subscriptions':{get:op('listCustomerSubscriptions',[offset],null,200,array(ref('CustomerSubscription')),'Own subscriptions only, up to 100 per page.')},
 '/api/customer/subscriptions/{subscription_id}':{get:op('getCustomerSubscription',[param('subscription_id')],null,200,ref('CustomerSubscriptionDetail'),'Own subscription, paid-through status, managed node delivery and current-period usage. Links require confirmed matching identity/grant with an unexpired lease. Configured is not an external availability probe. Unknown usage includes reserved or unreported bytes, never treated as zero.')},
 '/api/customer/subscriptions/{subscription_id}/periods':{get:op('listCustomerPeriods',[param('subscription_id'),offset],null,200,array(ref('CustomerPaidPeriod')),'Own immutable paid periods; up to 100, newest first. Another customer subscription returns 404.')},
 '/api/customer/invoices':{get:op('listCustomerInvoices',[offset],null,200,array(ref('BillingInvoice')),'Own invoices only, up to 100 newest first.'),post:op('createCustomerInvoice',[],ref('CustomerInvoiceInput'),201,ref('BillingInvoice'),'Fix current tariff version and amount. customer_id comes exclusively from session. Durable request_key replay returns the same invoice; changed payload conflicts.')},
 '/api/customer/invoices/{invoice_id}':{get:op('getCustomerInvoice',[param('invoice_id')],null,200,ref('BillingInvoice'),'Own invoice; another customer invoice returns 404. Success redirects cannot modify this status.')},
 '/api/customer/invoices/{invoice_id}/checkout':{post:op('customerCheckout',[param('invoice_id')],object({method:{type:'string',enum:['AC','PC']}}),200,ref('CustomerCheckoutForm'),'Signed form for own pending invoice. Browser submits only on explicit payment action. Return URL is configuration-owned. No payment or entitlement is created by this request.')},
};
const schemas={
 CustomerAuthMethods:object({enabled:boolean,email:boolean,email_delivery:boolean,telegram:boolean}),
 PortalCustomer:object({id:string,email:string,email_verified:boolean,telegram_linked:boolean,enabled:boolean,created_at:integer}),
 CustomerPaymentOption:object({id:string,name:string,provider:{type:'string',enum:['robokassa','yoomoney']},currency:{type:'string',enum:['RUB']}}),
 CustomerSubscription:object({id:string,customer_id:string,cluster_id:string,identity_id:string,enabled:boolean,created_at:integer}),
 CustomerPaidPeriod:object({id:string,subscription_id:string,invoice_id:string,plan_id:string,plan_version:integer,starts_at:integer,ends_at:integer,terms:ref('BillingPlanTerms')}),
 CustomerSubscriptionDetail:object({subscription:ref('CustomerSubscription'),current_period:{anyOf:[ref('CustomerPaidPeriod'),{type:'null'}]},paid_through:{type:['integer','null'],format:'int64'},server_time:integer,access_status:{type:'string',enum:['not_provisioned','disabled','unpaid','configured']},usage_available:boolean,access:ref('CustomerServiceAccess')}),
 CustomerConnectionLink:object({mode:{type:'string',enum:['classic','secure','faketls']},url:{type:'string',pattern:'^tg://proxy\\?'}}),
 CustomerServiceNode:object({node_id:string,status:{type:'string',enum:['pending','conflict','unavailable','quota_empty','configured']},checked_at:integer,budget_until:integer,links:array(ref('CustomerConnectionLink'))}),
 CustomerQuotaUsage:object({period_id:string,quota_bytes:{type:['string','null'],pattern:'^[0-9]+$'},overrun_bytes:{type:['string','null'],pattern:'^[0-9]+$'},granted_bytes:{type:'string',pattern:'^[0-9]+$'},observed_bytes:{type:'string',pattern:'^[0-9]+$'},unknown_bytes:{type:'string',pattern:'^[0-9]+$'},allocations:integer,last_reported_at:{type:['integer','null'],format:'int64'}}),
 CustomerServiceAccess:object({status:{type:'string',enum:['not_provisioned','disabled','unpaid','configured']},nodes:array(ref('CustomerServiceNode')),links:array(ref('CustomerConnectionLink')),usage:{anyOf:[ref('CustomerQuotaUsage'),{type:'null'}]}}),
 CustomerInvoiceInput:object({subscription_id:string,plan_id:string,account_id:string,request_key:{type:'string',pattern:'^[A-Za-z0-9_-]{16,128}$'}},['plan_id','account_id','request_key']),
 CustomerCheckoutForm:object({action:{type:'string',enum:['https://auth.robokassa.ru/Merchant/Index.aspx','https://yoomoney.ru/quickpay/confirm']},method:{type:'string',enum:['POST']},fields:{type:'object',additionalProperties:{type:'string'}}}),
};
function replaceBlock(name,indent,value,before){const start=indent+'# '+name+' start\n',end=indent+'# '+name+' end\n';const a=source.indexOf(start);if(a>=0){const b=source.indexOf(end,a);if(b<0)throw Error('missing end');source=source.slice(0,a)+source.slice(b+end.length);}if(!source.includes(before))throw Error('missing marker');const text=dump(value,{lineWidth:110,noRefs:true}).trimEnd().split('\n').map(line=>indent+line).join('\n')+'\n';source=source.replace(before,start+text+end+before);}
replaceBlock('customer paths','  ',paths,'  /api/nodes:');replaceBlock('customer schemas','    ',schemas,'    ConfigTree:');
if(!load(source).components.securitySchemes.customerSession)source=source.replace('  securitySchemes:\n','  securitySchemes:\n    customerSession:\n      type: apiKey\n      in: cookie\n      name: telemt_customer_session\n');
for(const code of ['customer_auth_failed','customer_invalid','customer_method_unavailable','customer_email_delivery_failed']){if(!load(source).components.schemas.Error.properties.code.enum.includes(code))source=source.replace(/^(\s*)- config_graph_limit\s*$/m,'$1- config_graph_limit\n$1- '+code);}
fs.writeFileSync(file,source);
