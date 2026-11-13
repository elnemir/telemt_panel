import {ClusterConfiguration} from "./ClusterConfiguration";
import {RoutingEditor} from "./RoutingEditor";
import {useState} from "react";
import {useBlocker} from "@tanstack/react-router";
import {useMutation,useQuery} from "@tanstack/react-query";
import {useStrings} from "../i18n";
import {apiErrorMessage} from "../people/apiError";
import {Button} from "../ui/Button";
import {Input} from "../ui/Input";
import {Select} from "../ui/Select";
import {Sheet} from "../ui/Sheet";
import {PageHeader} from "../ui/PageHeader";
import {getPlatformStatusOptions,listClustersOptions,getClusterOptions,listClusterIdentitiesOptions,getClusterIdentityOptions,listNodesOptions} from "../lib/api/generated/@tanstack/react-query.gen";
import {createCluster,renameCluster,deleteCluster,addClusterMember,removeClusterMember,createClusterIdentity,updateClusterIdentity,retryClusterIdentity} from "../lib/api/generated/sdk.gen";
import type {ClusterIdentity,ClusterIdentityPolicy,ClusterMember} from "../lib/api/generated/types.gen";

export function ClustersPage() {
 const s=useStrings(),t=s.clusters;
 const status=useQuery(getPlatformStatusOptions());
 const clusters=useQuery({...listClustersOptions(),enabled:status.data?.enabled===true,refetchInterval:5000});
 const nodes=useQuery(listNodesOptions());
 const [selected,setSelected]=useState(""),[name,setName]=useState("");
 const id=selected||clusters.data?.[0]?.id||"";
 const cluster=useQuery({...getClusterOptions({path:{cluster_id:id}}),enabled:!!id,refetchInterval:5000});
 const [rename,setRename]=useState<{id:string; revision:number; name:string}>();
 const [offset,setOffset]=useState(0),[node,setNode]=useState("");
 const identities=useQuery({...listClusterIdentitiesOptions({path:{cluster_id:id},query:{offset}}),enabled:!!id,refetchInterval:5000});
 const [identityID,setIdentityID]=useState("");
 const identity=useQuery({...getClusterIdentityOptions({path:{identity_id:identityID}}),enabled:!!identityID,refetchInterval:3000});
 const [routing,setRouting]=useState<{clusterID:string;members:ClusterMember[]}>();
 const [editor,setEditor]=useState<{clusterID:string;revision:number;identity?:ClusterIdentity}>();
 const [error,setError]=useState<unknown>();
 const work=useMutation({mutationFn:async(fn:()=>Promise<void>)=>fn(),retry:false,onError:(e:unknown)=>setError(e)});
 const refresh=async()=>{await Promise.all([clusters.refetch(),...(id?[cluster.refetch(),identities.refetch()]:[]),...(identityID?[identity.refetch()]:[])]);};
 const run=(fn:()=>Promise<void>)=>{setError(undefined);work.mutate(async()=>{try{await fn();}finally{await refresh();}});};
 const busy=work.isPending,c=cluster.data?.cluster;
 const headers={"If-Match":String(c?.revision??0)};
 const choose=(value:string)=>{setSelected(value);setOffset(0);setIdentityID("");setNode("");setError(undefined);};
 const err=error??status.error??clusters.error??cluster.error??identities.error??identity.error;
 return <div className="w-full space-y-6">
  <PageHeader title={t.title} description={t.description}/>
  {err!=null&&<p role="alert" className="text-warn">{apiErrorMessage(err as {code?:unknown},s)}</p>}
  {status.isPending&&<p>{t.loading}</p>}
  {status.data&&!status.data.enabled&&<section className="workspace-card"><h2 className="font-semibold">{t.setup}</h2><p>{status.data.available?t.setupFull:t.setupLite}</p></section>}
  {status.data?.enabled&&<>
   <section className="workspace-card space-y-4">
    <form className="flex flex-wrap items-end gap-3" onSubmit={e=>{e.preventDefault();run(async()=>{const {data}=await createCluster({body:{name},throwOnError:true});choose(data.id);setName("");});}}>
     <label className="min-w-0 flex-1">{t.name}<Input required disabled={busy} maxLength={128} value={name} onChange={e=>setName(e.target.value)}/></label><Button disabled={busy||!name.trim()}>{t.create}</Button>
    </form>
    {!!clusters.data?.length && <ul className="divide-y divide-border" aria-label={t.select}>
      {clusters.data.map(item => <li key={item.id} className="py-1">
        <button type="button" disabled={busy} aria-pressed={item.id===id}
          className="tap-target flex w-full flex-wrap items-center justify-between gap-2 rounded-lg px-3 text-left hover:bg-surface-2 aria-pressed:bg-[rgb(var(--lavender))]"
          onClick={()=>choose(item.id)}>
          <strong className="min-w-0 break-all">{item.name}</strong>
          <span className="text-sm text-text-muted">{t.revision} {item.revision}</span>
        </button>
      </li>)}
    </ul>}
    {!clusters.isPending&&clusters.data?.length===0&&<p>{t.empty}</p>}
   </section>
   {c&&<>
    <section className="workspace-card space-y-4">
     <div className="flex flex-wrap justify-between gap-3"><h2 className="text-lg font-semibold">{c.name}</h2><div className="flex flex-wrap gap-2">
      <Button variant="secondary" disabled={busy} onClick={()=>setRename({id:c.id,revision:c.revision,name:c.name})}>{t.rename}</Button>
      <Button variant="secondary" disabled={busy||!!cluster.data?.members.length||!!identities.data?.length||offset>0} onClick={()=>{if(window.confirm(t.deleteConfirm))run(async()=>{await deleteCluster({path:{cluster_id:c.id},headers,throwOnError:true});choose("");});}}>{t.delete}</Button>
     </div></div>
     <p className="text-sm text-text-muted">{t.revision}: {c.revision}</p>
     <Button variant="secondary" disabled={busy} onClick={()=>setRouting({clusterID:c.id,members:cluster.data?.members??[]})}>{t.routing.title}</Button>
     <h3 className="font-medium">{t.members}</h3>
     <p className="text-sm text-text-muted">{t.memberHint}</p>
     <ul className="space-y-2">{cluster.data?.members.map(m=><li key={m.node_id} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border p-3">
      <span>{nodes.data?.nodes.find(n=>n.id===m.node_id)?.name??m.node_id} · {m.state==="removing"?t.removing:t.active}</span>
      <Button variant="secondary" size="sm" disabled={busy||m.state==="removing"} onClick={()=>{if(window.confirm(t.removeConfirm))run(async()=>{await removeClusterMember({path:{cluster_id:c.id,member_id:m.node_id},headers,throwOnError:true});});}}>{t.remove}</Button>
     </li>)}</ul>
     <div className="flex flex-wrap items-end gap-3"><label className="min-w-0 flex-1">{t.node}<Select aria-label={t.node} disabled={busy} value={node} onChange={e=>setNode(e.target.value)}><option value="">{t.chooseNode}</option>{nodes.data?.nodes.filter(n=>n.enabled&&!cluster.data?.members.some(m=>m.node_id===n.id)).map(n=><option key={n.id} value={n.id}>{n.name}</option>)}</Select></label><Button disabled={busy||!node} onClick={()=>run(async()=>{await addClusterMember({path:{cluster_id:c.id},headers,body:{node_id:node},throwOnError:true});setNode("");})}>{t.addNode}</Button></div>
    </section>
    <ClusterConfiguration key={c.id} clusterID={c.id} revision={c.revision} members={cluster.data?.members??[]} names={new Map(nodes.data?.nodes.map(item=>[item.id,item.name]))} changed={refresh}/>
    <section className="workspace-card space-y-4">
     <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-lg font-semibold">{t.identities}</h2><Button disabled={busy} onClick={()=>setEditor({clusterID:c.id,revision:c.revision})}>{t.newIdentity}</Button></div>
     <p className="text-sm text-text-muted">{t.identityHint}</p>
     <ul className="space-y-2">{identities.data?.map(i=><li key={i.id}><button type="button" className="w-full rounded-lg border border-border p-3 text-left hover:bg-surface-2" onClick={()=>setIdentityID(i.id)}><strong>{i.username}</strong><span className="ml-3 text-sm text-text-muted">{i.revoked?t.revoked:i.policy.enabled?t.enabled:t.disabled} · {t.revision} {i.revision}</span></button></li>)}</ul>
     {!identities.isPending&&identities.data?.length===0&&<p>{t.noIdentities}</p>}
     <div className="flex gap-2"><Button variant="secondary" disabled={offset===0||busy} onClick={()=>setOffset(Math.max(0,offset-100))}>{t.previous}</Button><Button variant="secondary" disabled={identities.data?.length!==100||busy} onClick={()=>setOffset(offset+100)}>{t.next}</Button></div>
    </section>
    {identity.data&&identity.data.identity.cluster_id===c.id&&<section className="workspace-card space-y-4" aria-label={t.delivery}>
     <h2 className="text-lg font-semibold">{identity.data.identity.username} · {t.delivery}</h2>
     <p className="text-sm text-text-muted">{t.deliveryHint}</p>
     <div className="flex flex-wrap gap-2"><Button disabled={busy||identity.data.identity.revoked} onClick={()=>setEditor({clusterID:c.id,revision:c.revision,identity:identity.data!.identity})}>{t.edit}</Button><Button variant="secondary" disabled={busy} onClick={()=>run(async()=>{await retryClusterIdentity({path:{identity_id:identityID},headers:{"If-Match":String(identity.data!.identity.revision)},throwOnError:true});})}>{t.retry}</Button></div>
     {identity.data.jobs.length===0&&<p>{t.noJobs}</p>}
     <ul className="grid gap-3 md:grid-cols-2">{identity.data.jobs.map(j=><li key={j.node_id} className="rounded-lg border border-border p-3">
      <strong>{nodes.data?.nodes.find(n=>n.id===j.node_id)?.name??j.node_id}</strong><p>{t.states[j.status]}</p><p>{t.desired}: {j.desired_revision} · {t.applied}: {j.applied_revision}</p><p className="text-sm">{j.in_runtime?t.inRuntime:t.noRuntime}</p>{j.last_code&&<code className="break-all text-xs">{j.last_code}</code>}
     </li>)}</ul>
    </section>}
   </>}
  </>}
  {rename && <Sheet open title={t.rename} onClose={()=>{if(!busy)setRename(undefined);}}>
    <form className="space-y-4" onSubmit={event=>{
      event.preventDefault();
      run(async()=>{
        await renameCluster({path:{cluster_id:rename.id},headers:{"If-Match":String(rename.revision)},body:{name:rename.name},throwOnError:true});
        setRename(undefined);
      });
    }}>
      <label className="block">{t.name}<Input required maxLength={128} value={rename.name} disabled={busy}
        onChange={event=>setRename({...rename,name:event.target.value})}/></label>
      {error!=null&&<p role="alert" className="text-warn">{apiErrorMessage(error as {code?:unknown},s)}</p>}
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy||!rename.name.trim()}>{t.rename}</Button>
        <Button type="button" variant="secondary" disabled={busy} onClick={()=>setRename(undefined)}>{s.nodes.cancel}</Button>
      </div>
    </form>
  </Sheet>}
  {routing&&<RoutingEditor {...routing} names={new Map(nodes.data?.nodes.map(item=>[item.id,item.name]))} close={()=>setRouting(undefined)} saved={refresh}/>}
  {editor&&<IdentityEditor {...editor} close={()=>setEditor(undefined)} saved={async id=>{setEditor(undefined);setIdentityID(id);await refresh();}}/>}
 </div>;
}

