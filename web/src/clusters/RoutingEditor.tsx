import {useState} from "react";
import {useBlocker} from "@tanstack/react-router";
import {useQuery,useMutation} from "@tanstack/react-query";
import {useStrings} from "../i18n";
import {apiErrorMessage} from "../people/apiError";
import {Button} from "../ui/Button";
import {Input} from "../ui/Input";
import {Select} from "../ui/Select";
import {Sheet} from "../ui/Sheet";
import {getClusterRoutingOptions} from "../lib/api/generated/@tanstack/react-query.gen";
import {putClusterRouting,exportClusterRouting} from "../lib/api/generated/sdk.gen";
import type {IngressConfig,ClusterMember,ClusterRouting} from "../lib/api/generated/types.gen";
type Props={clusterID:string;members:ClusterMember[];close:()=>void;saved:()=>Promise<void>};
export function RoutingEditor(props:Props){
 const s=useStrings();const query=useQuery(getClusterRoutingOptions({path:{cluster_id:props.clusterID}}));
 if(query.data)return <RoutingForm {...props} initial={query.data}/>;
 return <Sheet open title={s.clusters.routing.title} onClose={props.close}>
 {query.error&&<p role="alert">{apiErrorMessage(query.error as {code?:unknown},s)}</p>}
 {!query.error&&<p>{s.clusters.loading}</p>}
 </Sheet>;
}
function RoutingForm({clusterID,members,initial,close,saved}:Props&{initial:ClusterRouting}){
 const s=useStrings(),t=s.clusters.routing;
 const initialConfig:IngressConfig={domain:initial.config?.domain??"",native_port:initial.config?.native_port??8443,web_port:443,native_balance:initial.config?.native_balance??"leastconn",targets:members.filter(m=>m.state==="active").map(m=>({...initial.config?.targets.find(v=>v.node_id===m.node_id),node_id:m.node_id,native_address:initial.config?.targets.find(v=>v.node_id===m.node_id)?.native_address??"",web_address:initial.config?.targets.find(v=>v.node_id===m.node_id)?.web_address??"",weight:m.weight,drain:m.drain}))};
 const [config,setConfig]=useState<IngressConfig>(initialConfig);
 const [revision,setRevision]=useState(initial.revision);
 const [savedConfig,setSavedConfig]=useState(initial.config?JSON.stringify(initial.config):"");
 const [notice,setNotice]=useState(false);
 const dirty=JSON.stringify(config)!==savedConfig;
 const save=useMutation({retry:false,mutationFn:async()=>{await putClusterRouting({path:{cluster_id:clusterID},headers:{"If-Match":String(revision)},body:config,throwOnError:true});setRevision(revision+1);setSavedConfig(JSON.stringify(config));setNotice(true);await saved();}});
 const download=useMutation({retry:false,mutationFn:async()=>{const result=await exportClusterRouting({path:{cluster_id:clusterID},headers:{"If-Match":String(revision)},throwOnError:true});const url=URL.createObjectURL(new Blob([result.data],{type:"text/plain"}));const a=document.createElement("a");a.href=url;a.download="haproxy.cfg";a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}});
 const busy=save.isPending||download.isPending;
 useBlocker({shouldBlockFn:()=>busy||(dirty&&!window.confirm(s.clusters.discard)),enableBeforeUnload:dirty||busy});
 const error=save.error??download.error;
 const cancel=()=>{if(!busy&&(!dirty||window.confirm(s.clusters.discard)))close();};
 return <Sheet open title={t.title} onClose={cancel}><form className="space-y-4" onSubmit={e=>{e.preventDefault();save.mutate();}}>
 <p className="text-sm text-text-muted">{t.hint}</p>
 <fieldset disabled={busy} className="space-y-4">
 <label className="block">{t.domain}<Input required maxLength={253} placeholder="proxy.example.com" value={config.domain} onChange={e=>setConfig({...config,domain:e.target.value.toLowerCase()})}/></label>
 <div className="grid grid-cols-2 gap-3">{(["native_port","web_port"] as const).map(k=><label key={k}>{t[k]}<Input type="number" required readOnly={k==="web_port"} min={1} max={65535} value={config[k]} onChange={e=>setConfig({...config,[k]:Number(e.target.value)})}/></label>)}</div>
 <label className="block">{t.algorithm}<Select value={config.native_balance} onChange={e=>setConfig({...config,native_balance:e.target.value as IngressConfig["native_balance"]})}>{(["leastconn","roundrobin","source"] as const).map(k=><option key={k} value={k}>{t.algorithms[k]}</option>)}</Select></label>
 {config.targets.length===0&&<p>{t.noMembers}</p>}
 {config.targets.map((node,index)=><fieldset key={node.node_id} className="rounded-lg border border-border p-3 space-y-3"><legend>{s.customer.server} · {node.node_id.slice(-8)}</legend>
 {(["native_address","web_address"] as const).map(k=><label className="block" key={k}>{t[k]}<Input required placeholder={k==="native_address"?"192.0.2.1:443":"192.0.2.1:18080"} value={node[k]} onChange={e=>setConfig({...config,targets:config.targets.map((v,i)=>i===index?{...v,[k]:e.target.value}:v)})}/></label>)}
 <label className="block">{t.weight}<Input required type="number" min={1} max={256} value={node.weight} onChange={e=>setConfig({...config,targets:config.targets.map((v,i)=>i===index?{...v,weight:Number(e.target.value)}:v)})}/></label>
 <label className="flex gap-2"><input type="checkbox" checked={node.drain} onChange={e=>setConfig({...config,targets:config.targets.map((v,i)=>i===index?{...v,drain:e.target.checked}:v)})}/>{t.drain}</label>
 </fieldset>)}
 </fieldset>
 {error&&<p role="alert" className="text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
 {notice&&<p role="status">{t.saved}</p>}
 <div className="flex flex-wrap gap-2"><Button disabled={busy||!dirty||config.targets.length===0}>{s.clusters.save}</Button><Button type="button" variant="secondary" disabled={busy||dirty} onClick={()=>download.mutate()}>{t.download}</Button><Button type="button" variant="secondary" disabled={busy} onClick={cancel}>{s.clusters.cancel}</Button></div>
 </form></Sheet>;
}
