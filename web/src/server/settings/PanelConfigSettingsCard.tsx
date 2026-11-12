import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage, useStrings } from "../../i18n";
import { getPanelConfigSettingsOptions, getPanelConfigSettingsQueryKey } from "../../lib/api/generated/@tanstack/react-query.gen";
import { previewPanelConfigSettings, savePanelConfigSettings } from "../../lib/api/generated/sdk.gen";
import type { PanelConfigSettings, PanelConfigValues } from "../../lib/api/generated/types.gen";
import { apiErrorCode } from "../../people/apiError";
import { Button } from "../../ui/Button";
import { CopyField } from "../../ui/CopyField";
import { Input } from "../../ui/Input";
import { ErrorState } from "../../ui/ErrorState";
import { Sheet } from "../../ui/Sheet";
import { Skeleton } from "../../ui/Skeleton";
import { pushToast } from "../../ui/Toast";
import { Notice } from "../Notice";
import { SettingsField } from "../../ui/SettingsField";

type FormValues = Omit<PanelConfigValues, "map_timeout_secs"> & { map_timeout_secs: string };

type EditorState = {
  snapshot: PanelConfigSettings;
  draft: FormValues;
  preview?: PanelConfigValues;
};

function toForm(settings: PanelConfigValues): FormValues {
  return { ...settings, map_timeout_secs: String(settings.map_timeout_secs) };
}

function editorFrom(snapshot: PanelConfigSettings): EditorState {
  return { snapshot, draft: toForm(snapshot.saved) };
}

const durationPattern = /^\+?(?:(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:ns|us|µs|μs|ms|s|m|h))+$/;
const fieldKeys = ["session_ttl", "map_timeout_secs", "customer_portal_enabled", "email_enabled", "telegram_enabled"] as const;
type FieldKey = (typeof fieldKeys)[number];

