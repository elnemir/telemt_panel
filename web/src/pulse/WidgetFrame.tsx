import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { cn } from "../lib/cn";
import { useStrings } from "../i18n";
import { StatePill } from "../ui/StatePill";
import type { DiagDomain } from "./types";
import { WidgetActionLabel } from "./WidgetActionLabel";
import { widgetActionClassName } from "./widgetActionStyles";

export interface WidgetFrameProps {
  title: string;
  /**
   * Native tooltip on the title — a fact ABOUT the widget that is not one of
   * its rows. «События» uses it for Telemt's `dropped_total`: the records
   * evicted from its fifty-slot ring, which the panel will never show and
   * which was costing a line in a five-line feed.
   */
  titleTooltip?: string;
  /** Links to the matching details page, when the widget has one. */
  diagDomain?: DiagDomain;
  /** SSE topic staleness (useSnapshot's `.stale`) — shown as a badge, data stays visible underneath. */
  stale?: boolean;
  /** Trailing slot on the title row, before the actions (a count, a state pill). */
  badge?: ReactNode;
  /**
   * The widget's own destination at the right of the title row. Every
   * overview destination uses the same «Детали →» label and button style.
   */
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}

// WidgetFrame is the one card shell every dashboard widget renders inside —
// title, optional drill-down link and optional stale
// badge (06-ui.md: widgets reuse shared primitives, "никаких 11 самодельных
// вёрсток"). A widget in the Gated/loading/error state still renders inside
// this frame (its own body decides what to show), so the title/hide/diag
// affordances stay available regardless of the underlying data's state.
//
// Figma card shell: 20px radius, 24px desktop spacing and a flat surface.
export function WidgetFrame({
  title,
  titleTooltip,
  diagDomain,
  stale,
  badge,
  action,
  className,
  children,
}: WidgetFrameProps) {
  const s = useStrings();
  return (
    <div className={cn("flex min-w-0 flex-col gap-4 rounded-xl bg-surface p-4 sm:p-6", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex min-w-0 items-center gap-2">
          {/* h2, not h3: a widget is a top-level section of Сводка and there
              is no intermediate heading above it — h1 straight to h3 left a
              gap in the outline a screen reader reads as a missing level. */}
          <h2 className="break-words text-lg font-bold text-text" title={titleTooltip}>
            {title}
          </h2>
          {badge}
          {stale && <StatePill state="warn">{s.common.stale}</StatePill>}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          {action}
          {diagDomain && (
            <Link
              to="/pulse/diag/$domain"
              params={{ domain: diagDomain }}
              className={widgetActionClassName}
              data-testid="widget-action"
            >
              <WidgetActionLabel />
            </Link>
          )}
        </div>
      </div>
      {children}
    </div>
  );
}
