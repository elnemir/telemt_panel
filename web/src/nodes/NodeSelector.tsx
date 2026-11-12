import { Link, useRouterState } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { listNodesOptions, listNodesQueryKey } from "../lib/api/generated/@tanstack/react-query.gen";
import { createNode, replaceNode, deleteNode } from "../lib/api/generated/sdk.gen";
import type { Node, NodeInput } from "../lib/api/generated/types.gen";
import { documentNodeId, nodeNavigationURL } from "./scope";
import { useStrings } from "../i18n";
import { apiErrorMessage } from "../people/apiError";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { Sheet } from "../ui/Sheet";

export function NodeWorkspace({ children }: { children: ReactNode }) {
  const s = useStrings();
  const t = s.nodes;
  const registry = useQuery({ ...listNodesOptions(), refetchInterval: 15_000 });
  const [editor, setEditor] = useState<Node | "new" | null>(null);
  const path = useRouterState({select: state => state.location.pathname});
  const empty = registry.data?.nodes.length === 0;
  const globalSettings = path === "/server/settings" || path === "/clusters" || path === "/billing";
  const selected = registry.data?.nodes.find(n => n.id === documentNodeId);
  const navigate = (id: string) => {
    if (id !== documentNodeId && window.confirm(t.switchConfirm)) window.location.assign(nodeNavigationURL(window.location.href, id));
  };
  return <>
    <section className="shrink-0 border-b border-border bg-surface px-4 py-2" aria-label={t.title}>
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex min-w-0 basis-full items-center gap-2 sm:basis-auto sm:flex-1"><span className="shrink-0 text-sm font-medium">{t.title}</span>
          <Select aria-label={t.title} value={documentNodeId} onChange={event => navigate(event.target.value)} disabled={!registry.data}>
            {!selected && <option value={documentNodeId}>{registry.isPending ? t.loading : empty ? t.none : documentNodeId}</option>}
            {registry.data?.nodes.map(node => <option key={node.id} value={node.id}>{node.name}{node.enabled ? "" : ` · ${t.disabled}`}</option>)}
          </Select>
        </label>
        <Link to="/billing" className="rounded-lg px-3 py-2 text-sm text-accent">{s.billing.title}</Link>
        <Link to="/clusters" className="rounded-lg px-3 py-2 text-sm text-accent">{s.clusters.title}</Link>
        <Button size="sm" variant="secondary" onClick={() => setEditor("new")}>{t.add}</Button>
        <Button size="sm" variant="secondary" disabled={!selected || selected.source === "configuration"} onClick={() => selected && setEditor(selected)}>{t.manage}</Button>
      </div>
      <p className="mt-1 text-xs text-text-muted">{empty ? t.empty : selected?.source === "configuration" ? t.defaultHint : t.remote}</p>
      {registry.data?.state_durable === false && <p className="text-sm text-warn">{t.temporary}</p>}
      {registry.isError && <p role="alert" className="text-sm text-warn">{t.loadError} <Button size="sm" variant="secondary" onClick={() => void registry.refetch()}>{t.retry}</Button></p>}
    </section>
    {registry.data && (!selected || !selected.enabled) && !globalSettings ? <main className="min-h-0 flex-1 overflow-y-auto p-4 pb-20"><p role="alert" className="text-warn">{empty ? t.empty : t.missing}</p><Link to="/server/settings" className="mt-4 inline-block text-accent">{t.panelSettings}</Link></main> : children}
    {editor && <NodeEditor key={editor === "new" ? "new" : `${editor.id}:${editor.revision}`} node={editor === "new" ? undefined : editor} close={() => setEditor(null)} />}
  </>;
}

