import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useStrings } from "../i18n";
import {
  applyNodeInstallMutation,
  getNodeInstallOptions,
  getNodeInstallQueryKey,
  listNodesQueryKey,
  previewNodeInstallMutation,
} from "../lib/api/generated/@tanstack/react-query.gen";
import type { NodeInstallInput, NodeInstallJob } from "../lib/api/generated/types.gen";
import { apiErrorCode, apiErrorMessage } from "../people/apiError";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { SettingsField } from "../ui/SettingsField";
import { Sheet } from "../ui/Sheet";

const jobStorageKey = "telemt-panel:ssh-node-install:v1";

function readJob() {
  try { return sessionStorage.getItem(jobStorageKey) ?? ""; } catch { return ""; }
}

function rememberJob(id: string) {
  try {
    if (id) sessionStorage.setItem(jobStorageKey, id);
    else sessionStorage.removeItem(jobStorageKey);
  } catch {
    // Progress remains available while the sheet is open.
  }
}

function isRunning(state: NodeInstallJob["state"] | undefined) {
  return state !== undefined && state !== "prepared" && state !== "installed" && state !== "failed";
}

const initialInput: NodeInstallInput = {
  name: "",
  host: "",
  ssh_port: 22,
  ssh_user: "root",
  host_fingerprint: "",
  proxy_port: 443,
  management_port: 9443,
  with_agent: true,
};

