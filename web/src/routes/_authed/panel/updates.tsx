import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useStrings } from "../../../i18n";
import { listNodesOptions } from "../../../lib/api/generated/@tanstack/react-query.gen";
import { documentNodeId } from "../../../nodes/scope";
import { ServerShell } from "../../../server/ServerShell";
import { UpdatesPage } from "../../../server/updates/UpdatesPage";
import { ErrorState } from "../../../ui/ErrorState";
import { Skeleton } from "../../../ui/Skeleton";

export const Route = createFileRoute("/_authed/panel/updates")({
  component: PanelUpdates,
});

function PanelUpdates() {
  const s = useStrings();
  const registry = useQuery(listNodesOptions());
  const local = registry.data?.nodes.find((node) => node.id === "default" && node.enabled);
  // The existing update engine is available only in the local default workspace.
  if (local && documentNodeId === "default") return <UpdatesPage target="panel" />;
  return (
    <ServerShell section="panel" title={s.panel.updates}>
      {registry.isPending ? <Skeleton className="h-32 w-full" /> : registry.isError ? (
        <ErrorState message={s.nodes.loadError} onRetry={() => registry.refetch()} />
      ) : (
        <section className="rounded-xl bg-surface p-4 text-sm text-text-muted">
          <p>{local ? s.panel.updateLocalHint : s.panel.updateManualHint}</p>
          {local && <Link className="mt-2 inline-flex min-h-11 items-center font-semibold text-accent" to="/panel/updates"
            search={{ node: "default" }} reloadDocument>{s.panel.updateLocalLink}</Link>}
        </section>
      )}
    </ServerShell>
  );
}