const blankPolicy:ClusterIdentityPolicy={enabled:true,max_tcp_conns:null,max_unique_ips:null,rate_limit_up_bps:null,rate_limit_down_bps:null,expiration_rfc3339:"",user_ad_tag:""};
function IdentityEditor({clusterID,revision,identity,close,saved}:{clusterID:string;revision:number;identity?:ClusterIdentity;close:()=>void;saved:(id:string)=>Promise<void>}) {
 const s=useStrings(),t=s.clusters;
 const [username,setUsername]=useState(identity?.username??""),[secret,setSecret]=useState("");
 const [policy,setPolicy]=useState<ClusterIdentityPolicy>(identity?.policy??blankPolicy);
 const [rotate,setRotate]=useState(false),[revoke,setRevoke]=useState(false);
 const initial=JSON.stringify(identity?.policy??blankPolicy);
 const dirty=secret!==""||username!==(identity?.username??"")||JSON.stringify(policy)!==initial||rotate||revoke;
 const work=useMutation({retry:false,mutationFn:async()=>{
  if(identity){await updateClusterIdentity({path:{identity_id:identity.id},headers:{"If-Match":String(identity.revision)},body:{policy,rotate_secret:rotate,revoke},throwOnError:true});return identity.id;}
  const {data}=await createClusterIdentity({path:{cluster_id:clusterID},headers:{"If-Match":String(revision)},body:{username,policy,...(secret?{secret}:{})},throwOnError:true});return data.id;
 },onSuccess:saved});
 const cancel=()=>{if(!work.isPending&&(!dirty||window.confirm(t.discard)))close();};
 useBlocker({shouldBlockFn:()=>work.isPending||(dirty&&!window.confirm(t.discard)),enableBeforeUnload:dirty||work.isPending});
 return <Sheet open title={identity?t.edit:t.newIdentity} onClose={cancel}>
  <form className="space-y-4" onSubmit={e=>{e.preventDefault();if((!rotate&&!revoke)||window.confirm(revoke?t.revokeConfirm:t.rotateConfirm))work.mutate();}}>
   <fieldset disabled={work.isPending} className="space-y-4">
    <label className="block">{t.username}<Input required disabled={!!identity} pattern="[A-Za-z0-9_.-]{1,64}" maxLength={64} value={username} onChange={e=>setUsername(e.target.value)}/></label>
    {!identity&&<label className="block">{t.secret}<Input type="password" autoComplete="off" pattern="[a-fA-F0-9]{32}|" maxLength={32} value={secret} onChange={e=>setSecret(e.target.value)}/><small className="text-text-muted">{t.secretHint}</small></label>}
    {identity?.billing_managed&&<p role="note" className="text-sm text-text-muted">{t.billingManaged}</p>}
    <fieldset disabled={identity?.billing_managed} className="space-y-4">
    <label className="flex gap-2"><input type="checkbox" checked={policy.enabled} onChange={e=>setPolicy({...policy,enabled:e.target.checked})}/>{t.enabled}</label>
    <p className="text-sm text-text-muted">{t.limitsHint}</p>
    {(["max_tcp_conns","max_unique_ips","rate_limit_up_bps","rate_limit_down_bps"] as const).map(field=><label key={field} className="block">{t.fields[field]}<Input type="number" min={0} max={field.startsWith("rate_")?100000000000:Number.MAX_SAFE_INTEGER} step={1} value={policy[field]??""} onChange={e=>{const value=e.target.value===""?null:Number(e.target.value);if(value===null||Number.isSafeInteger(value))setPolicy({...policy,[field]:value});}}/></label>)}
    <label className="block">{t.fields.expiration_rfc3339}<Input placeholder="2027-01-01T00:00:00Z" value={policy.expiration_rfc3339} onChange={e=>setPolicy({...policy,expiration_rfc3339:e.target.value})}/></label>
    <label className="block">{t.fields.user_ad_tag}<Input pattern="[a-fA-F0-9]{32}|" maxLength={32} value={policy.user_ad_tag} onChange={e=>setPolicy({...policy,user_ad_tag:e.target.value})}/></label>
    </fieldset>
    {identity&&<><label className="flex gap-2"><input type="checkbox" checked={rotate} onChange={e=>setRotate(e.target.checked)}/>{t.rotate}</label><label className="flex gap-2"><input type="checkbox" checked={revoke} onChange={e=>setRevoke(e.target.checked)}/>{t.revoke}</label></>}
   </fieldset>
   {work.error&&<p role="alert" className="text-warn">{apiErrorMessage(work.error as {code?:unknown},s)}</p>}
   <p className="text-sm text-text-muted">{t.saveHint}</p>
   <div className="flex flex-wrap gap-2"><Button disabled={work.isPending}>{t.save}</Button><Button type="button" variant="secondary" disabled={work.isPending} onClick={cancel}>{t.cancel}</Button></div>
  </form>
 </Sheet>;
}
