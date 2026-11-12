import type { ComponentType, ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useSnapshot } from "../../realtime";
import type { StatsSnapshot } from "../../realtime/topics";
import { Sparkline, type SparklineTone } from "../../ui/Sparkline";
import { Skeleton } from "../../ui/Skeleton";
import {
  IconActivity,
  IconPeople,
  IconShieldAlert,
  IconTraffic,
  type IconProps,
} from "../../ui/icons";
import { fill, formatNumber, useStrings } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatBytes } from "../../lib/format";
import { useHistorySeries } from "../useHistorySeries";
import type { HistorySeries } from "../../lib/api/generated/types.gen";
import type { DiagDomain } from "../types";
import {
  computeStatRowValues,
  connectionQuality,
  deltaSparklineValues,
  historyWindowDelta,
  lastHistoryValue,
  peakHistoryValue,
  qualityCaption,
  qualitySparklineValues,
  round1,
  sparklineValues,
  windowSeries,
} from "./statRow.helpers";

// One live metric and one responsive tile, preserving coverage and unknowns.
interface Metric {
  key: string;
  Icon: ComponentType<IconProps>;
  tone: SparklineTone;
  label: string;
  value: ReactNode;
  /** Secondary line — omitted when there is nothing real to say. */
  caption?: string;
  series: number[];
  /** The Пульс page this metric's full story lives on. */
  domain: DiagDomain;
}

/** Below this share of successful connections the quality tile turns warn. */
const QUALITY_WARN_PCT = 98;
/** …and so does a decline of more than this many points across the window. */
const QUALITY_DROP_POINTS = -1;

const TONE_TEXT: Record<SparklineTone, string> = {
  accent: "text-accent",
  ok: "text-ok",
  warn: "text-warn",
  error: "text-error",
  muted: "text-text-muted",
};

// The same live metric is rendered once at every viewport. Charts remain
// available as real history; the large value and coverage caption lead.
function Tile({ metric }: { metric: Metric }) {
  const { Icon } = metric;
  return (
    <Link to="/pulse/diag/$domain" params={{ domain: metric.domain }}
      className={cn("overview-metric", metric.key === "connections" && "overview-metric-accent", metric.key === "quality" && metric.tone === "ok" && "overview-metric-ok")}>
      <span className="flex min-w-0 items-start gap-2 text-xs font-bold text-text-muted">
        <Icon className={cn("h-4 w-4 shrink-0", TONE_TEXT[metric.tone])} />
        <span className="min-w-0 break-words">{metric.label}</span>
      </span>
      <span className="overview-metric-value">{metric.value}</span>
      <span className="text-xs leading-relaxed text-text-muted">{metric.caption}</span>
      {metric.series.length >= 2 && <span aria-hidden="true" className="mt-auto block h-8"><Sparkline values={metric.series} tone={metric.tone} decorative /></span>}
    </Link>
  );
}

