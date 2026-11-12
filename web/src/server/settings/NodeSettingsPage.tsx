import { GeographySettings } from "../../geography/GeographySettings";
import { useStrings } from "../../i18n";
import { ServerShell } from "../ServerShell";
import { LinkSettings } from "./LinkSettings";
import { StorageSettings } from "./StorageSettings";

export function NodeSettingsPage() {
  const copy = useStrings().server.menu.settings;
  return (
    <ServerShell title={copy.title}>
      <div className="grid min-w-0 items-start gap-4 lg:grid-cols-2">
        <LinkSettings />
        <div id="geography" className="min-w-0 scroll-mt-24">
          <GeographySettings />
        </div>
      </div>
      <div id="storage" className="scroll-mt-24">
        <StorageSettings />
      </div>
    </ServerShell>
  );
}
