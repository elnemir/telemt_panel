import { createFileRoute, Link } from "@tanstack/react-router";
import { useStrings } from "../../i18n";
import { JournalPage } from "../../journal/JournalPage";

export const Route = createFileRoute("/_authed/journal")({
  component: JournalRoute,
});

function JournalRoute() {
  const s = useStrings();
  return <JournalPage actions={
    <Link className="tap-target inline-flex items-center rounded-lg bg-surface px-3 text-sm font-semibold text-accent"
      to="/pulse/diag/$domain" params={{ domain: "events" }}>{s.journal.runtime}</Link>
  } />;
}
