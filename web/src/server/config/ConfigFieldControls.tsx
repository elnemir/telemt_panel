import { useId, useState } from "react";
import type { TelemtConfigCatalog, TelemtConfigField } from "../../lib/api/generated/types.gen";
import { useStrings, type Dict } from "../../i18n";
import { Input } from "../../ui/Input";
import { Select } from "../../ui/Select";
import { Toggle } from "../../ui/Toggle";
import { fieldInstances, setConfigValue, type ConfigFieldInstance } from "./configCatalog.helpers";
import { configFieldDescription, configFieldLabel } from "./configFieldPresentation";
import { formatConfigNumber } from "./configNumber.helpers";
import { HelpHint } from "../../ui/HelpHint";
import { ConfigMapControl } from "./ConfigMapControl";
import { configIntegerMax, isConfigIPAddress, isConfigNetwork, isConfigSocketAddress, parseConfigNumber } from "./configInputValidation";

export function GenericFields({ fields, sections, advanced, catalog, onChange }: {
  fields: TelemtConfigField[];
  sections: Record<string, unknown>;
  advanced: boolean;
  catalog?: TelemtConfigCatalog;
  onChange: (next: Record<string, unknown>) => void;
}) {
  return (
    <div>
      {fields.flatMap((field) =>
        fieldInstances(sections, field).map((instance) => (
          <ConfigFieldRow
            key={instance.concretePath}
            instance={instance}
            advanced={advanced}
            groupName={catalog?.groups.find((item) => item.id === field.group)?.short}
            onChange={(value) => onChange(setConfigValue(sections, instance.concretePath, value))}
          />
        )),
      )}
    </div>
  );
}

export function ConfigFieldRow({ instance, advanced, groupName, hint, onChange }: {
  instance: ConfigFieldInstance;
  advanced: boolean;
  groupName?: string;
  hint?: string;
  onChange: (value: unknown) => void;
}) {
  const copy = useStrings().server.config.catalog;
  const { field, value, concretePath, recordLabel } = instance;
  const label = configFieldLabel(field, copy.labels as Record<string, string>);
  const requiresRestart = field.apply.includes("restart");
  return (
    <div className="grid min-h-[74px] max-w-[960px] gap-2 border-b border-border/75 py-3.5 sm:grid-cols-[minmax(180px,1fr)_minmax(170px,225px)] sm:items-center sm:gap-4">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm font-semibold text-text">{label}</span>
          {hint && <HelpHint label={label}>{hint}</HelpHint>}
          {recordLabel && <span className="rounded bg-surface-2 px-1.5 py-0.5 text-micro font-semibold text-text-faint">{recordLabel}</span>}
          {advanced && groupName && <span className="rounded bg-surface-2 px-1.5 py-0.5 text-micro font-semibold text-text-faint">{groupName}</span>}
          {requiresRestart && <span className="rounded bg-warn/10 px-1.5 py-0.5 text-micro font-bold text-warn">restart</span>}
        </div>
        <p className="mt-1 text-meta leading-relaxed text-text-muted">
          {advanced ? configFieldDescription(field, { restart: copy.applyRestart, conditional: copy.applyConditional, reload: copy.applyReload }) : normalFieldHint(field, copy)}
        </p>
        {advanced && <code className="mt-1 block break-all font-mono text-micro text-accent/80">{concretePath}</code>}
      </div>
      <FieldControl field={field} value={value} label={label} onChange={onChange} />
    </div>
  );
}

