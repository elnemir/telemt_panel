import fs from 'node:fs';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url),{load,dump}=require('js-yaml');
const file=new URL('../../api/openapi.yaml',import.meta.url);
let source=fs.readFileSync(file,'utf8');
const ref=name=>({$ref:'#/components/schemas/'+name});
const string={type:'string'},boolean={type:'boolean'},integer={type:'integer',format:'int64'},array=items=>({type:'array',items});
const decimal={type:'string',pattern:'^[0-9]+$',description:'Exact int64 decimal string; do not parse as floating point.'};
const object=(properties,required=Object.keys(properties))=>({type:'object',required,additionalProperties:false,properties});
const response=(description,schema)=>({description,...(schema?{content:{'application/json':{schema}}}:{})});
const errors=Object.fromEntries(['400','401','403','404','409','428','503'].map(code=>[code,response('Rejected or unavailable.',ref('Error'))]));
const match={name:'If-Match',in:'header',required:true,schema:string};
const param=name=>({name,in:'path',required:true,schema:string});
const op=(id,parameters,body,code,schema,description)=>({operationId:id,tags:['billing'],description,parameters,...(body?{requestBody:{required:true,content:{'application/json':{schema:body}}}}:{}),responses:{[code]:response(description,schema),...errors}});
const paths={
 "/api/billing/invoices":{get:op("listBillingInvoices",[{name:"offset",in:"query",schema:{type:"integer",minimum:0}}],null,200,array(ref("BillingInvoice")),"Administrator invoice history, up to 100 items, newest first.")},
 "/api/billing/payments":{get:op("listBillingPayments",[{name:"offset",in:"query",schema:{type:"integer",minimum:0}}],null,200,array(ref("BillingPayment")),"Verified payments, including unallocated transfers and separate test events; up to 100 newest items. No signatures or credential values.")},
 '/api/billing/plans':{get:op('listBillingPlans',[{name:'cluster_id',in:'query',schema:string}],null,200,array(ref('BillingPlan')),'Admin tariff catalogue, including disabled plans; max 1000.'),post:op('createBillingPlan',[],ref('BillingPlanCreate'),201,ref('BillingPlan'),'Create explicit cluster-bound tariff. Does not activate proxy access.')},
 '/api/billing/plans/{plan_id}':{get:op('getBillingPlan',[param('plan_id')],null,200,ref('BillingPlan'),'Current terms and revision.'),put:op('updateBillingPlan',[param('plan_id'),match],ref('BillingPlanUpdate'),204,null,'New immutable version. Existing invoices and periods retain original terms.')},
 '/api/billing/payment-accounts':{get:op('listPaymentAccounts',[],null,200,array(ref('PaymentAccount')),'Admin payment connections, env names only; max 64.'),post:op('createPaymentAccount',[],ref('PaymentAccountCreate'),201,ref('PaymentAccount'),'Immutable provider, merchant and environment. TELEMT_PAYMENT_* credential references. Creation does not verify provider access.')},
 '/api/billing/payment-accounts/{account_id}':{get:op('getPaymentAccount',[param('account_id')],null,200,ref('PaymentAccount'),'Connection without resolved secrets.'),put:op('updatePaymentAccount',[param('account_id'),match],ref('PaymentAccountUpdate'),204,null,'Change name and new-checkout availability. In-flight callbacks still accepted when disabled.')},
 '/api/payments/{account_id}/notify':{post:{operationId:'receivePaymentNotification',tags:['billing'],security:[],parameters:[param('account_id')],description:'Public signed provider callback. Raw form body, no query parameters or duplicates. Configure provider POST. Acknowledges only durable commit. Test payments never create real paid periods; redirects cannot settle payments.',requestBody:{required:true,content:{'application/x-www-form-urlencoded':{schema:{type:'object',additionalProperties:{type:'string'}}}}},responses:{'200':{description:'Robokassa OK<InvId> or YooMoney OK, including durable replay.',content:{'text/plain':{schema:string}}},...errors,'415':response('Form content type required.',ref('Error'))}}},
};
const planUpdate=object({name:string,enabled:boolean,terms:ref('BillingPlanTerms')});
const accountUpdate=object({name:string,enabled:boolean});
const accountCreate=object({name:string,provider:{type:'string',enum:['robokassa','yoomoney']},test:boolean,enabled:boolean,config:ref('PaymentAccountConfig')});
const schemas={
 BillingInvoice:object({id:string,number:decimal,customer_id:string,subscription_id:string,account_id:string,plan_id:string,plan_version:integer,currency:string,currency_scale:{type:"integer"},amount_minor:decimal,test:boolean,status:{type:"string",enum:["pending","paid","test_paid"]},terms:ref("BillingPlanTerms"),created_at:integer,paid_at:{type:["integer","null"],format:"int64"}}),
 BillingPayment:object({id:string,account_id:string,provider:{type:"string",enum:["robokassa","yoomoney"]},external_id:string,customer_id:string,invoice_id:string,gross_minor:decimal,net_minor:{...decimal,type:["string","null"]},currency:string,test:boolean,kind:{type:"string",enum:["service_payment","unallocated_payment","test_payment"]},payment_at:integer,time_source:{type:"string",enum:["provider","verified_notification"]},received_at:integer}),
 BillingPlanTerms:object({currency:{type:'string',pattern:'^[A-Z]{3}$'},currency_scale:{type:'integer',minimum:0,maximum:6},price_minor:decimal,duration_seconds:decimal,quota_bytes:{...decimal,type:['string','null']},overrun_bytes:{...decimal,type:['string','null'],description:'Explicit tolerance required for finite quota; null for unlimited. Enforcement requires separate provisioning.'},node_policy:ref('ClusterIdentityPolicy')}),
 BillingPlanUpdate:planUpdate,BillingPlanCreate:object({cluster_id:string,...planUpdate.properties}),BillingPlan:object({id:string,cluster_id:string,...planUpdate.properties,revision:integer,version:integer,created_at:integer,updated_at:integer}),
 ServiceReceipt:object({name:string,tax:string,payment_method:string,tax_system:string}),
 PaymentAccountConfig:object({merchant:string,algorithm:{type:'string',enum:['','MD5','SHA256']},password1_env:string,password2_env:string,wallet:string,notification_secret_env:string,receipt:{anyOf:[ref('ServiceReceipt'),{type:'null'}]}},[]),
 PaymentAccountUpdate:accountUpdate,PaymentAccountCreate:accountCreate,PaymentAccount:object({id:string,...accountCreate.properties,revision:integer,created_at:integer,updated_at:integer}),
};
function replaceBlock(name,indent,value,before){
 const start=indent+'# '+name+' start\n',end=indent+'# '+name+' end\n';
 const a=source.indexOf(start);if(a>=0){const b=source.indexOf(end,a);if(b<0)throw Error('missing end');source=source.slice(0,a)+source.slice(b+end.length);}
 if(!source.includes(before))throw Error('missing marker');
 const text=dump(value,{lineWidth:110,noRefs:true}).trimEnd().split('\n').map(line=>indent+line).join('\n')+'\n';source=source.replace(before,start+text+end+before);
}
replaceBlock('billing paths','  ',paths,'  /api/nodes:');replaceBlock('billing schemas','    ',schemas,'    ConfigTree:');
for(const code of ['payment_invalid','payment_signature_invalid','payment_configuration_unavailable']){if(!load(source).components.schemas.Error.properties.code.enum.includes(code))source=source.replace(/^(\s*)- config_graph_limit\s*$/m,'$1- config_graph_limit\n$1- '+code);}
fs.writeFileSync(file,source);
