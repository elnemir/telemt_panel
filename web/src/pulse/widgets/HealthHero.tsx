import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useSnapshot } from "../../realtime";
import type { RuntimeTopic, StatsSnapshot, UpstreamsTopic } from "../../realtime/topics";
import { buttonClasses } from "../../ui/buttonStyles";
import { Skeleton } from "../../ui/Skeleton";
import { StatePill } from "../../ui/StatePill";
import { IconUpgrade, IconWarning } from "../../ui/icons";
import { getUpdatesOptions } from "../../lib/api/generated/@tanstack/react-query.gen";
import { fill, useStrings } from "../../i18n";
import { cn } from "../../lib/cn";
import {
  clearPendingChanges,
  getPendingChanges,
  resolvePendingChanges,
} from "../../server/config/pendingChanges";
import { computeHealthHero, telemtUpdateVersion, type HeroFact } from "./healthHero.helpers";
import { dcCoverageState } from "./dc.helpers";
import { resolveGated } from "./gated";
import { computeMeCard } from "./mePool.helpers";

// Fact — one of the small-caps pairs at the banner's right edge: the label
// above in the same caps grammar the app's section labels use, the value
// under it in tabular figures so three facts side by side stay aligned.
function Fact({ fact, updateVersion }: { fact: HeroFact; updateVersion: string | null }) {
  const mobileUpdate = fact.key === "version" && updateVersion;
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate text-micro font-semibold uppercase tracking-[0.06em] text-inherit">
        {fact.label}
      </span>
      {mobileUpdate ? (
        <>
          <Link
            to="/server/updates"
            className="-my-2 inline-flex min-h-11 min-w-0 items-center gap-1 text-row font-semibold tabular-nums text-inherit sm:hidden"
          >
            <span className="truncate">{fact.value}</span>
            <IconUpgrade className="h-3 w-3 shrink-0 text-inherit" />
            <span className="truncate text-inherit">{updateVersion}</span>
          </Link>
          <span className="hidden truncate text-row font-semibold tabular-nums text-inherit sm:block">
            {fact.value}
          </span>
        </>
      ) : (
        <span className="truncate text-row font-semibold tabular-nums text-inherit">{fact.value}</span>
      )}
    </div>
  );
}

// The hero presents the selected node's computed health. The scope caption,
// reason, freshness and unapplied-config notice stay distinct signals; a
// saved configuration never implies successful runtime convergence.
export function HealthHero() {
  const s = useStrings();
  const stats = useSnapshot<StatsSnapshot>("stats");
  const runtime = useSnapshot<RuntimeTopic>("runtime");
  const upstreams = useSnapshot<UpstreamsTopic>("upstreams");
  let meDegraded = false;
  if (runtime.data) {
    const pool = resolveGated(runtime.data.me_pool_state);
    const quality = resolveGated(runtime.data.me_quality);
    if (pool.status === "ok") {
      meDegraded =
        computeMeCard(
          pool.data,
          quality.status === "ok" ? quality.data : undefined,
          runtime.data.gates,
        ).reason !== null;
    }
  }
  const dcDegraded =
    upstreams.data?.dcs?.dcs.some((dc) => dcCoverageState(dc) !== "ok") ?? false;
  const view = computeHealthHero(
    {
      stats: stats.data,
      runtime: runtime.data,
      unreachable: stats.error !== null,
      degraded:
        Boolean(stats.data?.health?.read_only) ||
        runtime.error !== null ||
        upstreams.error !== null ||
        meDegraded ||
        dcDegraded,
    },
    s,
  );
  // Read once per mount: only the Конфигурация page writes it, and getting
  // back here means a remount.
  const [pending] = useState(getPendingChanges);
  const unapplied = resolvePendingChanges(pending, stats.data);
  const stillPending = unapplied.runtimeReload || unapplied.processRestart;
  useEffect(() => {
    if (pending && !stillPending) clearPendingChanges();
  }, [pending, stillPending]);
  // The release list behind the chip: a plain REST resource with its own
  // cache upstream (the update engine's), so a long staleTime and no retry
  // — the banner must never wait on GitHub, and an unreachable GitHub just
  // means no chip.
  const updates = useQuery({ ...getUpdatesOptions(), staleTime: 5 * 60_000, retry: false });
  const updateVersion = telemtUpdateVersion(updates.data);

  if (!view) {
    return (
      <section aria-label={s.pulse.widgets.health_hero}>
        <Skeleton className="h-[220px] w-full rounded-xl" />
      </section>
    );
  }


  return (
    <section aria-label={s.pulse.widgets.health_hero} data-testid="status-banner">
      <div
        className="overview-health-hero"
        data-health={view.tone}
      >
        <p className="mb-4 text-xs font-bold">{s.overview.heroEyebrow}</p>
        <div className="flex flex-col gap-5">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
              <span role="status" className="text-2xl font-bold leading-[1.45] sm:text-[32px]">
                {view.label}
              </span>
              {/* Staleness is a different axis from health — the data on
                  screen may be minutes old while Telemt itself is fine —
                  and 06-ui.md requires every screen to say so. */}
              {stats.stale && <StatePill state="warn" className="hero-status">{s.common.stale}</StatePill>}
            </div>
            {view.reason && (
              <p className="mt-2 text-sm leading-relaxed text-inherit">{view.reason}</p>
            )}
            {/* Present only when Telemt is running something other than what
                its config file says — absent in the normal case. */}
            {stillPending && (
              <p className="mt-1.5 flex items-center gap-1.5 text-meta text-inherit">
                <IconWarning aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
                {unapplied.processRestart
                  ? s.overview.status.pendingProcessRestart
                  : s.overview.status.pendingRuntimeReload}
              </p>
            )}
          </div>

          <div className="flex w-full flex-wrap items-center gap-x-4 gap-y-2 sm:w-auto sm:gap-x-6 sm:gap-y-2.5">
            {view.facts.map((fact) => (
              <Fact key={fact.key} fact={fact} updateVersion={updateVersion} />
            ))}
            {updateVersion && (
              <Link
                to="/server/updates"
                className={cn(
                  "hidden min-h-[32px] shrink-0 items-center gap-1.5 rounded-full px-3 sm:inline-flex",
                  "bg-accent/12 text-micro font-semibold text-inherit transition-colors hover:bg-accent/20",
                )}
              >
                <IconUpgrade className="h-3.5 w-3.5" />
                <span>{fill(s.overview.status.updateAvailable, { version: updateVersion })}</span>
              </Link>
            )}
          </div>
        </div>
        <Link to="/pulse" className={buttonClasses("primary", "md", "hero-action mt-5 self-start")}>
          {s.overview.diagnostics}
        </Link>
      </div>
    </section>
  );
}
