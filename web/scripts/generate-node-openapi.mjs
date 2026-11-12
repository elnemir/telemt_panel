import fs from 'node:fs';
import {configFilePaths} from './node-config-contract.mjs';
import {createRequire} from 'node:module';
const require=createRequire(process.cwd()+'/package.json');const {load,dump}=require('js-yaml');
const file='../api/openapi.yaml';let source=fs.readFileSync(file,'utf8');const spec=load(source);
spec.paths['/api/agent/commands/{key}'] = {
  get: {
    operationId: 'getAgentCommand',
    parameters: [{ name: 'key', in: 'path', required: true, schema: { type: 'string', pattern: '^[0-9]{10,12}-[0-9a-f]{32}$' } }],
    responses: {
      '200': {
        description: 'Persisted command outcome; status 0 means unknown intent, not success.',
        content: { 'application/json': { schema: { $ref: '#/components/schemas/AgentCommand' } } },
      },
    },
  },
};
Object.assign(spec.paths, configFilePaths);
const routes=fs.readFileSync('../internal/httpapi/node_routes.go','utf8');const added={};
const pairs=[...routes.matchAll(/mux.HandleFunc\("(GET|POST|PUT|PATCH|DELETE|HEAD) (\/api\/[^\"]+)", ([^)]+)\)/g)].map(m=>[m[1].toLowerCase(),m[2],m[3]]);
for(const path of ['/api/updates','/api/updates/{target}/apply','/api/updates/auto'])for(const method of Object.keys(spec.paths[path]??{}))if(['get','post','put','head'].includes(method))pairs.push([method,path,'nodeOperationUnavailable']);
for(const [method,path,handler]of pairs){
 const original=spec.paths[path]?.[method];if(!original)throw Error('Undocumented source route '+method+' '+path);
 const target='/api/nodes/{node_id}/manage'+path.slice(4);
 const op=structuredClone(original);op.operationId='managed'+op.operationId[0].toUpperCase()+op.operationId.slice(1);op.tags=['nodes'];
 op.description=('Node-scoped management. Uses only the selected node client, history, jobs and SSE. Legacy default history stays with default. '+(op.description??'')).trim();
 op.responses??={};
 for(const code of ['404','409','503'])op.responses[code]={description:'Node missing, disabled, retired or its credentials/storage unavailable. No fallback to another node.',content:{'application/json':{schema:{$ref:'#/components/schemas/Error'}}}};
 if(handler==='nodeOperationUnavailable'){
  op.description='Unavailable through node management. Local host actions and public subscription routing require a node agent or node-aware subscription service.';
  op.responses={'501':{description:'node_operation_unavailable',content:{'application/json':{schema:{$ref:'#/components/schemas/Error'}}}},'404':op.responses['404'],'409':op.responses['409'],'503':op.responses['503']};
 }
 if(handler==='s.handleNodeServiceAction'){op.description+=' Requires configured agent. Optional Idempotency-Key is epoch-seconds plus hyphen plus 32 lowercase hex digits, expires after 24 hours. Reuse the same key to inspect/replay a request; an unconfirmed intent never executes again.';op.parameters??=[];op.parameters.push({name:'Idempotency-Key',in:'header',schema:{type:'string'}});}
 if(path==='/api/events')op.description+=' SSE event IDs are node ID + runtime generation + sequence. A foreign or retired cursor starts with snapshots instead of replay.';
 if(path==='/api/host')op.description='Agent host capabilities when bound; otherwise all host operations false. Global panel host information is never exposed as remote node information.';
 added[target]??={parameters:[{name:'node_id',in:'path',required:true,schema:{type:'string',pattern:'^(default|n_[0-9a-f]{32})$'}}]};
 added[target][method]=op;
}
const start=source.indexOf('  /api/nodes/{node_id}/manage/');if(start!==-1){const end=source.indexOf('  /api/branding:',start);source=source.slice(0,start)+source.slice(end);}
const block=dump(added,{lineWidth:110,noRefs:true}).trimEnd().split('\n').map(x=>'  '+x).join('\n')+'\n';
source=source.replace('  /api/branding:',block+'  /api/branding:');
source=source.replace('              - node_upstream_timeout','              - node_upstream_timeout\n              - node_operation_unavailable');
// Respect the existing enum indentation without assuming it.
if(!load(source).components.schemas.Error.properties.code.enum.includes('node_operation_unavailable'))source=source.replace(/^(\s*)- node_upstream_timeout\s*$/m,'$1- node_upstream_timeout\n$1- node_operation_unavailable');
fs.writeFileSync(file,source);
console.log('Documented management operations:',pairs.length);
