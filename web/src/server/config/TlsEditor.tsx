import type { TelemtConfigField } from "../../lib/api/generated/types.gen";
import { useStrings } from "../../i18n";
import { Input } from "../../ui/Input";
import { SettingsField } from "../../ui/SettingsField";
import { IconTrash } from "../../ui/icons";
import { getConfigValue, setConfigValue } from "./configCatalog.helpers";
import { Subsection, AddRecordButton, RecordListEmpty, RecordIconButton } from "./ConfigEditorControls";
import { GenericFields } from "./ConfigFieldControls";
import { ConfigMapControl } from "./ConfigMapControl";
import { isConfigHostname } from "./configInputValidation";

export function TlsEditor({ fields, sections, advanced, onChange }: {
  fields: TelemtConfigField[];
  sections: Record<string, unknown>;
  advanced: boolean;
  onChange: (next: Record<string, unknown>) => void;
}) {
  const copy = useStrings().server.config.catalog;
  const specialized = new Set(["censorship.tls_domains", "censorship.exclusive_mask"]);
  const ordinaryFields = fields.filter((field) => !specialized.has(field.path));
  const rawDomains = getConfigValue(sections, "censorship.tls_domains");
  const tlsDomains = Array.isArray(rawDomains) ? rawDomains.map(String) : [];
  const maskMap = asStringMap(getConfigValue(sections, "censorship.exclusive_mask"));
  const maskEntries = Object.entries(maskMap);

  return (
    <div>
      <GenericFields fields={ordinaryFields} sections={sections} advanced={advanced} onChange={onChange} />

      {fields.some((field) => field.path === "censorship.tls_domains") && (
        <Subsection title={copy.additionalTlsDomains} count={tlsDomains.length}>
          <RecordListEmpty show={tlsDomains.length === 0}>{copy.noAdditionalDomains}</RecordListEmpty>
          {tlsDomains.map((domain, index) => (
            <div key={index} className="grid min-h-[62px] grid-cols-[minmax(0,1fr)_44px] items-center gap-2 border-b border-border py-2 first:border-t">
              <SettingsField label={`${copy.tlsDomain} ${index + 1}`} limits={copy.sniKeyLimit}
                error={!isConfigHostname(domain) ? copy.mapInvalidKey : undefined}>
                {(attrs) => <Input {...attrs} value={domain} placeholder="cdn.example.com" autoCapitalize="off" spellCheck={false}
                  onChange={(event) => {
                    const next = [...tlsDomains];
                    next[index] = event.target.value;
                    onChange(setConfigValue(sections, "censorship.tls_domains", next));
                  }} />}
              </SettingsField>
              <RecordIconButton label={copy.deleteRecord} danger onClick={() => onChange(setConfigValue(sections, "censorship.tls_domains", tlsDomains.filter((_, itemIndex) => itemIndex !== index)))}>
                <IconTrash className="size-4" />
              </RecordIconButton>
            </div>
          ))}
          <AddRecordButton onClick={() => onChange(setConfigValue(sections, "censorship.tls_domains", [...tlsDomains, ""]))}>{copy.addTlsDomain}</AddRecordButton>
        </Subsection>
      )}

      {fields.some((field) => field.path === "censorship.exclusive_mask") && (
        <Subsection title={copy.exclusiveMasks} count={maskEntries.length}>
          <p className="mb-3 text-meta leading-relaxed text-text-muted">{copy.exclusiveMasksHint}</p>
          <RecordListEmpty show={maskEntries.length === 0}>{copy.noExclusiveMasks}</RecordListEmpty>
          <ConfigMapControl label={copy.exclusiveMasks} value={maskMap} multiple={false}
            onChange={(next) => onChange(setConfigValue(sections, "censorship.exclusive_mask", next))} />
        </Subsection>
      )}
    </div>
  );
}

function asStringMap(value: unknown): Record<string, string> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, String(item)]));
}
