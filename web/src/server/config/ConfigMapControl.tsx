import { useState } from "react";
import { useStrings } from "../../i18n";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";
import { SettingsField } from "../../ui/SettingsField";
import { isPlainObject } from "./configObjectDiff";
import { isConfigHostname, isConfigSocketAddress } from "./configInputValidation";

export function ConfigMapControl({ value, label, multiple, onChange }: {
  value: unknown;
  label: string;
  multiple: boolean;
  onChange: (value: unknown) => void;
}) {
  const copy = useStrings().server.config.catalog;
  const entries = isPlainObject(value) ? value : {};
  const [key, setKey] = useState("");
  const [address, setAddress] = useState("");
  const validKey = multiple ? /^[1-9]\d*$/.test(key) : isConfigHostname(key);
  const keyError = key && (!validKey || Object.hasOwn(entries, key)) ? copy.mapInvalidKey : undefined;
  const keyLabel = multiple ? copy.dcKey : copy.sniKey;
  const addressLimits = multiple ? copy.dcAddressLimit : copy.maskAddressLimit;
  const addresses = (text: string) => (multiple ? text.split(",") : [text]).map((item) => item.trim()).filter(Boolean);
  const validAddresses = (text: string) => addresses(text).length > 0 &&
    addresses(text).every((item) => isConfigSocketAddress(item, !multiple));
  const toValue = (text: string) => multiple ? addresses(text) : text.trim();

  function add() {
    if (!validKey || keyError || !validAddresses(address)) return;
    onChange({ ...entries, [key]: toValue(address) });
    setKey("");
    setAddress("");
  }

  return (
    <div className="grid min-w-0 gap-3" role="group" aria-label={label}>
      {Object.entries(entries).map(([name, entry]) => (
        <AddressEntry key={name} label={name} hint={addressLimits} invalidText={copy.mapInvalidAddress}
          value={Array.isArray(entry) ? entry.join(", ") : String(entry ?? "")}
          valid={validAddresses} onChange={(text) => onChange({ ...entries, [name]: toValue(text) })} />
      ))}
      <p className="text-xs text-text-muted">{copy.mapKeepKeys}</p>
      <SettingsField label={keyLabel} limits={multiple ? copy.dcKeyLimit : copy.sniKeyLimit} error={keyError}>
        {(attrs) => <Input {...attrs} value={key} onChange={(event) => setKey(event.target.value)} autoCapitalize="off" spellCheck={false} />}
      </SettingsField>
      <SettingsField label={copy.mapAddress} limits={addressLimits} error={address && !validAddresses(address) ? copy.mapInvalidAddress : undefined}>
        {(attrs) => <Input {...attrs} value={address} onChange={(event) => setAddress(event.target.value)} autoCapitalize="off" spellCheck={false} />}
      </SettingsField>
      <Button size="sm" variant="secondary" type="button" disabled={!validKey || !!keyError || !validAddresses(address)} onClick={add}>
        {copy.addMapEntry}
      </Button>
    </div>
  );
}

function AddressEntry({ label, hint, invalidText, value, valid, onChange }: {
  label: string;
  hint: string;
  invalidText: string;
  value: string;
  valid: (text: string) => boolean;
  onChange: (text: string) => void;
}) {
  const [edited, setEdited] = useState<{ text: string; current: string }>();
  const text = edited?.current === value ? edited.text : value;
  return (
    <SettingsField label={label} limits={hint} error={edited && !valid(text) ? invalidText : undefined}>
      {(attrs) => <Input {...attrs} value={text} spellCheck={false} autoCapitalize="off" onChange={(event) => {
        const next = event.target.value;
        if (valid(next)) onChange(next);
        setEdited({ text: next, current: valid(next) ? next.split(",").map((part) => part.trim()).filter(Boolean).join(", ") : value });
      }} />}
    </SettingsField>
  );
}