export function NodeInstallForm({ available, close }: { available: boolean; close: () => void }) {
  const strings = useStrings();
  const t = strings.nodes.install;
  const cache = useQueryClient();
  const [input, setInput] = useState<NodeInstallInput>(initialInput);
  const [jobID, setJobID] = useState(readJob);
  const [invalidPorts, setInvalidPorts] = useState(false);
  const jobOptions = { path: { install_id: jobID } };
  const query = useQuery({
    ...getNodeInstallOptions(jobOptions),
    enabled: available && !!jobID,
    retry: false,
    refetchInterval: query => isRunning(query.state.data?.state) ? 1500 : false,
  });
  const job = query.data;
  const preview = useMutation({
    ...previewNodeInstallMutation(),
    onSuccess: (data) => {
      cache.setQueryData(getNodeInstallQueryKey({ path: { install_id: data.id } }), data);
      setJobID(data.id);
    },
  });
  const apply = useMutation({
    ...applyNodeInstallMutation(),
    onMutate: () => rememberJob(jobID),
    onSuccess: (data) => cache.setQueryData(getNodeInstallQueryKey(jobOptions), data),
    onError: () => { void query.refetch(); },
  });
  const nodeID = job?.node_id;
  useEffect(() => {
    if (nodeID) void cache.invalidateQueries({ queryKey: listNodesQueryKey() });
  }, [cache, nodeID]);

  function set<K extends keyof NodeInstallInput>(name: K, value: NodeInstallInput[K]) {
    setInput(current => ({ ...current, [name]: value }));
    setInvalidPorts(false);
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const ports = [input.ssh_port, input.proxy_port, input.management_port, 9091, 9092];
    if (new Set(ports).size !== ports.length) {
      setInvalidPorts(true);
      return;
    }
    preview.mutate({ body: input });
  }

  function reset() {
    setJobID("");
    rememberJob("");
    preview.reset();
    apply.reset();
  }

  function dismiss() {
    if (job?.state === "installed" || (job?.state === "failed" && !nodeID)) rememberJob("");
    close();
  }

  const error = preview.error ?? query.error ?? (job?.state === "prepared" ? apply.error : null);
  const statusExpired = apiErrorCode(query.error) === "node_install_not_found";
  const busy = preview.isPending || apply.isPending;
  return (
    <Sheet open title={t.title} subtitle={t.subtitle} placement="form" onClose={dismiss}>
      <div className="space-y-4">
        {!available ? <p className="text-sm text-text-muted">{t.disabled}</p> : <>
          {!jobID && (
            <form className="space-y-3" onSubmit={submit}>
              <SettingsField label={strings.nodes.name}>
                {attrs => <Input {...attrs} required maxLength={128} value={input.name} onChange={event => set("name", event.target.value)} />}
              </SettingsField>
              <SettingsField label={t.host} hint={t.hostHint}>
                {attrs => <Input {...attrs} required maxLength={64} value={input.host} placeholder="192.0.2.10" onChange={event => set("host", event.target.value)} />}
              </SettingsField>
              <div className="grid gap-3 sm:grid-cols-2">
                <SettingsField label={t.user} hint={t.userHint}>
                  {attrs => <Input {...attrs} required maxLength={64} pattern={String.raw`[A-Za-z_][A-Za-z0-9_\-]*`}
                    value={input.ssh_user} onChange={event => set("ssh_user", event.target.value)} />}
                </SettingsField>
                <SettingsField label={t.sshPort} limits="1–65535">
                  {attrs => <Input {...attrs} required type="number" min={1} max={65535}
                    value={input.ssh_port || ""} onChange={event => set("ssh_port", Number(event.target.value))} />}
                </SettingsField>
              </div>
              <SettingsField label={t.fingerprint} hint={t.fingerprintHint}>
                {attrs => <Input {...attrs} required monospace maxLength={50} pattern={String.raw`SHA256:[A-Za-z0-9+\/]{43}`}
                  value={input.host_fingerprint} onChange={event => set("host_fingerprint", event.target.value)} />}
              </SettingsField>
              <div className="grid gap-3 sm:grid-cols-2">
                <SettingsField label={t.proxyPort} limits="1–65535">
                  {attrs => <Input {...attrs} required type="number" min={1} max={65535}
                    value={input.proxy_port || ""} onChange={event => set("proxy_port", Number(event.target.value))} />}
                </SettingsField>
                <SettingsField label={t.managementPort} hint={t.managementHint} limits="1024–65535">
                  {attrs => <Input {...attrs} required type="number" min={1024} max={65535}
                    value={input.management_port || ""} onChange={event => set("management_port", Number(event.target.value))} />}
                </SettingsField>
              </div>
              {invalidPorts && <p role="alert" className="text-sm text-error-text">{t.invalidPorts}</p>}
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" checked={input.with_agent} onChange={event => set("with_agent", event.target.checked)} />
                <span>{t.agent}<span className="block text-xs text-text-muted">{t.agentHint}</span></span>
              </label>
              <Button type="submit" disabled={busy}>{preview.isPending ? t.preparing : t.preview}</Button>
            </form>
          )}
          {job && (
            <section className="space-y-3 rounded-xl border border-border bg-surface-2 p-3 text-sm">
              <h3 role="status" className="font-semibold">{t.states[job.state]}</h3>
              <dl className="grid gap-1 break-all">
                <div><dt className="inline font-semibold">{strings.nodes.name} · </dt><dd className="inline">{job.plan.name}</dd></div>
                <div><dt className="inline font-semibold">{t.release} · </dt><dd className="inline">{job.plan.release} / {job.plan.architecture}</dd></div>
                <div><dt className="inline font-semibold">{strings.nodes.address} · </dt><dd className="inline">{job.plan.api_url}</dd></div>
                <div><dt className="inline font-semibold">{t.panelPeer} · </dt><dd className="inline">{job.plan.panel_peer}</dd></div>
                <div><dt className="inline font-semibold">{t.proxyPort} · </dt><dd className="inline">{job.plan.proxy_port}</dd></div>
              </dl>
              {job.state === "prepared" && <>
                <p className="text-text-muted">{job.plan.with_agent ? t.planWithAgent : t.plan}</p>
                {job.plan.with_agent && <p className="text-text-muted">{job.plan.config_files ? t.planFiles : t.planAPIConfig}</p>}
                <div className="flex flex-wrap gap-2">
                  <Button type="button" disabled={busy} onClick={() => apply.mutate(jobOptions)}>{t.apply}</Button>
                  <Button type="button" variant="secondary" disabled={busy} onClick={reset}>{t.edit}</Button>
                </div>
              </>}
              {isRunning(job.state) && <p className="text-text-muted">{t.background}</p>}
              {job.code && <p role="alert" className="text-error-text">{apiErrorMessage({ code: job.code }, strings)}</p>}
              {job.state === "failed" && !nodeID && <Button type="button" variant="secondary" onClick={reset}>{t.edit}</Button>}
              {job.state === "installed" && <Button type="button" onClick={dismiss}>{t.done}</Button>}
            </section>
          )}
          {error && <p role="alert" className="text-sm text-error-text">{apiErrorMessage(error, strings)}</p>}
          {jobID && !job && !query.isError && <p role="status" className="text-sm text-text-muted">{t.loading}</p>}
          {jobID && query.isError && (
            <Button type="button" variant="secondary" onClick={statusExpired ? reset : () => void query.refetch()}>
              {statusExpired ? t.edit : strings.nodes.retry}
            </Button>
          )}
        </>}
      </div>
    </Sheet>
  );
}
