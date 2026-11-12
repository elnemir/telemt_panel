import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiErrorMessage } from "../../people/apiError";
import { localeOf, useStrings } from "../../i18n";
import {
  getPanelTlsConfigOptions,
  getPanelTlsConfigQueryKey,
  getPanelTlsQueryKey,
  putPanelTlsConfigMutation,
  restartPanelTlsMutation,
} from "../../lib/api/generated/@tanstack/react-query.gen";
import { preparePanelTls } from "../../lib/api/generated/sdk.gen";
import type {
  Error as ApiError,
  PanelTlsCandidate,
  PanelTlsPrepared,
  PanelTlsSaved,
  PanelTlsSettings,
  PanelTlsWarning,
} from "../../lib/api/generated/types.gen";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { Sheet } from "../../ui/Sheet";
import { Skeleton } from "../../ui/Skeleton";
import { SettingsField } from "../../ui/SettingsField";
import {
  buildPanelTlsPrepareBody,
  createPanelAccessDraft,
  isLoopbackHost,
  panelAccessMode,
  panelPort,
  safePanelDestination,
  splitPanelListen,
  splitAccessURL,
  type PanelAccessDraft,
  type PanelAccessMode,
} from "./panelAccess.helpers";

const certificateFileLimit = 128 * 1024;

interface PanelAccessFormProps {
  onClose: () => void;
  target?: "panel" | "subscription";
}

interface FormError {
  summary: string;
  technical?: string;
}

function candidateMode(candidate: PanelTlsCandidate, protocol: string, labels: Record<PanelAccessMode, string>) {
  return labels[panelAccessMode(candidate, protocol)];
}

function mutationError(error: unknown, strings: ReturnType<typeof useStrings>): FormError {
  const typed = error && typeof error === "object" ? error as Partial<ApiError> : undefined;
  const summary = apiErrorMessage(typed, strings);
  const technical = typeof typed?.message === "string" && typed.message.trim() && typed.message.trim() !== summary
    ? typed.message.trim()
    : undefined;
  return { summary, technical };
}

function FormErrorBlock({ error }: { error: FormError }) {
  return (
    <div role="alert" className="rounded-xl border border-error/25 bg-error/8 px-3 py-2.5 text-sm text-error-text">
      <p className="font-semibold">{error.summary}</p>
      {error.technical && <p className="mt-1 break-words font-mono text-xs opacity-90">{error.technical}</p>}
    </div>
  );
}