function NodeEditor({ node, close }: { node?: Node; close: () => void }) {
  const s = useStrings(); const t = s.nodes; const cache = useQueryClient();
  const [input, setInput] = useState<NodeInput>(() => ({ name: node?.name ?? "", api_url: node?.api_url ?? "", enabled: node?.enabled ?? true, auth_header_env: node?.auth_header_env ?? "", labels: node?.labels ?? {}, agent_url: node?.agent_url ?? "", agent_id: node?.agent_id ?? "", agent_auth_env: node?.agent_auth_env ?? "" }));
  const save = useMutation<unknown, {code?: unknown}>({ mutationFn: async () => {
    if (node) return replaceNode({ path: {node_id: node.id}, headers: {"If-Match": `"${node.revision}"`}, body: input, throwOnError: true });
    return createNode({body: input, throwOnError: true});
  }, onSuccess: async () => { await cache.invalidateQueries({queryKey: listNodesQueryKey()}); close(); },
  onError: () => { void cache.invalidateQueries({queryKey: listNodesQueryKey()}); } });
  const remove = useMutation<unknown, {code?: unknown}>({mutationFn: async () => {
    if (!node) return;
    await deleteNode({path: {node_id: node.id}, headers: {"If-Match": `"${node.revision}"`}, throwOnError: true});
  }, onSuccess: async () => { await cache.invalidateQueries({queryKey: listNodesQueryKey()}); close(); }});
  const busy = save.isPending || remove.isPending;
  return <Sheet open onClose={() => {if (!busy) close();}} title={node ? t.edit : t.add}>
    <form className="flex flex-col gap-4" onSubmit={event => {event.preventDefault(); save.mutate();}}>
      <label>{t.name}<Input required maxLength={128} value={input.name} onChange={event => setInput({...input, name: event.target.value})} /></label>
      <label>{t.address}<Input required type="url" maxLength={2048} disabled={!!node} value={input.api_url} onChange={event => setInput({...input, api_url: event.target.value})} placeholder="https://node.example:9091" /></label>
      <label>{t.env}<Input maxLength={128} pattern="[A-Za-z_][A-Za-z0-9_]*" value={input.auth_header_env} onChange={event => setInput({...input, auth_header_env: event.target.value})} /><span className="text-xs text-text-muted">{t.envHint}</span></label>
      <fieldset className="flex flex-col gap-3 rounded-lg border border-border p-3">
        <legend className="px-1 text-sm font-medium">{t.agent}</legend>
        <p className="text-xs text-text-muted">{t.agentHint}</p>
        <label>{t.agentAddress}<Input type="url" maxLength={2048} disabled={!!node?.agent_url} value={input.agent_url} onChange={event => setInput({...input, agent_url: event.target.value})} placeholder="https://node.example:9092" /></label>
        <label>{t.agentIdentity}<Input required={!!input.agent_url} minLength={16} maxLength={128} pattern="[A-Za-z0-9_-]+" disabled={!!node?.agent_id} value={input.agent_id} onChange={event => setInput({...input, agent_id: event.target.value})} /></label>
        <label>{t.agentEnv}<Input required={!!input.agent_url} maxLength={128} pattern="[A-Za-z_][A-Za-z0-9_]*" value={input.agent_auth_env} onChange={event => setInput({...input, agent_auth_env: event.target.value})} /></label>
      </fieldset>
      <label className="flex items-center gap-2"><input type="checkbox" checked={input.enabled} onChange={event => setInput({...input, enabled: event.target.checked})} />{t.enabled}</label>
      {(save.error || remove.error) && <p role="alert" className="text-warn">{apiErrorMessage(save.error ?? remove.error ?? undefined, s)}</p>}
      <div className="flex flex-wrap gap-2"><Button type="submit" disabled={busy}>{t.save}</Button><Button type="button" variant="secondary" disabled={busy} onClick={close}>{t.cancel}</Button>
        {node && <Button type="button" variant="secondary" disabled={busy} onClick={() => {if (window.confirm(t.deleteConfirm)) remove.mutate();}}>{t.remove}</Button>}
      </div>
    </form>
  </Sheet>;
}
