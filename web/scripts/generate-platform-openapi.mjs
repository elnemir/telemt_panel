import fs from 'node:fs';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url),{load,dump}=require('js-yaml');
const file=new URL('../../api/openapi.yaml',import.meta.url);
let source=fs.readFileSync(file,'utf8');
const ref=name=>({$ref:'#/components/schemas/'+name});
const object=(properties,required=Object.keys(properties))=>({type:'object',required,properties});
const string={type:'string'},boolean={type:'boolean'},integer={type:'integer',format:'int64'},array=items=>({type:'array',items});
const response=(description,schema)=>({description,...(schema?{content:{'application/json':{schema}}}:{})});
const input=schema=>({required:true,content:{'application/json':{schema}}});
const errors=Object.fromEntries(['400','401','403','404','409','428','503'].map(code=>[code,response('Rejected or unavailable; fetch the current revision before retrying.',ref('Error'))]));
const match={name:'If-Match',in:'header',required:true,description:'Positive revision of the cluster or identity owning this operation.',schema:string};
const param=name=>({name,in:'path',required:true,schema:string});
const op=(id,parameters,body,code,schema,description)=>({operationId:id,tags:['clusters'],description,parameters,...(body?{requestBody:input(body)}:{}),responses:{[code]:response(description??'Success',schema),...errors}});
const cid=[param('cluster_id')],iid=[param('identity_id')];
const paths={
 '/api/platform':{get:op('getPlatformStatus',[],null,200,ref('PlatformStatus'),'Full-build capability and explicit platform activation.')},
 '/api/clusters':{get:op('listClusters',[],null,200,array(ref('Cluster')),'Global clusters; not affected by selected node.'),post:op('createCluster',[],object({name:string}),201,ref('Cluster'),'Create a cluster with immutable ID.')},
 '/api/clusters/{cluster_id}':{get:op('getCluster',cid,null,200,ref('ClusterDetail'),'Cluster membership and revision.'),put:op('renameCluster',[...cid,match],object({name:string}),204,null,'Rename using the cluster revision.'),delete:op('deleteCluster',[...cid,match],null,204,null,'Delete only an empty cluster with no identities or members.')},
 '/api/clusters/{cluster_id}/routing':{get:op('getClusterRouting',cid,null,200,ref('ClusterRouting'),'Saved desired ingress policy; not an applied status.'),put:op('putClusterRouting',[...cid,match],ref('IngressConfig'),204,null,'Replace complete active-member routing inventory using cluster CAS. Does not deploy ingress.')},
 '/api/clusters/{cluster_id}/routing/export':{get:{...op('exportClusterRouting',[...cid,match],null,200,null,'Export deterministic HAProxy configuration. Reject stale membership; validate and apply separately.'),responses:{200:{description:'HAProxy configuration, not yet applied.',content:{'text/plain':{schema:string}}},...errors}}},
 '/api/clusters/{cluster_id}/members':{post:op('addClusterMember',[...cid,match],object({node_id:string}),204,null,'Join one enabled registered node to this cluster; enqueue non-revoked identities transactionally.')},
 '/api/clusters/{cluster_id}/members/{member_id}':{delete:op('removeClusterMember',[...cid,param('member_id'),match],null,202,null,'Request removal. Disable managed accounts with per-node retirement credentials and verify before releasing membership. Failure keeps ownership reserved.')},
 '/api/clusters/{cluster_id}/identities':{get:op('listClusterIdentities',[...cid,{name:'offset',in:'query',schema:{type:'integer',minimum:0,default:0}}],null,200,array(ref('ClusterIdentity')),'Up to 100 identities ordered by username; use offset for the next page.'),post:op('createClusterIdentity',[...cid,match],ref('ClusterIdentityInput'),201,ref('ClusterIdentity'),'Persist desired identity and delivery jobs. Does not assert successful delivery. If-Match is the cluster revision.')},
 '/api/identities/{identity_id}':{get:op('getClusterIdentity',iid,null,200,ref('ClusterIdentityDetail'),'Per-node desired/applied revisions and readback outcomes, without secrets.'),put:op('updateClusterIdentity',[...iid,match],ref('ClusterIdentityUpdate'),202,null,'Update desired policy, rotate secret or irreversibly revoke identity. Revoke also rotates the credential. Does not terminate all existing transport sessions.')},
 '/api/identities/{identity_id}/retry':{post:op('retryClusterIdentity',[...iid,match],null,202,null,'Schedule re-observation without stealing a live worker lease.')},
};
const nullableLimit={type:['integer','null'],format:'uint64',minimum:0};
const schemas={
 IngressTarget:object({node_id:string,native_address:string,web_address:string,weight:{type:'integer',minimum:1,maximum:256},drain:boolean}),
 IngressConfig:object({domain:string,native_port:{type:'integer',minimum:1,maximum:65535},web_port:{type:'integer',enum:[443],description:'Current Telemt bridge uses HTTPS port 443.'},native_balance:{type:'string',enum:['roundrobin','leastconn','source']},targets:{type:'array',minItems:1,maxItems:64,items:ref('IngressTarget')}}),
 ClusterRouting:object({revision:integer,config:{anyOf:[ref('IngressConfig'),{type:'null'}]}}),
 PlatformStatus:object({available:boolean,enabled:boolean}),
 Cluster:object({id:string,name:string,revision:integer,created_at:integer,updated_at:integer}),
 ClusterMember:object({node_id:string,cluster_id:string,state:{type:'string',enum:['active','removing']},weight:{type:'integer',minimum:1,maximum:1000},drain:boolean}),
 ClusterDetail:object({cluster:ref('Cluster'),members:array(ref('ClusterMember'))}),
 ClusterIdentityPolicy:object({enabled:boolean,max_tcp_conns:nullableLimit,max_unique_ips:nullableLimit,rate_limit_up_bps:nullableLimit,rate_limit_down_bps:nullableLimit,expiration_rfc3339:string,user_ad_tag:string}),
 ClusterIdentity:object({id:string,cluster_id:string,username:string,policy:ref('ClusterIdentityPolicy'),revision:integer,revoked:boolean,billing_managed:boolean,created_at:integer,updated_at:integer}),
 ClusterIdentityInput:object({username:{type:'string',pattern:'^[A-Za-z0-9_.-]{1,64}$'},policy:ref('ClusterIdentityPolicy'),secret:{type:'string',pattern:'^[a-fA-F0-9]{32}$'}},['username','policy']),
 ClusterIdentityUpdate:object({policy:ref('ClusterIdentityPolicy'),rotate_secret:boolean,revoke:boolean}),
 ClusterIdentityJob:object({identity_id:string,node_id:string,desired_revision:integer,applied_revision:integer,status:{type:'string',enum:['pending','running','applied','conflict','unavailable']},attempts:{type:'integer'},next_attempt:integer,last_code:string,in_runtime:boolean,updated_at:integer}),
 ClusterIdentityDetail:object({identity:ref('ClusterIdentity'),jobs:array(ref('ClusterIdentityJob'))}),
};
function replaceBlock(name,indent,value,before){
 const start=indent+'# '+name+' start\n',end=indent+'# '+name+' end\n';
 const a=source.indexOf(start);if(a>=0){const b=source.indexOf(end,a);if(b<0)throw Error('missing block end');source=source.slice(0,a)+source.slice(b+end.length);}
 if(!source.includes(before))throw Error('missing insertion marker');
 const text=dump(value,{lineWidth:110,noRefs:true}).trimEnd().split('\n').map(line=>indent+line).join('\n')+'\n';
 source=source.replace(before,start+text+end+before);
}
replaceBlock('platform paths','  ',paths,'  /api/nodes:');
replaceBlock('platform schemas','    ',schemas,'    ConfigTree:');
for(const code of ['platform_unavailable','platform_not_found','platform_invalid','platform_conflict','user_cluster_owned','node_cluster_owned']){
 if(!load(source).components.schemas.Error.properties.code.enum.includes(code))source=source.replace(/^(\s*)- config_graph_limit\s*$/m,'$1- config_graph_limit\n$1- '+code);
}
fs.writeFileSync(file,source);
