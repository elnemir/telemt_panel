import { useId, useState } from "react";
import { useBlocker } from "@tanstack/react-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useStrings } from "../i18n";
import { apiErrorMessage } from "../people/apiError";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { Sheet } from "../ui/Sheet";
import { getClusterRoutingOptions, getClusterRoutingSuggestionsOptions, getClusterIngressInstallOptions } from "../lib/api/generated/@tanstack/react-query.gen";
import { putClusterRouting, exportClusterRouting, startClusterIngressInstall } from "../lib/api/generated/sdk.gen";
import type { IngressConfig, ClusterMember, ClusterRouting, ClusterIngressJob } from "../lib/api/generated/types.gen";

type Props = {
  clusterID: string;
  members: ClusterMember[];
  names: ReadonlyMap<string, string>;
  close: () => void;
  saved: () => Promise<void>;
};

export function RoutingEditor(props: Props) {
  const s = useStrings();
  const query = useQuery(getClusterRoutingOptions({ path: { cluster_id: props.clusterID } }));
  if (query.data) return <RoutingForm {...props} initial={query.data} />;
  return <Sheet open title={s.clusters.routing.title} onClose={props.close}>
    {query.error ? <p role="alert">{apiErrorMessage(query.error as { code?: unknown }, s)}</p> : <p>{s.clusters.loading}</p>}
  </Sheet>;
}

function initialDraft(initial: ClusterRouting, members: ClusterMember[]): IngressConfig {
  const saved = initial.config;
  return {
    domain: saved?.domain ?? "",
    native_port: saved?.native_port ?? 8443,
    web_port: saved?.web_port ?? 0,
    native_balance: saved?.native_balance ?? "leastconn",
    targets: members.filter(member => member.state === "active").map(member => {
      const target = saved?.targets.find(item => item.node_id === member.node_id);
      return {
        node_id: member.node_id,
        native_address: target?.native_address ?? "",
        web_address: target?.web_address ?? "",
        weight: member.weight,
        drain: member.drain,
      };
    }),
  };
}

type TargetAddress = "native_address" | "web_address";
const addressFields = ["native_address", "web_address"] as const;

