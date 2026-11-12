import { useMemo, useState } from "react";
import { useStrings } from "../../i18n";
import { useConnectionState, useSnapshot } from "../../realtime";
import type { RuntimeTopic, UpstreamsTopic } from "../../realtime/topics";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";
import { StatePill } from "../../ui/StatePill";

const sources = ["me_writers", "dcs", "upstreams", "me_pool_state", "me_quality", "nat_stun"] as const;
type Source = typeof sources[number];
type Field = { path: string; type: string; value: string };
const maxFields = 5000;
const pageSize = 50;

function readFields(snapshot: unknown) {
  const fields: Field[] = [];
  let limited = false;
  function visit(value: unknown, path: string, depth: number) {
    if (depth > 24 || fields.length >= maxFields) {
      limited = true;
      return;
    }
    const type = value === null ? "null" : Array.isArray(value) ? "array" : typeof value;
    if (value !== null && typeof value === "object") {
      const entries = Object.entries(value);
      if (!entries.length) fields.push({ path, type, value: type === "array" ? "[]" : "{}" });
      for (const [key, child] of entries) {
        if (fields.length >= maxFields) { limited = true; break; }
        if (/secret|password|token|credential|authorization|private.?key|api.?key/i.test(key)) continue;
        const next = Array.isArray(value) ? `${path}[${key}]`
          : /^[a-zA-Z_][a-zA-Z_0-9]*$/.test(key) ? `${path}.${key}`
          : `${path}[${JSON.stringify(key)}]`;
        visit(child, next, depth + 1);
      }
    } else {
      fields.push({ path, type, value: value === null ? "—" : String(value) });
    }
  }
  visit(snapshot, "$", 0);
  return { fields, limited };
}

export function SnapshotExplorer() {
  const s = useStrings();
  const t = s.snapshotExplorer;
  const [open, setOpen] = useState(false);
  return <section className="workspace-card space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <h2 className="text-xl font-bold">{t.title}</h2>
      <Button variant="secondary" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? t.hide : t.show}</Button>
    </div>
    {open && <SnapshotFields />}
  </section>;
}

function SnapshotFields() {
  const s = useStrings();
  const t = s.snapshotExplorer;
  const [source, setSource] = useState<Source>("me_writers");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const upstreams = useSnapshot<UpstreamsTopic>("upstreams");
  const runtime = useSnapshot<RuntimeTopic>("runtime");
  const connection = useConnectionState();
  const runtimeSource = source === "me_pool_state" || source === "me_quality" || source === "nat_stun";
  const snapshot = runtimeSource ? runtime : upstreams;
  const value = runtimeSource ? runtime.data?.[source] : upstreams.data?.[source];
  const { fields, limited } = useMemo(() => value === undefined ? { fields: [], limited: false } : readFields(value), [value]);
  const filter = search.trim().toLowerCase();
  const matches = useMemo(() => fields.filter(field => field.path.toLowerCase().includes(filter)), [fields, filter]);
  const pages = Math.max(1, Math.ceil(matches.length / pageSize));
  const current = Math.min(page, pages - 1);

  return <div className="space-y-4">
    <div className="grid gap-3 sm:grid-cols-2">
      <label className="text-sm">{t.source}<Select aria-label={t.source} value={source} onChange={event => { setSource(event.target.value as Source); setPage(0); }}>
        {sources.map(key => <option key={key} value={key}>{t.sources[key]}</option>)}
      </Select></label>
      <label className="text-sm">{t.search}<Input type="search" value={search} onChange={event => { setSearch(event.target.value); setPage(0); }} /></label>
    </div>
    <div className="flex flex-wrap items-center gap-2 text-sm text-text-muted" role="status">
      {runtimeSource ? "runtime" : "upstreams"}
      {snapshot.ts !== null && <time dateTime={new Date(snapshot.ts * 1000).toISOString()}>{new Date(snapshot.ts * 1000).toLocaleTimeString(s.locale)}</time>}
      {(snapshot.stale || connection.stale || snapshot.error) && <StatePill state="warn">{t.stale}</StatePill>}
      {limited && <span>{t.limited}</span>}
    </div>
    {!matches.length ? <p>{value === undefined ? t.unavailable : t.noMatches}</p> :
      <div className="max-w-full overflow-x-auto" tabIndex={0} role="region" aria-label={t.title}>
        <table className="w-full text-left text-sm">
          <thead><tr className="border-b border-border text-text-muted">
            <th scope="col" className="p-2">{t.path}</th><th scope="col" className="p-2">{t.type}</th><th scope="col" className="p-2">{t.value}</th>
          </tr></thead>
          <tbody>{matches.slice(current * pageSize, (current + 1) * pageSize).map(field => <tr className="border-b border-border" key={field.path}>
            <td className="break-all p-2 font-mono">{field.path}</td><td className="p-2">{field.type}</td><td className="break-all p-2">{field.value}</td>
          </tr>)}</tbody>
        </table>
      </div>}
    {pages > 1 && <div className="flex flex-wrap items-center gap-3">
      <Button variant="secondary" disabled={current === 0} onClick={() => setPage(current - 1)}>{s.clusters.previous}</Button>
      <span>{current + 1} / {pages}</span>
      <Button variant="secondary" disabled={current + 1 >= pages} onClick={() => setPage(current + 1)}>{s.clusters.next}</Button>
    </div>}
  </div>;
}
