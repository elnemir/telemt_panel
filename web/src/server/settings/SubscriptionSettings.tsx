import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage, useLocale, useStrings } from "../../i18n";
import { getSubscriptionAppearanceOptions, getSubscriptionAppearanceQueryKey, putSubscriptionAppearanceMutation } from "../../lib/api/generated/@tanstack/react-query.gen";
import { previewSubscriptionAppearance } from "../../lib/api/generated/sdk.gen";
import type { SubscriptionAppearance, SubscriptionAppearanceSnapshot, SubscriptionPageCopy } from "../../lib/api/generated/types.gen";
import { apiErrorCode } from "../../people/apiError";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";
import { Sheet } from "../../ui/Sheet";
import { Skeleton } from "../../ui/Skeleton";
import { pushToast } from "../../ui/Toast";
import { TransportStatus } from "./TransportStatus";

const blocks = ["status", "quota", "expiry", "qr", "manual", "instructions", "fallback"] as const;
const methods = ["tls", "secure", "classic", "web"] as const;
const states = ["active", "disabled", "expired", "quota_exhausted"] as const;
const copyFields = { title: 80, description: 400, instructions: 1500, support_label: 60 } as const;
const emptyCopy = { title: "", description: "", instructions: "", support_label: "" };
type Lang = "ru" | "en";
type PreviewState = typeof states[number];

function validSupportURL(value: string) {
  if (!value.trim()) return true;
  try {
    const url = new URL(value.trim());
    return value.trim().startsWith("https://") && !!url.hostname && !url.username && !url.password && !/[\\\s]/u.test(value.trim());
  } catch {
    return false;
  }
}

function updateOptions<T>(values: readonly T[], option: T, include: boolean) {
  return include ? [...values, option].sort() : values.filter((value) => value !== option);
}

export function SubscriptionSettings() {
  const t = useStrings().server.settings.subscriptionAppearance;
  const settings = useQuery(getSubscriptionAppearanceOptions());
  const [editing, setEditing] = useState<SubscriptionAppearanceSnapshot | null>(null);

  return <section id="subscription-page" className="min-w-0 overflow-hidden rounded-xl bg-surface">
    <div className="border-b border-border px-4 py-3.5">
      <h2 className="text-base font-bold text-text">{t.heading}</h2>
      <p className="mt-1 text-sm text-text-muted">{t.note}</p>
    </div>
    <TransportStatus target="subscription" title={t.address} />
    <div className="border-t border-border p-4">
      {settings.isPending && <Skeleton className="h-10 w-44" />}
      {settings.isError && <Button variant="secondary" onClick={() => void settings.refetch()}>{t.reload}</Button>}
      {settings.data && <Button variant="secondary" onClick={() => setEditing(settings.data)}>{t.edit}</Button>}
    </div>
    {editing && <SubscriptionEditor initial={editing} onClose={() => setEditing(null)} />}
  </section>;
}

