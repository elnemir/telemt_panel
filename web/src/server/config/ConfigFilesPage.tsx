import { useState } from "react";
import { Link, useBlocker } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useStrings } from "../../i18n";
import { apiErrorMessage } from "../../people/apiError";
import { documentNodeId } from "../../nodes/scope";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";
import { Sheet } from "../../ui/Sheet";
import { ServerShell } from "../ServerShell";
import { ConfigTreeEditor, type ConfigPath, type FieldChoice } from "./ConfigTreeEditor";
import type { ConfigFileInput, ConfigFileSource, ConfigTree } from "../../lib/api/generated/types.gen";
import { managedGetConfigFilesOptions, managedListConfigBackupsOptions } from "../../lib/api/generated/@tanstack/react-query.gen";
import { managedApplyConfigFile, managedDeleteConfigBackup, managedGetAgentCommand, managedGetConfigBackup, managedRenderConfigFile, managedValidateConfigFile } from "../../lib/api/generated/sdk.gen";

type Draft = { source: ConfigFileSource; revision: string; content: string; tree?: ConfigTree | null };
const path = {node_id:documentNodeId};
const commandKey = () => `${Math.floor(Date.now()/1000)}-${crypto.randomUUID().replaceAll("-","")}`;

export function ConfigFilesPage() {
  const s=useStrings(), t=s.configFiles;
  // These responses contain secrets. They are never persisted and leave the
  // query cache immediately after unmount, including logout/navigation.
  const query=useQuery({...managedGetConfigFilesOptions({path}),retry:false,gcTime:0,refetchOnWindowFocus:false});
  const backups=useQuery({...managedListConfigBackupsOptions({path}),retry:false,gcTime:0,refetchOnWindowFocus:false});
  const [draft,setDraft]=useState<Draft>();
  const [raw,setRaw]=useState(false);
  const [field,setField]=useState<FieldChoice>();
  const [value,setValue]=useState("");
  const [fieldName,setFieldName]=useState("");
  const [kind,setKind]=useState<ConfigTree["kind"]>("string");
  const [validated,setValidated]=useState<string>();
  const [error,setError]=useState<unknown>();
  const [notice,setNotice]=useState("");
  const [pendingKey,setPendingKey]=useState<string>();
  const [outcome,setOutcome]=useState<number>();
  const first=query.data?.sources[0];
  const active=draft ?? (first && query.data ? {source:first,revision:query.data.revision,content:first.content,tree:first.tree}:undefined);
  const dirty=!!active && active.content!==active.source.content;
  const blocker=useBlocker({shouldBlockFn:()=>dirty||!!field||!!pendingKey,enableBeforeUnload:dirty||!!field||!!pendingKey,withResolver:true});
  const work=useMutation({mutationFn:async(fn:()=>Promise<void>)=>fn(),onError:(e:unknown)=>setError(e),retry:false});
  const busy=work.isPending;
  const editDisabled=busy||!!pendingKey;
  const run=(fn:()=>Promise<void>)=>{setError(undefined);setNotice("");work.mutate(fn);};
  function input(edits?: ConfigFileInput["edits"]):ConfigFileInput {
    if(!active)throw new Error("configuration source unavailable");
    return {revision:active.revision,source_id:active.source.id,content:active.content,...(edits ? {edits}:{})};
  }
  async function render(edits:NonNullable<ConfigFileInput["edits"]>) {
    if(!active)return;
    const {data}=await managedRenderConfigFile({path,body:input(edits),throwOnError:true});
    setDraft({...active,content:data.content,tree:data.tree});setValidated(undefined);setField(undefined);setRaw(false);
  }
  function choose(next:FieldChoice){setField(next);setKind(next.kind);setValue(next.value);setFieldName("");}
  function remove(at:ConfigPath){if(window.confirm(t.removeConfirm))run(()=>render([{path:at,literal:null}]));}
  async function refresh(){
    const latest=await query.refetch();if(latest.error)throw latest.error;
    const source=latest.data?.sources.find(x=>x.id===active?.source.id)??latest.data?.sources[0];
    setDraft(source&&latest.data?{source,content:source.content,tree:source.tree,revision:latest.data.revision}:undefined);setValidated(undefined);setPendingKey(undefined);setOutcome(undefined);setField(undefined);
    await backups.refetch();
  }
  return <ServerShell title={t.title}>
    <p className="px-4 text-sm text-text-muted">{t.description}</p>
    {query.isPending && <p className="p-4">{s.nodes.loading}</p>}
    {query.error && <p role="alert" className="p-4 text-warn">{apiErrorMessage(query.error,s)} {t.installHint}</p>}
    <div className="flex flex-wrap gap-2 px-4">
      <Button variant="secondary" disabled={busy} onClick={()=>{if((!dirty&&!pendingKey&&!field)||window.confirm(t.discardConfirm))run(refresh);}}>{t.refresh}</Button>
      <Link to="/server/platform" className="self-center text-accent">{t.service}</Link>
    </div>
    {active && <section className="flex min-w-0 flex-col gap-4 p-4">
      <label className="text-sm">{t.source}<Select value={active.source.id} disabled={editDisabled} onChange={e=>{
        if(dirty&&!window.confirm(t.discardConfirm))return;
        const source=query.data?.sources.find(x=>x.id===e.target.value);
        if(source&&query.data){setDraft({source,content:source.content,tree:source.tree,revision:query.data.revision});setValidated(undefined);}
      }}>{query.data?.sources.map(source=><option key={source.id} value={source.id}>{source.name}</option>)}</Select></label>
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" disabled={editDisabled} onClick={()=>raw ? run(()=>render([])):setRaw(true)}>{raw?t.structured:t.raw}</Button>
        <Button variant="secondary" disabled={editDisabled} onClick={()=>run(async()=>{await managedValidateConfigFile({path,body:input(),throwOnError:true});setValidated(active.content);setNotice(t.valid);})}>{t.validate}</Button>
        <Button disabled={busy||!dirty||validated!==active.content} onClick={()=>{
          if(!pendingKey&&!window.confirm(t.applyConfirm))return;
          run(async()=>{
            const key=pendingKey??commandKey();setPendingKey(key);setOutcome(undefined);
            await managedApplyConfigFile({path,body:input(),headers:{"Idempotency-Key":key},throwOnError:true});
            await refresh();setNotice(t.persisted);
          });
        }}>{pendingKey?t.retryCommand:t.apply}</Button>
      </div>
      {raw ? <label>{t.raw}<textarea spellCheck={false} className="min-h-96 w-full rounded-lg border border-border bg-surface p-3 font-mono text-sm" disabled={editDisabled} value={active.content} onChange={e=>{setDraft({...active,content:e.target.value,tree:null});setValidated(undefined);}} /></label>
        : active.tree ? <ConfigTreeEditor tree={active.tree} disabled={editDisabled} choose={choose} remove={remove}/>
        : <p className="text-warn">{t.noTree} <Button variant="secondary" onClick={()=>setRaw(true)}>{t.raw}</Button></p>}
      <details className="rounded-lg border border-border p-3"><summary>{t.preview}</summary><div className="mt-3 grid min-w-0 gap-3 lg:grid-cols-2"><div><strong>{t.before}</strong><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all text-xs">{active.source.content}</pre></div><div><strong>{t.after}</strong><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all text-xs">{active.content}</pre></div></div></details>
    </section>}
    {pendingKey && <section className="mx-4 rounded-lg border border-warn p-3 text-sm"><p>{t.commandPending}</p><code className="block break-all">{pendingKey}</code>
      <Button variant="secondary" disabled={busy} onClick={()=>run(async()=>{const {data}=await managedGetAgentCommand({path:{...path,key:pendingKey},throwOnError:true});setOutcome(data.status);})}>{t.inspectCommand}</Button>
      {outcome!==undefined && <p>{outcome===0?t.unknown:`${t.commandStatus}: ${outcome}`}</p>}
    </section>}
    {error!==undefined && <p role="alert" className="px-4 text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
    {notice && <p role="status" className="px-4 text-success">{notice}</p>}
    <section className="m-4 rounded-lg border border-border p-4"><h2 className="font-semibold">{t.backups}</h2><p className="text-sm text-text-muted">{t.backupsHint}</p>
      {backups.error && <p role="alert" className="text-warn">{apiErrorMessage(backups.error,s)}</p>}
      {backups.data?.map(backup=><div key={backup.id} className="mt-3 flex flex-wrap gap-2 border-t border-border pt-3"><span className="min-w-0 flex-1 break-all text-sm">{backup.source.name}<br/>{new Date(backup.created*1000).toLocaleString()}</span>
        <Button size="sm" variant="secondary" disabled={editDisabled} onClick={()=>{if(dirty&&!window.confirm(t.discardConfirm))return;run(async()=>{
          const {data}=await managedGetConfigBackup({path:{...path,key:backup.id},throwOnError:true});
          const source=query.data?.sources.find(x=>x.id===data.source.id);
          if(!source||!query.data)throw {code:"config_source_not_found"};
          setDraft({source,revision:query.data.revision,content:data.source.content,tree:null});setRaw(true);setValidated(undefined);setNotice(t.restoreDraft);
        });}}>{t.restore}</Button>
        <Button size="sm" variant="secondary" disabled={busy} onClick={()=>{if(window.confirm(t.deleteBackupConfirm))run(async()=>{await managedDeleteConfigBackup({path:{...path,key:backup.id},headers:{"Idempotency-Key":commandKey()},throwOnError:true});await backups.refetch();});}}>{t.remove}</Button>
      </div>)}
    </section>
    {field && <Sheet open title={field.add?t.add:t.edit} onClose={()=>{if(!busy)setField(undefined);}}><form className="flex flex-col gap-3" onSubmit={e=>{
      e.preventDefault();
      const at=field.add&&!field.array?[...field.path,fieldName]:field.path;
      const literal=kind==="string"?JSON.stringify(value):kind==="table"?"{}":kind==="array"?"[]":value;
      run(()=>render([{path:at,literal}]));
    }}>
      <code className="break-all text-sm">{field.path.join(" / ")}</code>
      {field.add&&!field.array&&<label>{t.key}<Input required maxLength={256} value={fieldName} onChange={e=>setFieldName(e.target.value)}/></label>}
      <label>{t.type}<Select disabled={!field.add} value={kind} onChange={e=>{const next=e.target.value as ConfigTree["kind"];setKind(next);setValue(next==="boolean"?"false":next==="integer"||next==="float"?"0":"");}}>{Object.entries(t.types).map(([key,label])=><option key={key} value={key}>{label}</option>)}</Select></label>
      {kind==="boolean"?<label><input type="checkbox" checked={value==="true"} onChange={e=>setValue(String(e.target.checked))}/>{t.value}</label>:kind!=="table"&&kind!=="array"&&<label>{t.value}{kind==="string"?<textarea aria-label={t.value} className="min-h-32 w-full rounded-lg border border-border bg-surface p-3 font-mono" value={value} onChange={e=>setValue(e.target.value)}/>:<Input required inputMode={kind==="integer"?"numeric":kind==="float"?"decimal":"text"} value={value} onChange={e=>setValue(e.target.value)}/>}</label>}
      <Button type="submit" disabled={busy}>{t.stageField}</Button>
      {error!==undefined&&<p role="alert" className="text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
    </form></Sheet>}
    {blocker.status==="blocked"&&<Sheet open title={t.unsaved} onClose={()=>blocker.reset()}><p>{t.discardConfirm}</p><div className="mt-4 flex gap-2"><Button onClick={()=>blocker.proceed()}>{t.leave}</Button><Button variant="secondary" onClick={()=>blocker.reset()}>{s.nodes.cancel}</Button></div></Sheet>}
  </ServerShell>;
}
