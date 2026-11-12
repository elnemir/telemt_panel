import { useState } from "react";
import { useBlocker } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useStrings } from "../i18n";
import { apiErrorMessage } from "../people/apiError";
import { Button } from "../ui/Button";
import { Select } from "../ui/Select";
import { Sheet } from "../ui/Sheet";
import { deleteClusterConfiguration, getClusterConfiguration, previewClusterConfiguration, putClusterConfiguration } from "../lib/api/generated/sdk.gen";
import type { ClusterConfigurationPreview, ClusterMember } from "../lib/api/generated/types.gen";

type Draft = { source: string; fields: string[]; revision: number };

function renderValue(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value) ?? "—";
}

export function ClusterConfiguration({ clusterID, revision, members, names, changed }: {
  clusterID: string;
  revision: number;
  members: ClusterMember[];
  names: ReadonlyMap<string, string>;
  changed: () => Promise<void>;
}) {
  const strings = useStrings();
  const copy = strings.clusters.configuration;
  const path = { cluster_id: clusterID };
  const config = useQuery({
    queryKey: ["cluster-configuration", clusterID, revision],
    queryFn: async ({ signal }) => (await getClusterConfiguration({ path, signal, throwOnError: true })).data,
    refetchInterval: 3000,
  });
  const [draft, setDraft] = useState<Draft>();
  const [preview, setPreview] = useState<ClusterConfigurationPreview>();
  const active = members.filter(member => member.state === "active");
  const name = (id: string) => names.get(id) ?? id;
  const settings = config.data;
  const fields = copy.fields as Record<string, string>;
  const reasons = copy.reasons as Record<string, string>;

  const refresh = async () => { await Promise.all([config.refetch(), changed()]); };
  const previewWork = useMutation({
    mutationFn: async (selection: Draft) => (await previewClusterConfiguration({
      path, body: { source_node_id: selection.source, fields: selection.fields }, throwOnError: true,
    })).data,
    onMutate: () => { setPreview(undefined); save.reset(); },
    onSuccess: setPreview,
  });
  const save = useMutation({
    mutationFn: async ({ selection, reviewed }: { selection: Draft; reviewed: ClusterConfigurationPreview }) => {
      await putClusterConfiguration({
        path, headers: { "If-Match": String(selection.revision) },
        body: { source_node_id: selection.source, fields: selection.fields, source_revision: reviewed.source_revision },
        throwOnError: true,
      });
    },
    onSuccess: async () => { setDraft(undefined); setPreview(undefined); await refresh(); },
  });
  const stop = useMutation({
    mutationFn: async (revision: number) => {
      await deleteClusterConfiguration({ path, headers: { "If-Match": String(revision) }, throwOnError: true });
    },
    onSuccess: refresh,
  });
  const busy = previewWork.isPending || save.isPending || stop.isPending;
  const error = previewWork.error ?? save.error ?? stop.error ?? config.error;
  const dirty = !!draft;
  useBlocker({ shouldBlockFn: () => busy || (dirty && !window.confirm(strings.clusters.discard)), enableBeforeUnload: busy || dirty });

  function edit(next: Draft) {
    setDraft(next);
    setPreview(undefined);
    previewWork.reset();
    save.reset();
  }
  function close() {
    if (!busy) {
      setDraft(undefined);
      setPreview(undefined);
      previewWork.reset();
      save.reset();
    }
  }
  function valueList(values: Record<string, unknown>) {
    return <dl className="grid gap-3 text-sm sm:grid-cols-2">
      {Object.entries(values).map(([field, value]) => <div key={field} className="min-w-0 rounded-lg border border-border p-3">
        <dt className="text-text-muted">{fields[field] ?? field}</dt>
        <dd className="break-all font-medium">{renderValue(value)}</dd>
      </div>)}
    </dl>;
  }

  return <section className="workspace-card space-y-4" aria-label={copy.title}>
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h2 className="text-lg font-semibold">{copy.title}</h2><p className="text-sm text-text-muted">{copy.hint}</p></div>
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" disabled={busy || !settings || !active.length} onClick={() => {
          if (!settings) return;
          stop.reset();
          const previousSource = active.find(member => member.node_id === settings.template?.source_node_id)?.node_id;
          edit({ source: previousSource ?? active[0]?.node_id ?? "", fields: Object.keys(settings.template?.settings ?? {}), revision: settings.revision });
        }}>{settings?.template ? copy.edit : copy.create}</Button>
        {settings?.template && <Button variant="secondary" disabled={busy} onClick={() => {
          if (window.confirm(copy.stopConfirm)) stop.mutate(settings.revision);
        }}>{copy.stop}</Button>}
      </div>
    </div>
    {error != null && !draft && <p role="alert" className="text-warn">{apiErrorMessage(error as { code?: unknown }, strings)}</p>}
    {!active.length && <p className="text-sm">{copy.noMembers}</p>}
    {settings && !settings.template && active.length > 0 && <p className="text-sm">{copy.notConfigured}</p>}
    {settings?.template && <>
      <p className="text-sm text-text-muted">{copy.source} {name(settings.template.source_node_id)} · {copy.templateRevision} {settings.template.revision}</p>
      {valueList(settings.template.settings)}
      <ul className="grid gap-2 sm:grid-cols-2">{settings.jobs.map(job => <li key={job.node_id} className="rounded-lg border border-border p-3">
        <strong className="break-all">{name(job.node_id)}</strong>
        <p className="text-sm" role="status">{copy.states[job.status]}</p>
        <p className="text-xs text-text-muted">{copy.templateRevision} {job.desired_revision} · {copy.confirmedRevision} {job.applied_revision || "—"}</p>
        {job.last_code && reasons[job.last_code] && <p className="mt-1 text-xs text-text-muted">{reasons[job.last_code]}</p>}
      </li>)}</ul>
    </>}
    {draft && <Sheet open title={copy.title} onClose={close}>
      <div className="space-y-4">
        <label className="block">{copy.source}
          <Select aria-label={copy.source} value={draft.source} disabled={busy} onChange={event => edit({ ...draft, source: event.target.value })}>
            {active.map(member => <option key={member.node_id} value={member.node_id}>{name(member.node_id)}</option>)}
          </Select>
        </label>
        <fieldset disabled={busy} className="space-y-2">
          <legend className="font-medium">{copy.fieldsTitle}</legend>
          {settings?.fields.map(field => <label key={field} className="flex items-start gap-2 text-sm">
            <input type="checkbox" checked={draft.fields.includes(field)} onChange={event => edit({
              ...draft, fields: event.target.checked ? [...draft.fields, field] : draft.fields.filter(item => item !== field),
            })} />
            <span>{fields[field] ?? field}</span>
          </label>)}
        </fieldset>
        <p className="text-sm text-text-muted">{copy.scope}</p>
        {draft.fields.some(field => field.startsWith("censorship.tls_domain") || field.startsWith("general.modes.")) &&
          <p className="text-sm text-warn">{copy.linkWarning}</p>}
        {preview && <div className="space-y-2"><h3 className="font-medium">{copy.preview}</h3>{valueList(preview.settings)}</div>}
        {error != null && <p role="alert" className="text-warn">{apiErrorMessage(error as { code?: unknown }, strings)}</p>}
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" disabled={busy || !draft.fields.length || !active.some(member => member.node_id === draft.source)}
            onClick={() => previewWork.mutate(draft)}>{copy.preview}</Button>
          <Button disabled={busy || !preview} onClick={() => { if (preview) save.mutate({ selection: draft, reviewed: preview }); }}>
            {copy.apply}
          </Button>
        </div>
      </div>
    </Sheet>}
  </section>;
}
