import fs from 'node:fs';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url),{load,dump}=require('js-yaml');
const file=new URL('../../api/openapi.yaml',import.meta.url);
let source=fs.readFileSync(file,'utf8');const spec=load(source);
const ref=name=>({$ref:'#/components/schemas/'+name});
const object=(properties,required=Object.keys(properties))=>({type:'object',required,properties});
const string={type:'string'},boolean={type:'boolean'},array=items=>({type:'array',items});
const response=(description,schema)=>({description,content:{'application/json':{schema}}});
const input=schema=>({required:true,content:{'application/json':{schema}}});
const errors=Object.fromEntries(['400','401','403','404','409','503'].map(code=>[code,response('Rejected, unavailable or unconfirmed operation; no automatic retry with a new key.',ref('Error'))]));
const paths={
 '/api/user-copies/prepare':{post:{operationId:'prepareUserCopy',tags:['nodes'],description:'Read-only preview. Same username/secret, selected limits; never copies history, usage or WEB sessions. Preview expires after five minutes.',requestBody:input(ref('UserCopySelection')),responses:{'200':response('Conflict preview without secrets',ref('UserCopyPreview')),...errors}}},
 '/api/user-copies':{post:{operationId:'executeUserCopy',tags:['nodes'],description:'One-shot copy with durable deduplication. 200 contains separate target outcomes; it does not mean all targets succeeded. Unfinished intent is never automatically resumed.',parameters:[{name:'Idempotency-Key',in:'header',required:true,schema:string}],requestBody:input(object({token:string})),responses:{'200':response('Per-target persistence and runtime visibility',ref('UserCopyResult')),...errors}}},
 '/api/user-copies/{key}':{get:{operationId:'getUserCopy',tags:['nodes'],parameters:[{name:'key',in:'path',required:true,schema:string}],responses:{'200':response('Durable journal outcome; status zero means unknown',ref('AgentCommand')),...errors}}},
};
const fields=structuredClone(spec.components.schemas.UserCreate);delete fields.properties.secret;
const schemas={
 UserCopyOptions:object({limits:boolean,expiration:boolean,enabled:boolean,ad_tag:boolean,overwrite:boolean}),
 UserCopySelection:object({source_id:string,username:{type:'string',pattern:'^[A-Za-z0-9_.-]{1,64}$'},targets:{...array(string),minItems:1,maxItems:8,uniqueItems:true},options:ref('UserCopyOptions')}),
 UserCopyFields:fields,
 UserCopyTarget:object({node_id:string,node_revision:{type:'integer',format:'uint64'},revision:string,exists:boolean,state:{type:'string',enum:['ready','skipped','unavailable','persisted','unconfirmed','rejected','unknown']},code:string,in_runtime:boolean},['node_id','node_revision','revision','exists','state','in_runtime']),
 UserCopyPreview:object({token:string,expires_at:{type:'string',format:'date-time'},selection:ref('UserCopySelection'),source_revision:string,source_node_revision:{type:'integer',format:'uint64'},fields:ref('UserCopyFields'),targets:array(ref('UserCopyTarget'))}),
 UserCopyResult:object({source_id:string,username:string,targets:array(ref('UserCopyTarget'))}),
};
function replaceBlock(name,indent,value,before){
 const start=indent+'# '+name+' start\n',end=indent+'# '+name+' end\n';
 const a=source.indexOf(start);if(a>=0){const b=source.indexOf(end,a);if(b<0)throw Error('missing generated block end');source=source.slice(0,a)+source.slice(b+end.length);}
 const text=dump(value,{lineWidth:110,noRefs:true}).trimEnd().split('\n').map(line=>indent+line).join('\n')+'\n';
 source=source.replace(before,start+text+end+before);
}
replaceBlock('user-copy paths','  ',paths,'  /api/nodes:');
replaceBlock('user-copy schemas','    ',schemas,'    ConfigTree:');
for(const code of ['copy_unavailable','copy_source_unavailable','copy_invalid','copy_preview_capacity','copy_preview_expired','copy_source_changed','copy_not_found']){
 if(!load(source).components.schemas.Error.properties.code.enum.includes(code))source=source.replace(/^(\s*)- config_graph_limit\s*$/m,'$1- config_graph_limit\n$1- '+code);
}
fs.writeFileSync(file,source);