function saveDownload(body: Blob, filename: string) {
  const url = URL.createObjectURL(body);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function RoutingForm({ clusterID, members, names, initial, close, saved }: Props & { initial: ClusterRouting }) {
  const s = useStrings();
  const t = s.clusters.routing;
  const path = { cluster_id: clusterID };
  const [config, setConfig] = useState(() => initialDraft(initial, members));
  const [revision, setRevision] = useState(initial.revision);
  const [savedConfig, setSavedConfig] = useState(initial.config ? JSON.stringify(initial.config) : "");
  const [step, setStep] = useState<"route" | "install">("route");
  const [notice, setNotice] = useState(false);
  const suggestionList = useId();
  const webEnabled = config.web_port === 443;
  const request = { path, headers: { "If-Match": String(revision) }, throwOnError: true } as const;
  const dirty = JSON.stringify(config) !== savedConfig;
  const suggestions = useQuery({
    ...getClusterRoutingSuggestionsOptions({ path }),
    enabled: config.targets.length > 0,
    retry: false,
  });
  const options = (nodeID: string, key: TargetAddress) =>
    suggestions.data?.find(item => item.node_id === nodeID)?.[key === "native_address" ? "native" : "web"] ?? [];
  const [editedAddresses, setEditedAddresses] = useState<Record<string, Partial<Record<TargetAddress, boolean>>>>({});
  const suggestedAddress = (node: IngressConfig["targets"][number], key: TargetAddress) => {
    const choices = options(node.node_id, key);
    return node[key] || (!editedAddresses[node.node_id]?.[key] && choices.length === 1 ? choices[0] : "");
  };

  const save = useMutation({
    retry: false,
    mutationFn: async () => {
      const submitted = {
        ...config,
        targets: config.targets.map(node => ({
          ...node,
          native_address: suggestedAddress(node, "native_address"),
          web_address: webEnabled ? suggestedAddress(node, "web_address") : "",
        })),
      };
      await putClusterRouting({ ...request, body: submitted });
      setConfig(submitted);
      setRevision(revision + 1);
      setSavedConfig(JSON.stringify(submitted));
      setNotice(true);
      setStep("install");
      await saved();
    },
  });
  const download = useMutation({
    retry: false,
    mutationFn: async () => {
      const result = await exportClusterRouting({ ...request, parseAs: "text" });
      saveDownload(new Blob([result.data], { type: "text/plain" }), "haproxy.cfg");
    },
  });
  const busy = save.isPending || download.isPending;
  useBlocker({ shouldBlockFn: () => busy || (dirty && !window.confirm(s.clusters.discard)), enableBeforeUnload: dirty || busy });

  const error = save.error ?? download.error;
  const cancel = () => {
    if (!busy && (!dirty || window.confirm(s.clusters.discard))) close();
  };
  function updateTarget(id: string, patch: Partial<IngressConfig["targets"][number]>) {
    setConfig(current => ({
      ...current,
      targets: current.targets.map(target => target.node_id === id ? { ...target, ...patch } : target),
    }));
  }
  return <Sheet open title={t.title} onClose={cancel}>
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="secondary" aria-pressed={step === "route"} disabled={busy} onClick={() => setStep("route")}>{t.routeStep}</Button>
        <Button type="button" variant="secondary" aria-pressed={step === "install"} disabled={busy || dirty} onClick={() => setStep("install")}>{t.installStep}</Button>
      </div>
      {error && <p role="alert" className="text-warn">{apiErrorMessage(error as { code?: unknown }, s)}</p>}
      {step === "route" ? <form className="space-y-4" onSubmit={event => { event.preventDefault(); save.mutate(); }}>
        <p className="text-sm text-text-muted">{t.hint}</p>
        <fieldset disabled={busy} className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <label>{t.native_port}
              <Input type="number" required min={1} max={65535} value={config.native_port || ""}
                onChange={event => setConfig({ ...config, native_port: Number(event.target.value) })} />
            </label>
            {webEnabled && <label>{t.web_port}<Input type="number" readOnly value={443} /></label>}
          </div>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={webEnabled} onChange={event => {
              const enabled = event.target.checked;
              setConfig({ ...config, web_port: enabled ? 443 : 0, targets: config.targets.map(target => ({ ...target, web_address: "" })) });
              setEditedAddresses(current => Object.fromEntries(Object.entries(current).map(([id, edited]) => [id, { ...edited, web_address: false }])));
            }} />{t.enableWeb}
          </label>
          {webEnabled && <label className="block">{t.domain}
            <Input required maxLength={253} placeholder="proxy.example.com" value={config.domain}
              onChange={event => setConfig({ ...config, domain: event.target.value.toLowerCase().trim() })} />
          </label>}
          {webEnabled && <p className="text-sm text-text-muted">{t.webHint}</p>}
          <label className="block">{t.algorithm}
            <Select value={config.native_balance} onChange={event => setConfig({ ...config, native_balance: event.target.value as IngressConfig["native_balance"] })}>
              {Object.entries(t.algorithms).map(([value, title]) => <option key={value} value={value}>{title}</option>)}
            </Select>
          </label>
          {config.targets.length ? <div className="space-y-2">
            <Button type="button" variant="secondary" disabled={suggestions.isFetching} onClick={() => void suggestions.refetch()}>
              {suggestions.isFetching ? s.clusters.loading : t.readNodes}
            </Button>
            <p className="text-sm text-text-muted">{t.suggestionsHint}</p>
            {suggestions.isError && <p role="alert" className="text-warn">{t.readFailed}</p>}
          </div> : <p>{t.noMembers}</p>}
          {config.targets.map((node, index) => <fieldset key={node.node_id} className="space-y-3 rounded-lg border border-border p-3">
            <legend>{names.get(node.node_id) ?? node.node_id}</legend>
            {suggestions.data?.find(item => item.node_id === node.node_id)?.status === "unavailable" && <p className="text-sm text-warn">{t.readFailed}</p>}
            {addressFields.filter(key => webEnabled || key === "native_address").map(key => {
              const choices = options(node.node_id, key);
              const address = suggestedAddress(node, key);
              const listID = `${suggestionList}-${index}-${key}`;
              return <div key={key} className="space-y-1">
                <label className="block">{t[key]}
                  <Input required list={choices.length ? listID : undefined} maxLength={64}
                    placeholder={key === "native_address" ? "192.0.2.1:443" : "192.0.2.1:18080"}
                    value={address} onChange={event => {
                      updateTarget(node.node_id, { [key]: event.target.value.trim() });
                      setEditedAddresses(current => ({ ...current, [node.node_id]: { ...current[node.node_id], [key]: true } }));
                    }} />
                </label>
                {choices.length > 0 && <datalist id={listID}>{choices.map(address => <option value={address} key={address} />)}</datalist>}
                {!address && !suggestions.isFetching && <p className="text-sm text-text-muted">{choices.length ? t.chooseAddress : t.enterAddress}</p>}
              </div>;
            })}
            <label className="block">{t.weight}
              <Input required type="number" min={1} max={256} value={node.weight || ""}
                onChange={event => updateTarget(node.node_id, { weight: Number(event.target.value) })} />
            </label>
            <label className="flex gap-2"><input type="checkbox" checked={node.drain} onChange={event => updateTarget(node.node_id, { drain: event.target.checked })} />{t.drain}</label>
          </fieldset>)}
        </fieldset>
        <div className="flex flex-wrap gap-2">
          <Button disabled={busy || !dirty || !config.targets.length || (webEnabled && config.native_port === 443)}>{s.clusters.save}</Button>
          <Button type="button" variant="secondary" disabled={busy} onClick={cancel}>{s.clusters.cancel}</Button>
        </div>
      </form> : <>
        {notice && <p role="status">{t.saved}</p>}
        <IngressInstall clusterID={clusterID} revision={revision} config={config} />
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="secondary" disabled={busy} onClick={() => download.mutate()}>{t.download}</Button>
          <Button type="button" variant="secondary" disabled={busy} onClick={cancel}>{s.clusters.cancel}</Button>
        </div>
      </>}
    </div>
  </Sheet>;
}

function ingressInProgress(job?: ClusterIngressJob) {
  return job && job.state !== "applied" && job.state !== "failed";
}

function IngressInstall({ clusterID, revision, config }: { clusterID: string; revision: number; config: IngressConfig }) {
  const s = useStrings();
  const t = s.clusters.routing;
  const queryClient = useQueryClient();
  const path = { cluster_id: clusterID };
  const statusOptions = getClusterIngressInstallOptions({ path });
  const status = useQuery({
    ...statusOptions,
    retry: false,
    refetchInterval: query => ingressInProgress(query.state.data?.job) ? 1000 : false,
  });
  const [host, setHost] = useState("");
  const [port, setPort] = useState(22);
  const [user, setUser] = useState("root");
  const [fingerprint, setFingerprint] = useState("");
  const install = useMutation({
    retry: false,
    mutationFn: async () => {
      await queryClient.cancelQueries({ queryKey: statusOptions.queryKey });
      const { data } = await startClusterIngressInstall({
        path,
        headers: { "If-Match": String(revision) },
        body: {
          host: host.trim(),
          ssh_port: port,
          ssh_user: user.trim(),
          host_fingerprint: fingerprint.trim(),
          request_id: Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join(""),
        },
        throwOnError: true,
      });
      queryClient.setQueryData(statusOptions.queryKey, { available: true, job: data });
    },
    onSettled: () => { void queryClient.invalidateQueries({ queryKey: statusOptions.queryKey }); },
  });
  const job = status.data?.job;
  const busy = install.isPending || ingressInProgress(job);
  const error = install.error ?? status.error;
  return <div className="space-y-4">
    <p className="text-sm text-text-muted">{t.installHint}</p>
    <ul className="list-disc space-y-1 pl-5 text-sm text-text-muted">
      <li>{t.portStep.replace("{port}", String(config.native_port))} {t.nativeTrust}</li>
      {config.domain && <li>{t.dnsStep.replace("{domain}", config.domain)}</li>}
      {config.web_port === 443 && <li>{t.webSetup}</li>}
    </ul>
    {status.isLoading && <p role="status">{s.clusters.loading}</p>}
    {error && <p role="alert" className="text-warn">{apiErrorMessage(error as { code?: unknown }, s)}</p>}
    {status.isError && <Button type="button" variant="secondary" onClick={() => void status.refetch()}>{t.refreshStatus}</Button>}
    {status.data?.available === false && <p className="text-sm text-text-muted">{t.installDisabled}</p>}
    {job && <div className="space-y-1 rounded-lg border border-border p-3" role={job.state === "failed" ? "alert" : "status"}>
      <p className="break-all">{job.host} · {s.clusters.revision} {job.revision}</p>
      <p>{t.installStates[job.state]}</p>
      {job.code && <p>{apiErrorMessage({ code: job.code }, s)}</p>}
      {job.max_connections && <p>{t.capacity.replace("{count}", String(job.max_connections))}</p>}
    </div>}
    {status.data?.available && <form className="space-y-3" onSubmit={event => { event.preventDefault(); install.mutate(); }}>
      <fieldset disabled={busy} className="grid gap-3 sm:grid-cols-2">
        <label className="sm:col-span-2">{t.installHost}
          <Input required maxLength={45} placeholder="192.0.2.10" value={host} onChange={event => setHost(event.target.value)} />
        </label>
        <label>{t.installPort}
          <Input type="number" min={1} max={65535} required value={port || ""} onChange={event => setPort(Number(event.target.value))} />
        </label>
        <label>{t.installUser}
          <Input required maxLength={64} pattern={"[a-zA-Z_][a-zA-Z0-9_\\-]{0,63}"} value={user} onChange={event => setUser(event.target.value)} />
        </label>
        <label className="sm:col-span-2">{t.installFingerprint}
          <Input required maxLength={50} pattern={"SHA256:[A-Za-z0-9+\\/]{43}"} placeholder="SHA256:…" value={fingerprint} onChange={event => setFingerprint(event.target.value)} />
        </label>
      </fieldset>
      <p className="text-sm text-text-muted">{t.fingerprintHint} <code className="break-all">ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code></p>
      <Button disabled={busy || status.isError}>{t.applySSH}</Button>
    </form>}
  </div>;
}
