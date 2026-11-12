import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useStrings } from "../i18n";
import { getNodeHealthOptions, listNodesOptions } from "../lib/api/generated/@tanstack/react-query.gen";
import type { Node } from "../lib/api/generated/types.gen";
import { withBasePath } from "../lib/base-path";
import { Button } from "../ui/Button";
import { buttonClasses } from "../ui/buttonStyles";
import { Input } from "../ui/Input";
import { PageHeader } from "../ui/PageHeader";
import { StatePill } from "../ui/StatePill";
import { documentNodeId, nodeNavigationURL } from "./scope";

const pageSize = 8;

export function NodesPage() {
  const s = useStrings();
  const t = s.nodes;
  const registry = useQuery(listNodesOptions());
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const filter = search.trim().toLocaleLowerCase(s.locale);
  const nodes = registry.data?.nodes.filter(node => node.name.toLocaleLowerCase(s.locale).includes(filter)) ?? [];
  const current = Math.min(page, Math.max(0, Math.ceil(nodes.length / pageSize) - 1));
  const visible = nodes.slice(current * pageSize, (current + 1) * pageSize);

  return <div className="space-y-4">
    <PageHeader title={t.registry} description={t.registryHint} />
    <label className="block max-w-md text-sm">
      {t.search}
      <Input type="search" value={search} onChange={event => { setSearch(event.target.value); setPage(0); }} />
    </label>
    {registry.isPending && <p role="status">{t.loading}</p>}
    {registry.isError && <div className="workspace-card flex flex-wrap items-center gap-3" role="alert">
      {t.loadError}<Button variant="secondary" onClick={() => void registry.refetch()}>{t.retry}</Button>
    </div>}
    {registry.data && !visible.length && <p className="workspace-card">{filter ? t.noMatches : t.empty}</p>}
    <ul className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      {visible.map(node => <NodeCard key={node.id} node={node} />)}
    </ul>
    {nodes.length > pageSize && <div className="flex flex-wrap items-center gap-3">
      <Button variant="secondary" disabled={current === 0} onClick={() => setPage(current - 1)}>{s.clusters.previous}</Button>
      <span>{current + 1} / {Math.ceil(nodes.length / pageSize)}</span>
      <Button variant="secondary" disabled={(current + 1) * pageSize >= nodes.length} onClick={() => setPage(current + 1)}>{s.clusters.next}</Button>
    </div>}
  </div>;
}

function NodeCard({ node }: { node: Node }) {
  const t = useStrings().nodes;
  const health = useQuery({
    ...getNodeHealthOptions({ path: { node_id: node.id } }),
    enabled: node.enabled,
    retry: false,
    staleTime: 15_000,
    refetchInterval: 30_000,
  });
  const fresh = health.data?.node_id === node.id && health.data.node_revision === node.revision ? health.data.data : undefined;
  const healthy = node.enabled && !health.isError && fresh?.status === "ok";
  const state = !node.enabled ? t.disabled : health.isError ? t.healthUnavailable : healthy ? t.healthReachable : fresh?.status ?? t.loading;
  const url = nodeNavigationURL(new URL(withBasePath("/overview"), window.location.origin).href, node.id);
  return <li className="workspace-card flex min-w-0 flex-col gap-4">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="min-w-0 break-all text-xl font-bold">{node.name}</h2>
      {node.id === documentNodeId && <StatePill state="muted">{t.current}</StatePill>}
    </div>
    <div role="status" className="flex flex-wrap items-center gap-2">
      <StatePill state={node.enabled && health.isError ? "warn" : healthy ? "ok" : "muted"}>{state}</StatePill>
      {node.enabled && !health.isError && fresh?.read_only && <StatePill state="warn">{t.readOnly}</StatePill>}
    </div>
    <p className="text-sm text-text-muted">{node.source === "configuration" ? t.defaultHint : t.registryManaged}</p>
    {node.enabled && <a href={url} className={buttonClasses("secondary", "md", "mt-auto self-start")}
      onClick={event => { if (node.id !== documentNodeId && !window.confirm(t.switchConfirm)) event.preventDefault(); }}>
      {t.open}
    </a>}
  </li>;
}