function SubscriptionEditor({ initial, onClose }: { initial: SubscriptionAppearanceSnapshot; onClose: () => void }) {
  const s = useStrings();
  const t = s.server.settings.subscriptionAppearance;
  const locale = useLocale();
  const queryClient = useQueryClient();
  const [baseline, setBaseline] = useState(initial);
  const [draft, setDraft] = useState(initial.settings);
  const [copyLanguage, setCopyLanguage] = useState<Lang>(locale);
  const [previewLanguage, setPreviewLanguage] = useState<Lang>(locale);
  const [previewState, setPreviewState] = useState<PreviewState>("active");
  const [previewDraft, setPreviewDraft] = useState(draft);
  const invalidURL = !validSupportURL(draft.support_url);
  const valid = !invalidURL && draft.connections.length > 0;
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline.settings);

  useEffect(() => {
    const timer = setTimeout(() => setPreviewDraft(draft), 300);
    return () => clearTimeout(timer);
  }, [draft]);

  const preview = useQuery<string>({
    queryKey: ["subscription-preview", previewDraft, previewLanguage, previewState],
    enabled: valid && draft === previewDraft,
    gcTime: 0,
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const result = await previewSubscriptionAppearance({ body: previewDraft, query: { lang: previewLanguage, state: previewState }, parseAs: "text", throwOnError: true, signal });
      if (typeof result.data !== "string") throw new Error("Invalid subscription preview response");
      return result.data;
    },
  });

  const save = useMutation({
    ...putSubscriptionAppearanceMutation(),
    onSuccess: (result) => {
      queryClient.setQueryData(getSubscriptionAppearanceQueryKey(), result);
      pushToast(t.saved, "ok");
      onClose();
    },
  });
  const reload = useMutation({
    mutationFn: () => queryClient.fetchQuery(getSubscriptionAppearanceOptions()),
    onSuccess: (latest) => {
      setBaseline(latest);
      setDraft(latest.settings);
      save.reset();
    },
  });

  const pending = save.isPending || reload.isPending;
  const conflict = apiErrorCode(save.error) === "subscription_settings_conflict";
  const copyLabels: Record<keyof SubscriptionPageCopy, string> = {
    title: t.title, description: t.description, instructions: t.instructions, support_label: t.supportLabel,
  };

  function edit(values: Partial<SubscriptionAppearance>) {
    setDraft((current) => ({ ...current, ...values }));
  }

  function textField(key: keyof SubscriptionPageCopy) {
    const value = draft[copyLanguage][key];
    const onChange = (text: string) => edit({ [copyLanguage]: { ...draft[copyLanguage], [key]: text } });
    const multiline = key === "description" || key === "instructions";
    return <label key={key} className="block space-y-1.5 text-sm font-semibold text-text">
      <span>{copyLabels[key]}</span>
      {multiline
        ? <textarea className="block w-full rounded-lg border border-border bg-surface-2 p-3 text-sm font-normal text-text" rows={key === "instructions" ? 5 : 3} maxLength={copyFields[key]} value={value} onChange={(event) => onChange(event.target.value)} />
        : <Input maxLength={copyFields[key]} value={value} onChange={(event) => onChange(event.target.value)} />}
      <small className="block text-right font-normal text-text-muted">{value.length} / {copyFields[key]}</small>
    </label>;
  }

  return <Sheet open placement="form" className="lg:max-w-[1040px]!" title={t.heading} onClose={() => { if (!pending) onClose(); }}>
    <form className="grid min-w-0 gap-5 p-4 lg:grid-cols-2 lg:p-5" onSubmit={(event) => {
      event.preventDefault();
      if (valid && dirty && !pending && !conflict) save.mutate({ body: { settings: draft, revision: baseline.revision } });
    }}>
      <fieldset disabled={pending} className="min-w-0 space-y-5">
        <div className="space-y-3">
          <h3 className="text-base font-bold text-text">{t.appearance}</h3>
          <div className="grid gap-3 sm:grid-cols-2">
            <Choice label={t.language} value={draft.language} choices={["auto", "ru", "en"]} labels={t.choices} onChange={(language) => edit({ language })} />
            <Choice label={t.theme} value={draft.theme} choices={["auto", "light", "dark"]} labels={t.choices} onChange={(theme) => edit({ theme })} />
            <Choice label={t.accent} value={draft.accent} choices={["blue", "emerald", "violet", "amber"]} labels={t.choices} onChange={(accent) => edit({ accent })} />
            <Choice label={t.logo} value={draft.logo} choices={["initial", "panel", "hidden"]} labels={t.choices} onChange={(logo) => edit({ logo })} />
          </div>
        </div>
        <div className="space-y-3 border-t border-border pt-4">
          <Choice label={t.content} value={copyLanguage} choices={["ru", "en"]} labels={t.choices} onChange={setCopyLanguage} />
          <p className="text-sm text-text-muted">{t.defaultCopy}</p>
          {(Object.keys(copyFields) as Array<keyof SubscriptionPageCopy>).map(textField)}
          <label className="block space-y-1.5 text-sm font-semibold text-text">
            <span>{t.supportURL}</span>
            <Input type="url" maxLength={512} placeholder="https://example.com/support" value={draft.support_url} aria-invalid={invalidURL} onChange={(event) => edit({ support_url: event.target.value })} />
            <small className={invalidURL ? "block text-error-text" : "block text-text-muted"}>{invalidURL ? t.invalidURL : t.supportNote}</small>
          </label>
        </div>
        <div className="space-y-4 border-t border-border pt-4">
          <fieldset className="grid grid-cols-2 gap-2">
            <legend className="mb-2 text-base font-bold text-text">{t.blocks}</legend>
            {blocks.map((item) => <Check key={item} label={t.choices[item]} checked={!draft.hidden.includes(item)} onChange={(checked) => edit({ hidden: updateOptions(draft.hidden, item, !checked) })} />)}
          </fieldset>
          <fieldset className="grid grid-cols-2 gap-2">
            <legend className="mb-2 text-base font-bold text-text">{t.connections}</legend>
            <p className="col-span-2 text-sm text-text-muted">{t.connectionNote}</p>
            {methods.map((item) => <Check key={item} label={t.choices[item]} checked={draft.connections.includes(item)} onChange={(checked) => edit({ connections: updateOptions(draft.connections, item, checked) })} />)}
          </fieldset>
        </div>
      </fieldset>
      <div className="min-w-0 space-y-3 lg:sticky lg:top-0 lg:self-start">
        <h3 className="text-base font-bold text-text">{t.preview}</h3>
        <div className="grid gap-2 sm:grid-cols-2">
          {draft.language === "auto" && <Choice label={t.previewLanguage} value={previewLanguage} choices={["ru", "en"]} labels={t.choices} onChange={setPreviewLanguage} />}
          <Choice label={t.previewState} value={previewState} choices={states} labels={t.choices} onChange={setPreviewState} />
        </div>
        {!valid ? <p role="status" className="text-sm text-text-muted">{t.previewInvalid}</p>
          : preview.isError ? <Button variant="secondary" onClick={() => void preview.refetch()}>{t.preview}</Button>
          : preview.data ? <iframe title={t.preview} sandbox="" referrerPolicy="no-referrer" srcDoc={preview.data} className="h-[500px] w-full rounded-xl border border-border bg-surface-2" />
          : <Skeleton className="h-[500px] w-full rounded-xl" />}
      </div>
      <div className="space-y-2 border-t border-border pt-4 lg:col-span-2">
        {conflict && <p role="alert" className="text-sm text-error-text">{t.conflict}</p>}
        {(reload.isError || (save.isError && !conflict)) && <p role="alert" className="text-sm text-error-text">{errorMessage(s, apiErrorCode(save.error) ?? "internal_error")}</p>}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={!dirty || !valid || pending || conflict}>{t.save}</Button>
          {conflict
            ? <Button type="button" variant="secondary" disabled={pending} onClick={() => reload.mutate()}>{t.reload}</Button>
            : <Button type="button" variant="secondary" disabled={pending} onClick={() => setDraft({
                language: "auto", theme: "auto", accent: "blue", logo: "initial",
                ru: emptyCopy, en: emptyCopy, support_url: "", hidden: [], connections: [...methods].sort(),
              })}>{t.reset}</Button>}
        </div>
      </div>
    </form>
  </Sheet>;
}

function Choice<T extends string>({ label, value, choices, labels, onChange }: {
  label: string; value: T; choices: readonly T[]; labels: Record<T, string>; onChange: (value: T) => void;
}) {
  return <label className="block space-y-1.5 text-sm font-semibold text-text">
    <span>{label}</span>
    <Select value={value} onChange={(event) => onChange(event.target.value as T)}>
      {choices.map((item) => <option key={item} value={item}>{labels[item]}</option>)}
    </Select>
  </label>;
}

function Check({ label, checked, onChange }: { label: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return <label className="flex min-h-10 items-center gap-2 rounded-lg bg-surface-2 px-2.5 text-sm text-text">
    <input type="checkbox" checked={checked} className="h-4 w-4 shrink-0 accent-accent" onChange={(event) => onChange(event.target.checked)} />
    {label}
  </label>;
}
