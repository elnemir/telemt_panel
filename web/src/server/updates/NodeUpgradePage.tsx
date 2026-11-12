import { useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useStrings } from "../../i18n";
import {
  getNodeOptions,
  getNodeUpgradeOptions,
  getNodeUpgradeQueryKey,
  startNodeUpgradeMutation,
} from "../../lib/api/generated/@tanstack/react-query.gen";
import type { NodeUpgradeInput, NodeUpgradeJob } from "../../lib/api/generated/types.gen";
import { documentNodeId } from "../../nodes/scope";
import { apiErrorCode } from "../../people/apiError";
import { Button } from "../../ui/Button";
import { Card, CardTitle } from "../../ui/Card";
import { ErrorState } from "../../ui/ErrorState";
import { Input } from "../../ui/Input";
import { SettingsField } from "../../ui/SettingsField";
import { Skeleton } from "../../ui/Skeleton";
import { StatePill } from "../../ui/StatePill";
import { ServerShell } from "../ServerShell";

const scope = { path: { node_id: documentNodeId } };
const activeStates = new Set<NodeUpgradeJob["state"]>(["checking_host", "staging", "restarting", "checking", "rolling_back"]);

function requestID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, "0")).join("");
}

export function NodeUpgradePage() {
  const strings = useStrings();
  const t = strings.server.updates.nodeUpgrade;
  const cache = useQueryClient();
  const status = useQuery({
    ...getNodeUpgradeOptions(scope),
    refetchInterval: q => q.state.data?.job && activeStates.has(q.state.data.job.state) ? 1500 : false,
  });
  const node = useQuery(getNodeOptions(scope));
  const start = useMutation({
    ...startNodeUpgradeMutation(),
    onSuccess: data => cache.setQueryData(getNodeUpgradeQueryKey(scope), data),
    onError: () => { void status.refetch(); },
  });
  const [sshUser, setSSHUser] = useState("root");
  const [sshPort, setSSHPort] = useState("22");
  const [fingerprint, setFingerprint] = useState("");
  const [telemt, setTelemt] = useState(true);
  const [agent, setAgent] = useState(true);
  const last = useRef<NodeUpgradeInput | null>(null);

  const job = status.data?.job;
  const busy = start.isPending || (job && activeStates.has(job.state));
  const errorCode = apiErrorCode(start.error);
  const errorText = (code?: string) => t.errors[code as keyof typeof t.errors] ??
    strings.errors[code as keyof typeof strings.errors] ?? t.errors.node_install_failed;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !status.data?.available) return;
    const input = {
      ssh_port: Number(sshPort),
      ssh_user: sshUser,
      host_fingerprint: fingerprint.trim(),
      telemt: Boolean(status.data.telemt_version) && telemt,
      agent,
    };
    if (!input.telemt && !input.agent) return;
    const previous = last.current;
    const retry = previous && start.isError && !errorCode &&
      previous.ssh_port === input.ssh_port && previous.ssh_user === input.ssh_user &&
      previous.host_fingerprint === input.host_fingerprint && previous.telemt === input.telemt && previous.agent === input.agent;
    const body = { ...input, request_id: retry ? previous.request_id : requestID() };
    last.current = body;
    start.mutate({ ...scope, body });
  }

  return (
    <ServerShell section="server" title={strings.server.updates.title}>
      {status.isPending ? <Skeleton className="h-48 w-full" /> :
        status.isError && !status.data ? <ErrorState message={errorText(apiErrorCode(status.error))} onRetry={() => status.refetch()} /> :
        <Card className="max-w-3xl space-y-4">
          <div className="space-y-1">
            <CardTitle>{t.title} · {node.data?.name ?? documentNodeId}</CardTitle>
            <p className="text-sm text-text-muted">{t.description}</p>
          </div>
          {job && <div role="status" className="space-y-2 rounded-xl border border-border p-3">
            <StatePill state={job.state === "updated" || job.state === "unchanged" ? "ok" :
              job.state === "failed" || job.state === "unconfirmed" ? "error" : job.state === "rolled_back" ? "warn" : "muted"}>
              {t.states[job.state]}
            </StatePill>
            {job.code && <p className="text-sm text-text-muted">{errorText(job.code)}</p>}
          </div>}
          {status.isRefetchError && <ErrorState message={t.statusError} onRetry={() => status.refetch()} />}
          {!status.data?.available ? <p className="text-sm text-text-muted">{t.disabled}</p> :
          <form className="space-y-4" onSubmit={submit}>
            <fieldset disabled={Boolean(busy)} className="grid gap-4">
              <legend className="sr-only">{t.title}</legend>
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" className="mt-1" checked={Boolean(status.data.telemt_version) && telemt}
                  disabled={!status.data.telemt_version} onChange={event => setTelemt(event.target.checked)} />
                <span>Telemt · {status.data.telemt_version ?? t.noArtifact}</span>
              </label>
              <label className="flex items-start gap-2 text-sm">
                <input type="checkbox" className="mt-1" checked={agent} onChange={event => setAgent(event.target.checked)} />
                <span>{t.agent} · {status.data.agent_version ?? "—"}</span>
              </label>
              <div className="grid gap-3 sm:grid-cols-2">
                <SettingsField label={strings.nodes.install.user} hint={t.userHint}>
                  {attrs => <Input {...attrs} required pattern={String.raw`[a-zA-Z_][a-zA-Z0-9_\-]{0,63}`} maxLength={64} value={sshUser} onChange={event => setSSHUser(event.target.value)} />}
                </SettingsField>
                <SettingsField label={strings.nodes.install.sshPort} limits="1–65535">
                  {attrs => <Input {...attrs} required type="number" min={1} max={65535} step={1} value={sshPort} onChange={event => setSSHPort(event.target.value)} />}
                </SettingsField>
              </div>
              <SettingsField label={strings.nodes.install.fingerprint} hint={strings.nodes.install.fingerprintHint}>
                {attrs => <Input {...attrs} required pattern={String.raw`SHA256:[A-Za-z0-9+\/]{43}`} maxLength={50}
                  spellCheck={false} autoCapitalize="off" value={fingerprint} onChange={event => setFingerprint(event.target.value.trim())} />}
              </SettingsField>
            </fieldset>
            <p className="text-sm text-text-muted">{t.restart}</p>
            {start.isError && <p role="alert" className="text-sm text-error">{errorText(errorCode)}</p>}
            <Button type="submit" variant="primary" disabled={Boolean(busy) || (!agent && !(status.data.telemt_version && telemt))}>{t.submit}</Button>
          </form>}
        </Card>}
    </ServerShell>
  );
}
