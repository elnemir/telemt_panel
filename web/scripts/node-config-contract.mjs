// Templates for node-only operations: there are deliberately no legacy root
// file routes. generate-node-openapi adds the node scope and common failures.
const ref = name => ({ $ref: '#/components/schemas/' + name });
const response = (description, schema) => ({ description, content: { 'application/json': { schema } } });
const input = { required: true, content: { 'application/json': { schema: ref('ConfigFileInput') } } };
const key = { name: 'key', in: 'path', required: true, schema: { type: 'string', pattern: '^[0-9]{10,12}-[0-9a-f]{32}$' } };
const command = { name: 'Idempotency-Key', in: 'header', schema: { type: 'string' }, description: 'Reuse for the same mutation after ambiguous transport failure. Never auto-retry with a new key.' };
export const configFilePaths = {
 '/api/telemt/config/files': { get: { operationId:'getConfigFiles', description:'Reads the full include source manifest from the installed agent. Contains secrets; do not persist in browser storage or telemetry.', responses:{'200':response('Exact source graph',ref('ConfigFileSnapshot'))} } },
 '/api/telemt/config/files/render': { post: { operationId:'renderConfigFile', description:'Renders typed edits while preserving unrelated comments and include directives. Does not write or validate runtime configuration.', requestBody:input, responses:{'200':response('Rendered candidate',ref('ConfigFileRendered'))} } },
 '/api/telemt/config/files/validate': { post: { operationId:'validateConfigFile', description:'Runs the installed Telemt loader/preparation on one owner override without writing.', requestBody:input, responses:{'200':response('Validated candidate; no readiness guarantee',ref('ConfigFileResult'))} } },
 '/api/telemt/config/files/apply': { post: { operationId:'applyConfigFile', description:'Durable command intent, backup before write, then root-lock/source-graph CAS. One source per transaction; persisted is not runtime convergence.', parameters:[command], requestBody:input, responses:{'200':response('Persisted source revision',ref('ConfigFileResult'))} } },
 '/api/telemt/config/files/backups': { get: { operationId:'listConfigBackups', responses:{'200':response('Backup metadata; content omitted', {type:'array',items:ref('ConfigFileBackup')})} } },
 '/api/telemt/config/files/backups/{key}': {
  get:{operationId:'getConfigBackup',parameters:[key],responses:{'200':response('Sensitive backup content for review; restore through a new revision-checked apply',ref('ConfigFileBackup'))}},
  delete:{operationId:'deleteConfigBackup',parameters:[key,command],responses:{'204':{description:'Selected backup removed; live source unchanged'}}},
 },
};