export function FieldControl({ field, value, label, onChange }: { field: TelemtConfigField; value: unknown; label: string; onChange: (value: unknown) => void }) {
  const copy = useStrings().server.config.catalog;
  const hintID = useId();
  if (field.kind === "boolean") {
    return <div className="grid gap-1 sm:justify-items-end">
      <Toggle checked={value === true} onChange={onChange} aria-label={label} aria-describedby={hintID} />
      <p id={hintID} className="text-xs text-text-muted">{copy.booleanLimit}</p>
    </div>;
  }
  if (field.kind === "enum") {
    return <div className="grid gap-1">
      <Select value={value === false ? "false" : String(value ?? "")} onChange={(event) => onChange(enumValue(field, event.target.value))} aria-label={label} aria-describedby={hintID}>
        {value === undefined && <option value="">{copy.unset}</option>}
        {field.options?.map((option) => <option key={option} value={option}>{option}</option>)}
      </Select>
      <p id={hintID} className="text-xs text-text-muted">{copy.enumLimit}</p>
    </div>;
  }
  if (field.kind === "integer" || field.kind === "decimal") {
    const integer = field.kind === "integer";
    const unsafe = typeof value === "number" && !Number.isSafeInteger(value) && integer;
    const format = (candidate: unknown) => formatConfigNumber(candidate, integer ? "" : field.data_type);
    const hint = integer ? copy.integerLimit.replace("{max}", String(configIntegerMax(field.data_type))) : copy.decimalLimit;
    return <ValidatedInput label={label} value={format(value)} inputMode={integer ? "numeric" : "decimal"} disabled={unsafe}
      hint={unsafe ? copy.exactToml : hint} parse={(text) => parseConfigNumber(text, integer, field.data_type)} format={format} onChange={onChange} />;
  }
  if (field.kind === "string_list" || field.kind === "integer_list") {
    const format = (candidate: unknown) => Array.isArray(candidate) ? candidate.join(", ") : "";
    const integer = field.kind === "integer_list";
    const cidr = field.data_type === "IpNetwork[]";
    const fixedLength = /^\[u64; (\d+)\]$/.exec(field.data_type)?.[1];
    function parse(text: string) {
      const values = text.split(",").map((item) => item.trim()).filter(Boolean);
      if (fixedLength && values.length !== Number(fixedLength)) return undefined;
      if (cidr && values.some((item) => !isConfigNetwork(item))) return undefined;
      if (!integer) return values;
      const parsed = values.map((item) => parseConfigNumber(item, true, "u64"));
      return parsed.includes(undefined) ? undefined : parsed;
    }
    const unsafe = integer && Array.isArray(value) && value.some((item) => typeof item === "number" && !Number.isSafeInteger(item));
    const hint = unsafe ? copy.exactToml : fixedLength ? copy.integerListLimit : cidr ? copy.networkLimit : copy.commaSeparated;
    return <ValidatedInput label={label} value={format(value)} hint={hint} disabled={unsafe} parse={parse} format={format} onChange={onChange} />;
  }
  if (field.kind === "map") {
    return <ConfigMapControl label={label} value={value} multiple={field.path === "dc_overrides"} onChange={onChange} />;
  }
  if (field.kind === "structure") {
    return <p className="text-xs text-text-muted">{copy.structureHint}</p>;
  }
  const text = value === undefined ? "" : String(value);
  if (field.data_type === "IpAddr" || field.data_type === "SocketAddr") {
    const ip = field.data_type === "IpAddr";
    return <ValidatedInput label={label} value={text} hint={ip ? copy.ipLimit : copy.socketLimit}
      parse={(next) => (ip ? isConfigIPAddress(next) : isConfigSocketAddress(next)) ? next : undefined}
      format={String} onChange={onChange} />;
  }
  return <Input type={field.secret ? "password" : "text"} value={text} placeholder={field.default_value === "—" ? copy.unset : field.default_value}
    autoCapitalize="off" spellCheck={false} aria-label={label} onChange={(event) => onChange(event.target.value)} />;
}

function ValidatedInput({ label, value, hint, parse, format, onChange, inputMode, disabled }: {
  label: string;
  value: string;
  hint: string;
  parse: (text: string) => unknown;
  format: (value: unknown) => string;
  onChange: (value: unknown) => void;
  inputMode?: "numeric" | "decimal";
  disabled?: boolean;
}) {
  const id = useId();
  const [draft, setDraft] = useState<{ text: string; accepted: string; invalid: boolean }>();
  const current = draft?.accepted === value ? draft : undefined;
  return (
    <div className="grid gap-1">
      <Input value={current?.text ?? value} inputMode={inputMode} monospace spellCheck={false} autoCapitalize="off" disabled={disabled}
        aria-label={label} aria-invalid={!!current?.invalid} aria-describedby={id} onChange={(event) => {
          const text = event.target.value;
          const parsed = parse(text);
          if (parsed === undefined) {
            setDraft({ text, accepted: value, invalid: true });
          } else {
            setDraft({ text, accepted: format(parsed), invalid: false });
            onChange(parsed);
          }
        }} />
      <p id={id} className={current?.invalid ? "text-xs text-error" : "text-xs text-text-muted"}>{hint}</p>
    </div>
  );
}

function enumValue(field: TelemtConfigField, value: string): string | boolean | number {
  if (value === "false") return false;
  if (field.data_type === "4 or 6") return Number(value);
  return value;
}

function normalFieldHint(field: TelemtConfigField, copy: Dict["server"]["config"]["catalog"]): string {
  if (field.apply.includes("restart")) return copy.applyRestart;
  if (field.path === "web.enabled") return copy.webEnabledHint;
  if (field.path === "general.me2dc_fallback") return copy.fallbackHint;
  if (field.path === "general.use_middle_proxy") return copy.middleProxyHint;
  return `${copy.currentValue} · ${field.data_type}`;
}
