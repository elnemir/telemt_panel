import {useEffect,useRef,useState} from "react";
import {useMutation,useQuery} from "@tanstack/react-query";
import {useStrings} from "../i18n";
import {Button} from "../ui/Button";
import {Sheet} from "../ui/Sheet";
import {documentNodeId,nodeStorageKey} from "../nodes/scope";
import {useNow} from "./useNow";
import {apiErrorMessage} from "./apiError";
import {listNodesOptions} from "../lib/api/generated/@tanstack/react-query.gen";
import {executeUserCopy,getUserCopy,prepareUserCopy} from "../lib/api/generated/sdk.gen";
import type {UserCopyOptions,UserCopyPreview,UserCopyResult} from "../lib/api/generated/types.gen";

export function UserCopy({username}:{username:string}) {
 const s=useStrings(),t=s.userCopy,now=useNow();
 const storageKey=nodeStorageKey("telemt-panel:user-copy:"+username,window.location.pathname);
 const [open,setOpen]=useState(false),[targets,setTargets]=useState<string[]>([]);
 const [options,setOptions]=useState<UserCopyOptions>({limits:true,expiration:true,enabled:true,ad_tag:true,overwrite:false});
 const [preview,setPreview]=useState<UserCopyPreview>(),[result,setResult]=useState<UserCopyResult>();
 const [key,setKey]=useState<string>(()=>{try{return localStorage.getItem(storageKey)??"";}catch{return "";}});
 const [error,setError]=useState<unknown>();
 const registry=useQuery({...listNodesOptions(),enabled:open});
 const work=useMutation({mutationFn:async(fn:()=>Promise<void>)=>fn(),onError:(e:unknown)=>setError(e),retry:false});
 const run=(fn:()=>Promise<void>)=>{setError(undefined);work.mutate(fn);};
 const busy=work.isPending,locked=busy||!!key;
 const eligible=registry.data?.nodes.filter(n=>n.enabled&&n.id!==documentNodeId)??[];
 function saveKey(value:string){setKey(value);try{if(value)localStorage.setItem(storageKey,value);else localStorage.removeItem(storageKey);}catch{/* The displayed key remains available for manual recovery. */}}
 async function inspect(savedKey:string){
  const {data}=await getUserCopy({path:{key:savedKey},throwOnError:true});
  if(data.status===200&&data.body&&typeof data.body==="object"&&"targets" in data.body&&Array.isArray(data.body["targets"])){setResult(data.body as UserCopyResult);}
  else if(data.status>=400){setError(data.body);}
 }
 const resultRef=useRef<HTMLDivElement>(null);
 useEffect(()=>{if(open&&result)resultRef.current?.focus();},[open,result]);
 const rows=result?.targets??preview?.targets;
 const outcomes=rows&&<ul className="mt-3 flex flex-col gap-3">{rows.map(row=><li key={row.node_id} className="rounded-lg border border-border p-3"><strong className="break-all">{registry.data?.nodes.find(n=>n.id===row.node_id)?.name??row.node_id}</strong><p>{t.states[row.state]}</p>{!result&&<small>{row.exists?t.existing:t.newUser}</small>}{row.code&&<code className="block break-all text-xs">{row.code}</code>}{row.state==="persisted"&&<p className="text-sm">{row.in_runtime?t.runtime:t.noRuntime}</p>}</li>)}</ul>;
 return <section className="user-section"><h2>{t.title}</h2><p className="user-note">{t.description}</p>
  <Button variant="secondary" onClick={()=>{setOpen(true);if(key)run(()=>inspect(key));}}>{t.title}</Button>
  <Sheet open={open} title={t.title} onClose={()=>{if(!busy&&(!key||window.confirm(t.closeConfirm)))setOpen(false);}}>
   <p className="mb-3 text-sm text-text-muted">{username} · {t.description}</p>
   {registry.error&&<p role="alert">{apiErrorMessage(registry.error,s)}</p>}
   {result&&<div ref={resultRef} tabIndex={-1} role="status">{outcomes}</div>}
   <fieldset disabled={locked} className="flex flex-col gap-3"><legend>{t.targets}</legend>
    {eligible.map(n=><label key={n.id} className="flex gap-2"><input type="checkbox" checked={targets.includes(n.id)} disabled={!targets.includes(n.id)&&targets.length>=8} onChange={e=>{setTargets(e.target.checked?[...targets,n.id]:targets.filter(id=>id!==n.id));setPreview(undefined);}}/>{n.name}</label>)}
    {!registry.isPending&&eligible.length===0&&<p>{t.empty}</p>}
    {(["limits","expiration","enabled","ad_tag","overwrite"] as const).map(option=><label key={option} className="flex gap-2"><input type="checkbox" checked={options[option]} onChange={e=>{setOptions({...options,[option]:e.target.checked});setPreview(undefined);}}/>{t[option]}</label>)}
   </fieldset>
   <p className="my-3 text-sm text-text-muted">{t.defaults}</p>
   <div className="flex flex-wrap gap-2">
    {!key&&<Button disabled={busy||targets.length===0} onClick={()=>run(async()=>{const {data}=await prepareUserCopy({body:{source_id:documentNodeId,username,targets,options},throwOnError:true});setPreview(data);setResult(undefined);})}>{t.preview}</Button>}
    {preview&&!result&&<Button disabled={busy||(!key&&Date.parse(preview.expires_at)<=now)||!preview.targets.some(x=>x.state==="ready")} onClick={()=>run(async()=>{
      const next=key||`${Math.floor(Date.now()/1000)}-${crypto.randomUUID().replaceAll("-","")}`;saveKey(next);
      const {data}=await executeUserCopy({body:{token:preview.token},headers:{"Idempotency-Key":next},throwOnError:true});setResult(data);
    })}>{key?t.retry:t.execute}</Button>}
    {key&&<><Button variant="secondary" disabled={busy} onClick={()=>run(()=>inspect(key))}>{t.status}</Button><Button variant="secondary" disabled={busy} onClick={()=>{if((result&&result.targets.every(row=>row.state!=="unknown"&&row.state!=="unconfirmed"))||window.confirm(t.resetConfirm)){saveKey("");setPreview(undefined);setResult(undefined);setError(undefined);}}}>{t.reset}</Button></>}
   </div>
   {key&&<div className="my-3 rounded-lg border border-border p-3 text-sm"><code className="break-all">{key}</code>{!result&&<p>{t.unknown}</p>}</div>}
   {preview&&<><p className="my-3 text-sm">{t.expires}: {new Date(preview.expires_at).toLocaleTimeString()}</p><details><summary>{t.fields}</summary><pre className="overflow-auto text-xs">{JSON.stringify(preview.fields,null,2)}</pre></details></>}
   {!result&&outcomes}
   {error!==undefined&&<p role="alert" className="mt-3 text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
  </Sheet>
 </section>;
}