export function PanelConfigSettingsCard() {
  const s = useStrings();
  const labels = s.server.settings.panelConfig;
  const query = useQuery(getPanelConfigSettingsOptions());
  const client = useQueryClient();
  const editorInput = useRef<HTMLInputElement>(null);
  const [editor, setEditor] = useState<EditorState>();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState<"preview" | "save" | "reload">();
  const [issue, setIssue] = useState<string>();
  const snapshot = query.data;
  const refreshRequired = issue === "panel_config_conflict" || issue === "panel_config_write_unconfirmed";
  const draft = editor?.draft;
  const mapTimeout = Number(draft?.map_timeout_secs);
  const errors: Partial<Record<FieldKey, string>> = {};
  if (draft) {
    if (!durationPattern.test(draft.session_ttl) || !/[1-9]/.test(draft.session_ttl)) {
      errors.session_ttl = "panel_config_session_ttl";
    }
    if (!/^\d+$/.test(draft.map_timeout_secs) || mapTimeout < 1 || mapTimeout > 120) {
      errors.map_timeout_secs = "panel_config_map_timeout_secs";
    }
    if (draft.customer_portal_enabled && !draft.email_enabled && !draft.telegram_enabled) {
      errors.customer_portal_enabled = "panel_config_customer_portal_enabled";
    }
  }
  const serverField = fieldKeys.find((key) => issue === "panel_config_" + key);
  if (serverField && issue) errors[serverField] = issue;
  const fieldError = (key: FieldKey) => errors[key] ? errorMessage(s, errors[key]) : undefined;
  const clientInvalid = Object.keys(errors).length > 0;
  const toggleFields = [
    ["customer_portal_enabled", labels.portal, labels.portalHelp, labels.portalLimits],
    ["email_enabled", labels.email, labels.emailHelp, labels.emailLimits],
    ["telegram_enabled", labels.telegram, labels.telegramHelp, labels.telegramLimits],
  ] as const;

  function openEditor() {
    if (!snapshot) return;
    setEditor((previous) => previous ?? editorFrom(snapshot));
    setOpen(true);
  }

  function updateDraft(draft: FormValues) {
    setEditor((previous) => previous && { snapshot: previous.snapshot, draft });
    if (!refreshRequired) setIssue(undefined);
  }

  async function reload() {
    setPending("reload");
    const result = await query.refetch();
    if (result.isSuccess) {
      setEditor(editorFrom(result.data));
      setIssue(undefined);
    } else {
      setIssue("panel_config_conflict");
    }
    setPending(undefined);
  }

  async function submit(action: "preview" | "save") {
    if (!editor || pending || refreshRequired || clientInvalid) return;
    const settings = { ...editor.draft, map_timeout_secs: mapTimeout };
    const body = { revision: editor.snapshot.revision, settings };
    setPending(action);
    setIssue(undefined);
    try {
      if (action === "preview") {
        const { data } = await previewPanelConfigSettings({ body, throwOnError: true });
        setEditor({ ...editor, preview: data });
      } else {
        const { data } = await savePanelConfigSettings({ body, throwOnError: true });
        client.setQueryData(getPanelConfigSettingsQueryKey(), data);
        setEditor(editorFrom(data));
        pushToast(labels.saved, "ok");
      }
    } catch (err) {
      const code = apiErrorCode(err) ?? (action === "save" ? "panel_config_write_unconfirmed" : "network");
      setIssue(code);
      setEditor({ snapshot: editor.snapshot, draft: editor.draft });
    } finally {
      setPending(undefined);
    }
  }

  function values(settings: PanelConfigValues) {
    return (
      <dl className="grid gap-2 text-sm">
        {[
          [labels.session, settings.session_ttl],
          [labels.map, settings.map_timeout_secs],
          ...toggleFields.map(([key, label]) => [label, settings[key] ? s.common.yes : s.common.no]),
        ].map(([label, value]) => (
          <div key={label} className="flex flex-wrap items-baseline justify-between gap-x-3">
            <dt className="text-text-muted">{label}</dt>
            <dd className="font-medium text-text">{value}</dd>
          </div>
        ))}
      </dl>
    );
  }

  return (
    <section className="rounded-xl bg-surface" aria-labelledby="panel-config-title">
      <header className="border-b border-border px-4 py-3.5">
        <h2 id="panel-config-title" className="text-[16px] font-bold text-text">{labels.title}</h2>
        <p className="mt-1 text-xs text-text-muted">{labels.description}</p>
      </header>
      <div className="grid gap-3 p-4">
        {query.isPending && <Skeleton className="h-11 w-full" />}
        {query.isError && (
          <ErrorState message={errorMessage(s, apiErrorCode(query.error) ?? "network")} onRetry={() => query.refetch()} />
        )}
        {snapshot && <>
          {!snapshot.saved_valid && <Notice tone="warn" title={labels.invalidSaved} />}
          {snapshot.restart_required && (
            <Notice tone="warn" title={labels.pending}>
              <p className="text-sm text-text-muted">{labels.pendingNote}</p>
              {snapshot.restart_command && <CopyField label={labels.restartCommand} value={snapshot.restart_command} />}
            </Notice>
          )}
          {!snapshot.writable && <p className="text-sm text-text-muted">{labels.readOnly}</p>}
          <Button type="button" variant="secondary" className="justify-self-start" onClick={openEditor}>
            {snapshot.writable ? labels.open : labels.view}
          </Button>
        </>}
      </div>
      {editor && draft && <Sheet
        open={open}
        onClose={() => { if (!pending) setOpen(false); }}
        initialFocusRef={editorInput}
        title={labels.title}
        subtitle={labels.editorNote}
        placement="form"
      >
        <div className="grid gap-4">
          <fieldset className="grid gap-4" disabled={!editor.snapshot.writable || !!pending || refreshRequired}>
            <SettingsField label={labels.session} hint={labels.sessionHelp} limits={labels.sessionLimits} error={fieldError("session_ttl")}>
              {(attrs) => <Input {...attrs} ref={editorInput} value={draft.session_ttl} autoCapitalize="off" spellCheck={false}
                onChange={(event) => updateDraft({ ...draft, session_ttl: event.target.value })} required />}
            </SettingsField>
            <SettingsField label={labels.map} hint={labels.mapHelp} limits={labels.mapLimits} error={fieldError("map_timeout_secs")}>
              {(attrs) => <Input {...attrs} type="number" inputMode="numeric" min={1} max={120} step={1} value={draft.map_timeout_secs}
                onChange={(event) => updateDraft({ ...draft, map_timeout_secs: event.target.value })} required />}
            </SettingsField>
            <div className="grid gap-4 rounded-xl border border-border p-3">
              {toggleFields.map(([key, label, hint, limits]) => (
                <SettingsField key={key} label={label} hint={hint} limits={limits} error={fieldError(key)}>
                  {(attrs) => <input {...attrs} type="checkbox" checked={draft[key]} className="h-5 w-5 accent-accent"
                    onChange={(event) => updateDraft({ ...draft, [key]: event.target.checked })} />}
                </SettingsField>
              ))}
            </div>
          </fieldset>
          <details className="min-w-0 rounded-lg border border-border p-3">
            <summary className="cursor-pointer text-sm font-semibold text-text">{labels.active}</summary>
            <div className="mt-3">{values(snapshot?.active ?? editor.snapshot.active)}</div>
          </details>
          {editor.preview !== undefined && (
            <Notice tone="ok" title={labels.preview}>{values(editor.preview)}</Notice>
          )}
          {issue && !serverField && <div role="alert">
            <Notice tone={refreshRequired ? "warn" : "error"} title={errorMessage(s, issue)} />
          </div>}
          {snapshot?.restart_required && <p role="status" className="text-sm text-warn">{labels.pendingNote}</p>}
          {editor.snapshot.writable && (
            <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border pt-3">
              <Button type="button" variant="secondary" disabled={!!pending} onClick={() => void reload()}>{labels.reload}</Button>
              {!refreshRequired && <>
                <Button type="button" variant="secondary" disabled={!!pending} onClick={() => updateDraft(toForm(editor.snapshot.startup))}>{labels.inherit}</Button>
                <Button type="button" variant="secondary" disabled={!!pending || clientInvalid} onClick={() => void submit("preview")}>
                  {pending === "preview" ? labels.checking : labels.check}
                </Button>
                <Button type="button" disabled={!!pending || editor.preview === undefined} onClick={() => void submit("save")}>
                  {pending === "save" ? labels.saving : labels.save}
                </Button>
              </>}
            </div>
          )}
        </div>
      </Sheet>}
    </section>
  );
}