// StatRow — «Показатели»: connections · active users · traffic · connection
// quality, each with its own /api/history series (06-ui.md's default second
// widget; the fourth KPI per the dashboard concept §5 — «нормально ли
// устанавливаются соединения», with «Проблемы» below explaining why not).
//
// Window figures use observed deltas from live samples and disk aggregates,
// with partial coverage explicitly labelled. Lifetime totals stay captions.
//
// The uptime metric is gone: the status banner carries it now, beside the
// version and the route mode, and a dashboard does not need the same figure
// twice.
export function StatRow() {
  const s = useStrings();
  const stats = useSnapshot<StatsSnapshot>("stats");
  const connectionsHistory = useHistorySeries("connections");
  const usersHistory = useHistorySeries("active_users");
  const trafficHistory = useHistorySeries("traffic");
  const refusalsHistory = useHistorySeries("refusals");
  const attemptsHistory = useHistorySeries("attempts");

  if (!stats.data) {
    return (
      <div className="grid grid-cols-2 gap-3 md:gap-4 lg:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton key={index} className="h-40 w-full rounded-xl" />
        ))}
      </div>
    );
  }

  const values = computeStatRowValues(stats.data);
  // /api/history now returns thirty minutes (useHistorySeries) so the
  // quality tile can compare two windows. Everything shown is still the last
  // fifteen, so every other reading is taken from the cut series — the
  // fetch got wider, the captions did not.
  const connectionsWindow = windowSeries(connectionsHistory.data);
  const usersWindow = windowSeries(usersHistory.data);
  const trafficWindow = windowSeries(trafficHistory.data);
  const attemptsWindow = windowSeries(attemptsHistory.data);
  const refusalsWindow = windowSeries(refusalsHistory.data);
  const traffic = historyWindowDelta(trafficWindow);
  const trafficTotal = lastHistoryValue(trafficWindow);
  const quality = connectionQuality(attemptsHistory.data, refusalsHistory.data);
  // Peak is only meaningful for the two instantaneous gauges: on a
  // cumulative counter the maximum is just its last point.
  const peakCaption = (peak: number | null) =>
    peak === null ? undefined : `${s.pulse.stat.peak15m} — ${peak}`;
  const totalCaption = (total: string | null) =>
    total === null ? undefined : fill(s.pulse.stat.totalAllTime, { value: total });
  const historyCaption = (history: HistorySeries | undefined, fallback?: string) => {
    if (!history || history.state === "empty") return s.pulse.stat.historyCollecting;
    if (history.state === "disabled") return s.pulse.stat.historyDisabled;
    if (history.source_available === false) return s.pulse.stat.sourceUnavailable;
    if (history.state === "partial") return s.pulse.stat.historyPartial;
    return fallback;
  };

  const metrics: Metric[] = [
    {
      key: "connections",
      Icon: IconActivity,
      tone: "accent",
      label: values.connectionsApprox ? s.pulse.stat.connectionsApprox : s.pulse.stat.connections,
      value: values.connections ?? "—",
      caption: historyCaption(connectionsWindow, peakCaption(peakHistoryValue(connectionsWindow))),
      series: sparklineValues(connectionsWindow),
      domain: "connections",
    },
    {
      key: "active_users",
      Icon: IconPeople,
      tone: "ok",
      label: values.activeUsersApprox ? s.pulse.stat.activeUsersApprox : s.pulse.stat.activeUsers,
      value: values.activeUsers ?? "—",
      caption: historyCaption(usersWindow, peakCaption(peakHistoryValue(usersWindow))),
      series: sparklineValues(usersWindow),
      domain: "connections",
    },
    {
      key: "traffic",
      Icon: IconTraffic,
      tone: "accent",
      label: s.pulse.stat.traffic,
      value: traffic !== null ? formatBytes(traffic, s) : "—",
      caption: historyCaption(trafficWindow, totalCaption(trafficTotal === null ? null : formatBytes(trafficTotal, s))),
      series: deltaSparklineValues(trafficWindow),
      domain: "connections",
    },
    {
      key: "quality",
      Icon: IconShieldAlert,
      // Warn when connections are failing at a rate worth looking at, or
      // when the rate is getting worse inside the window — not for a burst
      // that has already stopped, which the reader would learn to ignore.
      tone:
        quality.percent !== null &&
        (quality.percent < QUALITY_WARN_PCT ||
          (quality.changePoints !== null && quality.changePoints < QUALITY_DROP_POINTS))
          ? "warn"
          : "ok",
      label: s.pulse.stat.quality,
      value: quality.percent === null ? "—" : `${formatNumber(s, round1(quality.percent))} %`,
      // The concept's own preferred caption (§5): how the figure MOVED,
      // this window against the previous one. Until a previous window exists
      // — the first quarter-hour after a panel start — it falls back to
      // naming the refusals rather than claiming a comparison it cannot make.
      caption: historyCaption(
        attemptsWindow?.state !== "ready" ? attemptsWindow : refusalsWindow,
        qualityCaption(quality, s),
      ),
      series: qualitySparklineValues(attemptsWindow, refusalsWindow),
      domain: "counters",
    },
  ];

  return (
    <section
      aria-label={s.pulse.widgets.stat_row}
      className="grid grid-cols-2 gap-3 md:gap-4 lg:grid-cols-4"
      data-testid="kpi-grid"
    >
      {metrics.map((m) => (
        <Tile key={m.key} metric={m} />
      ))}
    </section>
  );
}