export function PanelAccessForm({ onClose, target = "panel" }: PanelAccessFormProps) {
  const strings = useStrings();
  const copy = strings.server.settings.transport;
  const endpoint = copy.endpoints;
  const subscription = target === "subscription";
  const options = subscription ? { query: { target: "subscription" as const } } : undefined;
  const queryClient = useQueryClient();
  const query = useQuery({
    ...getPanelTlsConfigOptions(options),
    refetchInterval: (current) => ["preparing", "restarting"].includes(current.state.data?.state ?? "") ? 2000 : 60_000,
  });
  const settings = query.data;
  const [draftState, setDraft] = useState<PanelAccessDraft | null>(null);
  const managed = settings?.capabilities.managed_https === true;
  const canUpload = settings?.capabilities.certificate_upload === true;
  const secureUpload = window.location.protocol === "https:" || isLoopbackHost(window.location.hostname);
  const draft = draftState ?? (settings ? createPanelAccessDraft(settings, subscription ? "http:" : window.location.protocol) : null);
  const [source, setSource] = useState<"upload" | "paths">("upload");
  const [certificate, setCertificate] = useState<File>();
  const [privateKey, setPrivateKey] = useState<File>();
  const upload = canUpload && (managed || source === "upload") && draft?.mode === "certificate";
  const [preparedState, setPrepared] = useState<PanelTlsPrepared | null | undefined>(undefined);
  const prepared = preparedState === undefined ? settings?.prepared ?? null : preparedState;
  const [savedState, setSaved] = useState<PanelTlsSaved | null | undefined>(undefined);
  const pendingSaved = savedState === undefined && settings?.restart_required && settings.new_url
    ? { new_url: settings.new_url, restart_required: true }
    : savedState ?? null;
  const [formError, setFormError] = useState<FormError | null>(null);
  const [restartUnconfirmed, setRestartUnconfirmed] = useState(false);
  const [restartRequestedState, setRestartRequested] = useState(false);
  const preparation = useRef<AbortController | null>(null);
  useEffect(() => () => preparation.current?.abort(), []);

  const restartFinished = (restartRequestedState || restartUnconfirmed)
    && settings?.state === "idle" && !settings.restart_required;
  const saved = restartFinished ? null : pendingSaved;
  const restartFailed = settings?.state === "restart_failed" || settings?.error === "tls_restart_failed";

  const [preparing, setPreparing] = useState(false);
  const apply = useMutation(putPanelTlsConfigMutation());
  const restart = useMutation(restartPanelTlsMutation());
  const activePort = settings ? panelPort(settings.active) : "";
  const destination = safePanelDestination(saved?.new_url ?? prepared?.new_url ?? settings?.new_url, draft?.mode ?? "http");
  const warningLabels = copy.preparedWarnings as Record<PanelTlsWarning, string>;
  const lockedForRestart = Boolean(settings?.restart_required || saved?.restart_required);
  const restartRequested = !restartFailed && (restartRequestedState || settings?.state === "restarting");
  const requiresHttpConfirmation = Boolean(
    draft && (!subscription || draft.enabled) && (draft.mode === "http" || (draft.mode === "proxy" && !isLoopbackHost(draft.host))),
  );
  const networkWarnings = useMemo(() => {
    if (!draft || !settings) return [];
    const warnings: string[] = [];
    if (draft.port !== activePort) warnings.push(copy.firewallWarning.replace("{port}", draft.port || "—"));
    if (isLoopbackHost(splitPanelListen(settings.active.listen).host) && !isLoopbackHost(draft.host)) warnings.push(copy.publicBindWarning);
    if (draft.mode === "acme") warnings.push(copy.acmePort80);
    return warnings;
  }, [activePort, copy.acmePort80, copy.firewallWarning, copy.publicBindWarning, draft, settings]);

  const [checked, setChecked] = useState(false);
  const validation = useMemo(() => {
    const errors: Partial<Record<keyof PanelAccessDraft, string>> = {};
    if (!draft) return errors;
    const port = Number(draft.port);
    const basePath = splitAccessURL(draft.publicURL ?? "", draft.basePath ?? "").basePath.replace(/\/+$/, "");
    if (subscription && !basePath.replace(/\//g, "")) errors.basePath = endpoint.pathRequired;
    else if (basePath && (!/^[A-Za-z0-9._~/-]+$/.test(basePath) ||
      basePath.replace(/^\//, "").split("/").some((part) => !part || part === "." || part === ".."))) {
      errors.basePath = copy.fieldLimits.invalidPath;
    }
    if (subscription && draft.enabled && draft.mode === "proxy" && !draft.publicURL?.trim()) errors.publicURL = endpoint.proxyURL;
    if (draft.publicURL) {
      try {
        const url = new URL(draft.publicURL);
        if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.port === "0") {
          errors.publicURL = endpoint.invalidURL;
        } else if (draft.mode === "proxy" && url.protocol !== "https:") {
          errors.publicURL = endpoint.proxyURL;
        } else if ((draft.mode === "certificate" || draft.mode === "acme") && url.protocol !== "https:") {
          errors.publicURL = copy.certificateInput.httpsURL;
        } else if (draft.mode === "acme" && draft.domain.trim() && url.hostname.toLowerCase() !== draft.domain.trim().replace(/\.$/, "").toLowerCase()) {
          errors.publicURL = copy.certificateInput.domainMatch;
        } else if (managed && basePath !== (draft.basePath ?? "")) {
          errors.publicURL = copy.certificateInput.samePath;
        }
      } catch { errors.publicURL = endpoint.invalidURL; }
    }
    if (!managed && !draft.host.trim()) errors.host = copy.validation.listen;
    if (!/^\d+$/.test(draft.port) || !Number.isInteger(port) || port < 1 || port > 65535 || (draft.mode === "acme" && port === 80)) {
      errors.port = copy.fieldLimits.port;
    }
    if (draft.mode === "acme") {
      const domain = draft.domain.trim().replace(/\.$/, "");
      if (!domain) errors.domain = copy.validation.domain;
      else if (domain.length > 253 || !domain.includes(".") || /^\d+(?:\.\d+){3}$/.test(domain) ||
        domain.split(".").some((part) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(part))) {
        errors.domain = copy.fieldLimits.invalidDomain;
      }
    }
    if (upload) {
      if (!draft.publicURL?.trim()) errors.publicURL = copy.certificateInput.httpsURL;
      if (!secureUpload) errors.certFile = copy.certificateInput.secure;
      else if (!certificate?.size || certificate.size > certificateFileLimit) errors.certFile = copy.certificateInput.fileLimit;
      if (!privateKey?.size || privateKey.size > certificateFileLimit) errors.keyFile = copy.certificateInput.fileLimit;
    } else if (draft.mode === "certificate") {
      if (!draft.certFile.trim()) errors.certFile = copy.validation.certificate;
      if (!draft.keyFile.trim()) errors.keyFile = copy.validation.certificate;
    }
    if (requiresHttpConfirmation && !draft.httpConfirmed) errors.httpConfirmed = copy.validation.httpConfirmation;
    return errors;
  }, [certificate, copy.certificateInput, copy.fieldLimits, copy.validation, draft, endpoint, managed, privateKey, requiresHttpConfirmation, secureUpload, subscription, upload]);

  const fieldError = (key: keyof PanelAccessDraft) => checked ? validation[key] : undefined;

  function edit(change: Partial<PanelAccessDraft>) {
    setDraft((current) => current || draft ? { ...(current ?? draft!), ...change } : null);
    setPrepared(null);
    setFormError(null);
  }

  function selectMode(mode: PanelAccessMode) {
    setChecked(false);
    setCertificate(undefined);
    setPrivateKey(undefined);
    setDraft((current) => {
      current ??= draft;
      if (!current) return null;
      return {
        ...current,
        mode,
        host: mode === "proxy" && current.mode !== "proxy" ? "127.0.0.1" : current.host,
        httpConfirmed: mode === "http" ? current.httpConfirmed : false,
      };
    });
    setPrepared(null);
    setFormError(null);
  }

  async function onPrepare(event: FormEvent) {
    event.preventDefault();
    if (preparation.current) return;
    setChecked(true);
    if (!draft || Object.keys(validation).length || (upload && !secureUpload)) return;
    setFormError(null);
    const controller = new AbortController();
    preparation.current = controller;
    setPreparing(true);
    try {
      const body = buildPanelTlsPrepareBody(draft);
      if (managed && settings) {
        body.listen = (settings.configured ?? settings.active).listen;
        delete body.tls.acme_cache_dir;
      }
      if (upload && certificate && privateKey) {
        const [certificatePEM, privateKeyPEM] = await Promise.all([certificate.text(), privateKey.text()]);
        if (controller.signal.aborted) return;
        if (!certificatePEM.includes("-----BEGIN CERTIFICATE-----") ||
          !/-----BEGIN (?:RSA |EC )?PRIVATE KEY-----/.test(privateKeyPEM)) {
          setFormError({ summary: copy.certificateInput.invalidPEM });
          return;
        }
        delete body.tls.cert_file;
        delete body.tls.key_file;
        body.certificate_upload = { certificate_pem: certificatePEM, private_key_pem: privateKeyPEM };
      }
      const result = await preparePanelTls({ ...options, body, signal: controller.signal, throwOnError: true });
      if (!controller.signal.aborted) {
        setPrepared(result.data);
        setCertificate(undefined);
        setPrivateKey(undefined);
      }
    } catch (error) {
      if (!controller.signal.aborted) setFormError(mutationError(error, strings));
    } finally {
      if (preparation.current === controller) preparation.current = null;
      if (!controller.signal.aborted) setPreparing(false);
    }
  }

  async function onApply() {
    if (!prepared) return;
    setFormError(null);
    try {
      const result = await apply.mutateAsync({ ...options, body: { receipt: prepared.receipt, candidate: prepared.candidate } });
      setSaved(result);
      setPrepared(null);
      queryClient.setQueryData<PanelTlsSettings>(getPanelTlsConfigQueryKey(options), (current) => current ? {
        ...current,
        configured: prepared.candidate,
        state: "saved",
        restart_required: true,
        new_url: result.new_url,
      } : current);
    } catch (error) {
      setFormError(mutationError(error, strings));
    }
  }

  async function onRestart() {
    setFormError(null);
    setRestartUnconfirmed(false);
    setRestartRequested(false);
    try {
      const result = await restart.mutateAsync({});
      setSaved(result);
      setRestartRequested(true);
      queryClient.setQueryData<PanelTlsSettings>(getPanelTlsConfigQueryKey(options), (current) => current ? {
        ...current,
        state: "restarting",
        restart_required: result.restart_required,
        new_url: result.new_url,
      } : current);
      void queryClient.invalidateQueries({ queryKey: getPanelTlsQueryKey() });
    } catch (error) {
      const knownResponse = error && typeof error === "object" && "code" in error;
      if (saved?.restart_required && !knownResponse) setRestartUnconfirmed(true);
      else setFormError(mutationError(error, strings));
    }
  }

  const modeDescriptions: Record<PanelAccessMode, string> = copy.accessModeDescriptions;

  function close() {
    if (apply.isPending || restart.isPending) return;
    preparation.current?.abort();
    onClose();
  }

  return (
    <Sheet open onClose={close} title={subscription ? endpoint.subscriptionTitle : copy.formTitle} eyebrow={copy.formEyebrow} subtitle={subscription ? endpoint.subscriptionNote : copy.formSubtitle} placement="form" bodyClassName="px-4 py-4 sm:px-5">
      {query.isError && !settings ? (
        <p role="alert" className="text-sm text-error-text">{copy.unavailable}</p>
      ) : query.isPending || !draft || !settings ? <Skeleton className="h-40" /> : (
        <div className="space-y-4">
          <section className="rounded-xl border border-border bg-surface-2/70 p-3 text-sm">
            <p className="font-semibold text-text">{copy.activeNow}: {subscription && !settings.active.enabled ? endpoint.disabled : candidateMode(settings.active, subscription ? "http:" : window.location.protocol, copy.accessModes)}</p>
            <p className="mt-1 break-all font-mono text-xs text-text-muted">{settings.active.listen}</p>
            {settings.configured && JSON.stringify(settings.configured) !== JSON.stringify(settings.active) && (
              <p className="mt-2 text-text-muted">{copy.configured}: {candidateMode(settings.configured, window.location.protocol, copy.accessModes)} · {settings.configured.listen}</p>
            )}
            {(settings.state === "restart_failed" || settings.error === "tls_restart_failed") && <p className="mt-2 text-error-text">{copy.restartFailedState}</p>}
          </section>

          {!settings.capabilities.prepare ? (
            <section className="rounded-xl border border-warning/30 bg-warning/8 p-3 text-sm text-text-muted">
              <h3 className="font-semibold text-text">{copy.manualTitle}</h3>
              <p className="mt-1">{copy.manualDescription}</p>
              {settings.manual_hints.includes("edit_startup_config_manually") && (
                <div className="mt-3"><p className="text-xs font-semibold uppercase tracking-wide text-text-faint">{copy.configPath}</p><p className="mt-1 break-all rounded-lg bg-bg/60 p-2 font-mono text-xs text-text">{settings.config_path ?? copy.configPathUnknown}</p></div>
              )}
              {settings.manual_hints.includes("restart_panel_manually") && (
                <div className="mt-3"><p className="text-xs font-semibold uppercase tracking-wide text-text-faint">{copy.restartCommand}</p><p className="mt-1 break-all rounded-lg bg-bg/60 p-2 font-mono text-xs text-text">{settings.manual_restart_command}</p></div>
              )}
            </section>
          ) : lockedForRestart ? (
            saved ? null : <section className="rounded-xl border border-accent/25 bg-accent/8 p-3 text-sm text-text-muted"><h3 className="font-semibold text-text">{copy.savedPending}</h3><p className="mt-1">{copy.savedPendingNote}</p></section>
          ) : prepared ? (
            null
          ) : (
            <form className="space-y-4" onSubmit={onPrepare} noValidate>
              <fieldset disabled={preparing} className="min-w-0 space-y-4">
              {subscription && <label className="flex min-h-11 items-center gap-3 rounded-xl bg-surface-2 p-3 text-sm font-semibold"><input type="checkbox" checked={draft.enabled ?? false} onChange={(event) => edit({ enabled: event.target.checked })} className="h-5 w-5 accent-accent" />{endpoint.enable}</label>}
              <div className="space-y-3 rounded-xl border border-border p-3">
                <SettingsField label={endpoint.publicURL} limits={copy.fieldLimits.url} error={fieldError("publicURL")}>
                  {(attrs) => <Input {...attrs} value={draft.publicURL ?? ""} placeholder={subscription ? "https://links.example.com/access-7k2/" : "https://admin.example.com/admin-9r4/"}
                    autoCapitalize="none" autoCorrect="off" onChange={(event) => edit({ publicURL: event.target.value })}
                    onBlur={() => { if (!managed) edit(splitAccessURL(draft.publicURL ?? "", draft.basePath ?? "")); }} />}
                </SettingsField>
                <SettingsField label={endpoint.path} limits={copy.fieldLimits.path} error={fieldError("basePath")}>
                  {(attrs) => <Input {...attrs} value={draft.basePath ?? ""} placeholder={subscription ? "/sub" : "/admin-9r4"} autoCapitalize="none" autoCorrect="off"
                    readOnly={managed} onChange={(event) => edit({ basePath: event.target.value })} />}
                </SettingsField>
                <p className="text-xs leading-relaxed text-text-muted">{managed ? copy.certificateInput.managed : endpoint.addressNote}</p>
              </div>
              <fieldset className="space-y-2">
                <legend className="mb-2 text-sm font-semibold text-text">{copy.modeLegend}</legend>
                {(Object.keys(copy.accessModes) as PanelAccessMode[]).filter((mode) => !managed || mode === "certificate" || mode === "acme").map((mode) => {
                  const disabled = mode === "acme" && !settings.capabilities.acme_prepare;
                  return (
                    <label key={mode} className={`flex min-h-11 items-start gap-3 rounded-xl border px-3 py-2.5 ${draft.mode === mode ? "border-accent bg-accent/10" : "border-border bg-surface-2"} ${disabled ? "opacity-50" : ""}`}>
                      <input type="radio" name="panel-access-mode" value={mode} checked={draft.mode === mode} disabled={disabled} onChange={() => selectMode(mode)} className="mt-0.5 h-5 w-5 shrink-0 accent-accent" />
                      <span><span className="block text-sm font-semibold text-text">{copy.accessModes[mode]}{mode === "acme" && " · Let’s Encrypt"}</span><span className="mt-0.5 block text-xs leading-relaxed text-text-muted">{modeDescriptions[mode]}</span></span>
                    </label>
                  );
                })}
              </fieldset>

              <div className="space-y-3 rounded-xl border border-border p-3">
                {draft.mode === "acme" && <SettingsField label={copy.domain} limits={copy.fieldLimits.domain} error={fieldError("domain")}>
                  {(attrs) => <Input {...attrs} value={draft.domain} onChange={(event) => edit({ domain: event.target.value })}
                    placeholder="panel.example.com" autoCapitalize="none" autoCorrect="off" required />}
                </SettingsField>}
                <SettingsField label={copy.port} limits={copy.fieldLimits.port} error={fieldError("port")}>
                  {(attrs) => <Input {...attrs} value={draft.port} onChange={(event) => edit({ port: event.target.value })}
                    type="number" inputMode="numeric" min={1} max={65535} step={1} readOnly={managed} required />}
                </SettingsField>
                {draft.mode === "certificate" && canUpload && !managed && (
                  <fieldset className="space-y-2">
                    <legend className="mb-1 text-sm font-semibold text-text">{copy.certificateInput.source}</legend>
                    {(["upload", "paths"] as const).map((value) => (
                      <label key={value} className="flex min-h-11 items-center gap-2 text-sm">
                        <input type="radio" name="certificate-source" value={value} checked={source === value}
                          className="h-5 w-5 accent-accent" onChange={() => {
                            setSource(value);
                            setCertificate(undefined);
                            setPrivateKey(undefined);
                            setChecked(false);
                            setFormError(null);
                          }} />
                        {copy.certificateInput[value]}
                      </label>
                    ))}
                  </fieldset>
                )}
                {upload && (
                  <div className="space-y-3">
                    {!secureUpload && <p role="status" className="text-sm text-warning-text">{copy.certificateInput.secure}</p>}
                    <SettingsField label={copy.certificateInput.certificate} limits={copy.certificateInput.certificateHelp} error={fieldError("certFile")}>
                      {(attrs) => <Input {...attrs} type="file" accept=".pem,.crt,.cer" disabled={!secureUpload}
                        className="py-1.5" onChange={(event) => setCertificate(event.target.files?.[0])} required />}
                    </SettingsField>
                    <SettingsField label={copy.certificateInput.privateKey} limits={copy.certificateInput.privateKeyHelp} error={fieldError("keyFile")}>
                      {(attrs) => <Input {...attrs} type="file" accept=".pem,.key" disabled={!secureUpload}
                        className="py-1.5" onChange={(event) => setPrivateKey(event.target.files?.[0])} required />}
                    </SettingsField>
                  </div>
                )}
                {draft.mode === "certificate" && !upload && (
                  <div className="space-y-3">
                    <SettingsField label={copy.certPath} limits={copy.fieldLimits.pem} error={fieldError("certFile")}>
                      {(attrs) => <Input {...attrs} monospace value={draft.certFile} onChange={(event) => edit({ certFile: event.target.value })}
                        placeholder="/etc/ssl/panel/fullchain.pem" required />}
                    </SettingsField>
                    <SettingsField label={copy.keyPath} limits={copy.fieldLimits.pem} error={fieldError("keyFile")}>
                      {(attrs) => <Input {...attrs} monospace value={draft.keyFile} onChange={(event) => edit({ keyFile: event.target.value })}
                        placeholder="/etc/ssl/panel/privkey.pem" required />}
                    </SettingsField>
                  </div>
                )}
                {!managed && <details className="rounded-lg bg-surface-2 px-3 py-2">
                  <summary className="min-h-11 cursor-pointer py-3 text-sm font-semibold text-text">{copy.advanced}</summary>
                  <div className="space-y-3 pb-2">
                    <SettingsField label={copy.listenHost} limits={copy.fieldLimits.host} error={fieldError("host")}>
                      {(attrs) => <Input {...attrs} monospace value={draft.host} onChange={(event) => edit({ host: event.target.value })} required />}
                    </SettingsField>
                    {draft.mode === "acme" && <SettingsField label={copy.cachePath} limits={copy.fieldLimits.cache}>
                      {(attrs) => <Input {...attrs} monospace value={draft.cacheDir} onChange={(event) => edit({ cacheDir: event.target.value })} />}
                    </SettingsField>}

                  </div>
                </details>}
              </div>

              {networkWarnings.length > 0 && <section className="rounded-xl border border-warning/30 bg-warning/8 px-3 py-2.5 text-sm text-warning-text" data-network-warnings><h3 className="font-semibold text-text">{copy.networkWarnings}</h3><ul className="mt-1.5 space-y-1">{networkWarnings.map((warning) => <li key={warning}>• {warning}</li>)}</ul></section>}
              {draft.mode === "acme" && <div className="rounded-xl border border-accent/20 bg-accent/8 px-3 py-2.5 text-sm text-text-muted"><p>{copy.acmeActionNotice}</p><a className="mt-2 inline-block font-semibold text-accent underline underline-offset-2" href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer">{copy.acmeTerms}</a></div>}
              {requiresHttpConfirmation && <label className="flex min-h-11 items-start gap-3 rounded-xl border border-warning/30 bg-warning/8 px-3 py-2.5 text-sm text-warning-text"><input type="checkbox" className="mt-0.5 h-5 w-5 shrink-0 accent-accent" checked={draft.httpConfirmed} onChange={(event) => edit({ httpConfirmed: event.target.checked })} /><span>{subscription ? endpoint.httpWarning : copy.httpConfirmation}</span></label>}
              {fieldError("httpConfirmed") && <p role="alert" className="text-sm text-error">{fieldError("httpConfirmed")}</p>}
              {draft.mode === "proxy" && <p className="rounded-xl bg-surface-2 px-3 py-2.5 text-sm text-text-muted">{copy.proxyConfigurationNote}</p>}
              {(draft.mode === "acme" || draft.mode === "certificate") && isLoopbackHost(draft.host) && <p className="rounded-xl bg-surface-2 px-3 py-2.5 text-sm text-text-muted">{copy.loopbackHttpsNote}</p>}
              {formError && <FormErrorBlock error={formError} />}
              <Button className="w-full" type="submit" disabled={preparing || (upload && !secureUpload) || (draft.mode === "acme" && !settings.capabilities.acme_prepare)}>{preparing ? copy.preparing : draft.mode === "acme" && (!subscription || draft.enabled) ? copy.prepareAcme : copy.prepare}</Button>
              </fieldset>
            </form>
          )}

          {prepared && !saved && (
            <section className="space-y-3 rounded-xl border border-success/25 bg-success/6 p-3 text-sm text-text-muted">
              <div><h3 className="font-semibold text-text">{copy.preparedTitle}</h3><p className="mt-1">{copy.preparedNote}</p></div>
              <div><p className="text-xs font-semibold uppercase tracking-wide text-text-faint">{copy.newAddress}</p>{!subscription && safePanelDestination(prepared.new_url, draft.mode) ? <a className="mt-1 block break-all font-mono text-sm text-accent underline underline-offset-2" href={safePanelDestination(prepared.new_url, draft.mode)}>{prepared.new_url}</a> : <p className="mt-1 break-all font-mono text-sm text-text">{subscription ? prepared.new_url + "<token>" : draft.mode === "proxy" ? copy.externalProxyOwned : prepared.new_url}</p>}</div>
              {prepared.certificate && (
                <div className="rounded-lg bg-surface-2 p-2.5">
                  <p className="font-semibold text-text">{copy.certificateVerified}</p>
                  {prepared.certificate.domain && <p className="mt-1 break-all font-mono text-xs text-text">{prepared.certificate.domain}</p>}
                  <p className="mt-1">{copy.certificateExpires}: <time dateTime={prepared.certificate.expires_at}>{new Date(prepared.certificate.expires_at).toLocaleString(localeOf(strings))}</time></p>
                  <p className="mt-1">{prepared.certificate.publicly_trusted ? copy.publicTrustConfirmed : copy.privateTrustOnly}</p>
                </div>
              )}
              <ul className="space-y-1.5">{prepared.warnings.map((warning) => <li key={warning}>• {warningLabels[warning]}</li>)}</ul>
              {!prepared.warnings.includes("public_reachability_unverified") && <p>{copy.certificateCheckNote}</p>}
              <p>{copy.restartSeparate}</p>
              {formError && <FormErrorBlock error={formError} />}
              <div className="grid gap-2 sm:grid-cols-2">
                <Button variant="secondary" className="w-full" type="button" disabled={apply.isPending} onClick={() => setPrepared(null)}>{copy.editSettings}</Button>
                <Button className="w-full" type="button" onClick={onApply} disabled={apply.isPending}>{apply.isPending ? copy.saving : copy.save}</Button>
              </div>
            </section>
          )}

          {saved && (
            <section className="space-y-3 rounded-xl border border-accent/25 bg-accent/8 p-3 text-sm text-text-muted">
              <div><h3 className="font-semibold text-text">{copy.savedPending}</h3><p className="mt-1">{copy.savedPendingNote}</p></div>
              <p className="font-medium text-text">{copy.activeNow}: {candidateMode(settings.active, window.location.protocol, copy.accessModes)}</p>
              {destination && !subscription ? <a className="block break-all font-mono text-sm text-accent underline underline-offset-2" href={destination}>{saved.new_url}</a> : <p>{subscription ? saved.new_url + "<token>" : draft.mode === "proxy" ? copy.externalProxyOwned : saved.new_url}</p>}
              {restartUnconfirmed && <p role="status" className="rounded-lg border border-warning/30 bg-warning/8 p-2.5 text-warning-text">{copy.restartDisconnect}</p>}
              {formError && <FormErrorBlock error={formError} />}
              {restartRequested ? <p role="status" className="rounded-lg bg-surface-2 p-2.5 text-text">{copy.restartRequested}</p> : settings.capabilities.restart ? <Button className="w-full" type="button" onClick={onRestart} disabled={restart.isPending}>{restart.isPending ? copy.restarting : copy.restart}</Button> : <div><p>{copy.restartManually}</p><p className="mt-1 break-all rounded-lg bg-bg/60 p-2 font-mono text-xs text-text">{settings.manual_restart_command}</p></div>}
            </section>
          )}
        </div>
      )}
    </Sheet>
  );
}
