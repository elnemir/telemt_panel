import type { ReactNode } from "react";
import { useId, useState } from "react";
import { useStrings } from "../i18n";
import { IconActivity, IconJournal } from "../ui/icons";
import { LogsTab } from "./LogsTab";
import { ActionsTab } from "./EventsTab";
import { PageHeader } from "../ui/PageHeader";

type JournalTab = "logs" | "actions";

const TABS: JournalTab[] = ["logs", "actions"];

// Both panes stay mounted while the internal tab changes: the Logs pane
// owns a live EventSource and an in-memory ring that must survive a quick
// look at the administrative action history.
export function JournalPage({ actions }: { actions?: ReactNode } = {}) {
  const s = useStrings();
  const idPrefix = useId();
  const [tab, setTab] = useState<JournalTab>("logs");

  return (
    <div className="journal-page">
      <PageHeader title={s.nav.journal} actions={actions} />
      <section className="journal-surface">
        <div className="journal-tabs" role="tablist" aria-label={s.nav.journal}>
          {TABS.map((id) => {
            const Glyph = id === "logs" ? IconActivity : IconJournal;
            return (
              <button
                key={id}
                type="button"
                role="tab"
                id={`${idPrefix}-${id}-tab`}
                aria-controls={`${idPrefix}-${id}-panel`}
                aria-selected={tab === id}
                tabIndex={tab === id ? 0 : -1}
                onKeyDown={event => {
                  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
                  event.preventDefault();
                  const next = event.key === "Home" ? "logs" : event.key === "End" ? "actions" : id === "logs" ? "actions" : "logs";
                  setTab(next);
                  document.getElementById(`${idPrefix}-${next}-tab`)?.focus();
                }}
                className={tab === id ? "is-active" : undefined}
                onClick={() => setTab(id)}
              >
                <Glyph aria-hidden="true" />
                <span>
                  <strong>{s.journal.tabs[id]}</strong>
                  <small>{s.journal.tabDescriptions[id]}</small>
                </span>
              </button>
            );
          })}
        </div>

        <div className="journal-pane-host" role="tabpanel" id={`${idPrefix}-logs-panel`} aria-labelledby={`${idPrefix}-logs-tab`} hidden={tab !== "logs"}>
          <LogsTab />
        </div>
        <div className="journal-pane-host" role="tabpanel" id={`${idPrefix}-actions-panel`} aria-labelledby={`${idPrefix}-actions-tab`} hidden={tab !== "actions"}>
          <ActionsTab />
        </div>
      </section>
    </div>
  );
}
