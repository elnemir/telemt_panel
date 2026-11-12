// Generate the private agent surface from the shared response schemas.
import fs from 'node:fs';
import { load, dump } from 'js-yaml';
const panel = load(fs.readFileSync('../api/openapi.yaml', 'utf8'));
const identity = { name: 'X-Telemt-Agent-ID', in: 'header', required: true, schema: { type: 'string', minLength: 16, maxLength: 128 } };
const error = { description: 'Rejected, unavailable, or unconfirmed; inspect code before retrying.', content: { 'application/json': { schema: { $ref: './openapi.yaml#/components/schemas/Error' } } } };
const paths = {};
function route(path, method, operation) {
  paths[path] ??= { parameters: [identity] };
  const op = structuredClone(operation);
  op.security = [{ agentAuthorization: [] }];
  op.tags = ['agent'];
  op.operationId = 'agent' + op.operationId[0].toUpperCase() + op.operationId.slice(1);
  op.parameters = (op.parameters ?? []).filter(p => p.name !== 'node_id');
  for (const p of op.parameters) if (p.name === 'service') p.schema = { type: 'string', enum: ['telemt'] };
  for (const code of ['400', '401', '409', '503']) op.responses[code] ??= error;
  // Reuse public DTOs, never central cookie security.
  const serialized = JSON.stringify(op).replaceAll('"#/components/', '"./openapi.yaml#/components/');
  paths[path][method] = JSON.parse(serialized);
}
route('/v1/agent/health', 'get', {operationId:'health',responses:{'200':{description:'Authenticated installed identity',content:{'application/json':{schema:{type:'object',required:['agent_id','version'],properties:{agent_id:{type:'string'},version:{type:'string'}}}}}}}});
route('/v1/agent/host', 'get', panel.paths['/api/host'].get);
route('/v1/agent/service', 'get', panel.paths['/api/telemt/service'].get);
for (const action of ['start','stop','restart']) {
 const op = structuredClone(panel.paths[`/api/telemt/${action}`].post);
 op.description = 'Fixed locally configured Telemt service action. Intent is durable before execution. Reusing a key returns the saved result; an unresolved intent is never rerun. 202 does not establish runtime readiness.';
 op.parameters ??= [];
 op.parameters.push({name:'Idempotency-Key',in:'header',required:true,schema:{type:'string',pattern:'^[0-9]{10,12}-[0-9a-f]{32}$'},description:'Unix seconds plus 128-bit random hex; valid 24 hours, at most 60 seconds future skew.'});
 route(`/v1/agent/service/${action}`,'post',op);
}
route('/v1/agent/commands/{key}','get',panel.paths['/api/nodes/{node_id}/manage/agent/commands/{key}'].get);
route('/v1/agent/logs/tail','get',panel.paths['/api/logs/tail'].get);
for(const method of ['get','head']) route('/v1/agent/events/logs',method,panel.paths['/api/events/logs'][method]);
for (const [path, methods] of Object.entries(panel.paths)) {
 const prefix = '/api/nodes/{node_id}/manage/telemt/config/files';
 if (!path.startsWith(prefix)) continue;
 for (const method of ['get','post','delete']) if (methods[method]) {
  const op = structuredClone(methods[method]);
  for (const p of op.parameters ?? []) if (p.name === 'Idempotency-Key') p.required = true;
  route('/v1/agent/config' + path.slice(prefix.length),method,op);
 }
}
const spec = {
 openapi:'3.1.0',
 info:{title:'Telemt node agent private API',version:'1',description:'Separate authenticated agent process. All routes require exact Authorization and expected identity headers. Responses include X-Telemt-Agent-ID. No redirects, arbitrary commands, panel login, or SPA. See docs/NODE_AGENT.md.'},
 security:[{agentAuthorization:[]}],
 paths,
 components:{securitySchemes:{agentAuthorization:{type:'apiKey',in:'header',name:'Authorization',description:'Exact value from agent.authorization_env, at least 32 characters.'}}},
};
fs.writeFileSync('../api/agent-openapi.yaml', dump(spec,{lineWidth:110,noRefs:true}));
