import { NodeWorkspace } from "../nodes/NodeSelector";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { cn } from "../lib/cn";
import { useStrings } from "../i18n";
import { StatusStrip } from "./StatusStrip";
import { HeaderMenu } from "./HeaderMenu";
import {
  MORE_NAV_ITEMS,
  COMPACT_NAV_ITEMS,
  SIDEBAR_MANAGEMENT_NAV_ITEMS,
  SIDEBAR_OVERVIEW_NAV_ITEMS,
  isNavItemActive,
  type NavItem,
} from "./nav";
import { useKeyboardInset } from "./useKeyboardInset";
import { useLogout } from "../auth/useLogout";
import { useAuthDisabled } from "../auth/useAuthDisabled";
import { AuthDisabledNotice } from "../auth/AuthDisabledNotice";
import { Sheet } from "../ui/Sheet";
import { IconLogout, IconMore, IconServer, IconJournal, IconInfo } from "../ui/icons";
import { HistoryNotice } from "./HistoryNotice";
import { PanelLogo } from "../branding/branding";
import { useBranding } from "../branding/useBranding";
import { helpTopicForPath } from "../documentation/topics";
import { buttonClasses } from "../ui/buttonStyles";

function BrandMark({ className }: { className?: string }) {
  const branding = useBranding();
  return <PanelLogo branding={branding} className={cn("h-10 w-10 shrink-0 rounded-xl object-contain", className)} />;
}

// One information architecture in three geometries. The full sidebar starts
// only when its 240px width still leaves the Overview grid enough room.
export function Shell({ children }: { children: ReactNode }) {
  const s = useStrings();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const panelSection = isNavItemActive("/panel", pathname);
  const documentationSection = isNavItemActive("/documentation", pathname);
  const Workspace = panelSection || documentationSection ? Fragment : NodeWorkspace;
  const helpTopic = helpTopicForPath(pathname);
  const keyboardInset = useKeyboardInset();
  const [mobileMoreOpen, setMobileMoreOpen] = useState(false);
  const [railMoreOpen, setRailMoreOpen] = useState(false);
  const ownsLayout = pathname === "/people" || pathname.startsWith("/people/");
  const managementActive = MORE_NAV_ITEMS.some((item) => isNavItemActive(item.to, pathname)) || pathname === "/nodes" || pathname === "/clusters" || pathname === "/billing";

  useEffect(() => {
    if (!railMoreOpen) return;
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") setRailMoreOpen(false);
    };
    document.addEventListener("keydown", close);
    return () => document.removeEventListener("keydown", close);
  }, [railMoreOpen]);

  return (
    <div data-panel-shell="" className="flex h-dvh flex-col overflow-hidden min-[600px]:flex-row">
      <FullSidebar pathname={pathname} />

      <aside data-testid="navigation-rail" className="relative hidden w-16 shrink-0 flex-col items-center border-r border-border bg-surface py-3 min-[600px]:flex min-[1180px]:hidden">
        <BrandMark className="h-8 w-8" />
        <nav className="mt-5 flex flex-col gap-2" aria-label={s.shell.navLabel}>
          {COMPACT_NAV_ITEMS.map((item) => (
            <RailLink key={item.to} item={item} active={isNavItemActive(item.to, pathname)} />
          ))}
        </nav>
        <button
          type="button"
          className={cn(
            "tap-target mt-auto flex w-11 items-center justify-center rounded-lg text-text-faint transition-colors hover:bg-surface-2 hover:text-text",
            (managementActive || railMoreOpen) && "bg-accent/14 text-accent",
          )}
          aria-label={s.nav.more}
          aria-haspopup="menu"
          aria-expanded={railMoreOpen}
          onClick={() => setRailMoreOpen((open) => !open)}
        >
          <IconMore className="h-5 w-5" />
        </button>
        {railMoreOpen && (
          <>
            <button
              type="button"
              className="fixed inset-0 z-30 cursor-default"
              aria-label={s.common.close}
              onClick={() => setRailMoreOpen(false)}
            />
            <div className="fixed bottom-3 left-[72px] z-40 w-64 rounded-xl border border-border bg-surface p-2 shadow-2xl">
              <SecondaryLinks pathname={pathname} onNavigate={() => setRailMoreOpen(false)} />
            </div>
          </>
        )}
      </aside>

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <Workspace>
        <main
          className={cn(
            "flex min-h-0 flex-1 flex-col",
            ownsLayout
              ? "overflow-hidden"
              : "overflow-y-auto p-4 pb-[84px] min-[600px]:p-6 min-[1180px]:p-8",
          )}
        >
          <div
            className={cn(
              "flex w-full min-w-0 flex-1 flex-col",
              // Only pages with their own scrollers may shrink below content height.
              // Ordinary pages must retain main's bottom padding after their content.
              ownsLayout && "min-h-0",
            )}
            data-testid="page-frame"
          >
            {!panelSection && !documentationSection && <HistoryNotice />}
            <AuthDisabledNotice />
            {helpTopic && (
              <div className={cn("flex shrink-0 justify-end pb-2", ownsLayout && "px-4 pt-2 sm:px-6")}>
                <Link to="/documentation/$topic" params={{ topic: helpTopic }}
                  className={buttonClasses("secondary", "sm")}>
                  <IconInfo className="h-4 w-4" aria-hidden="true" />{s.documentation.help}
                </Link>
              </div>
            )}
            {children}
          </div>
        </main>
        </Workspace>

        <nav
          data-testid="mobile-bottom-nav"
          className="fixed inset-x-0 bottom-0 z-40 flex min-h-[60px] border-t border-border bg-surface pb-safe min-[600px]:hidden"
          style={{ bottom: keyboardInset }}
          aria-label={s.shell.navLabel}
        >
          {COMPACT_NAV_ITEMS.map((item) => (
            <BottomLink key={item.to} item={item} active={isNavItemActive(item.to, pathname)} />
          ))}
          <button
            type="button"
            className={cn(
              "tap-target flex flex-auto flex-col items-center justify-center gap-0.5 py-1 text-[10px] font-semibold",
              managementActive || mobileMoreOpen ? "text-accent" : "text-text-faint",
            )}
            aria-label={s.nav.more}
            aria-haspopup="dialog"
            aria-expanded={mobileMoreOpen}
            onClick={() => setMobileMoreOpen(true)}
          >
            <span
              className={cn(
                "flex h-7 min-w-10 items-center justify-center rounded-lg",
                (managementActive || mobileMoreOpen) && "bg-accent/14",
              )}
            >
              <IconMore className="h-5 w-5" />
            </span>
            {s.nav.more}
          </button>
        </nav>
      </div>

      <Sheet
        open={mobileMoreOpen}
        onClose={() => setMobileMoreOpen(false)}
        title={s.nav.more}
        placement="bottom"
      >
        <SecondaryLinks pathname={pathname} onNavigate={() => setMobileMoreOpen(false)} />
      </Sheet>
    </div>
  );
}

function FullSidebar({ pathname }: { pathname: string }) {
  const s = useStrings();
  const branding = useBranding();
  const logout = useLogout();
  const authDisabled = useAuthDisabled();
  return (
    <aside data-testid="full-sidebar" className="hidden w-[236px] shrink-0 flex-col overflow-y-auto bg-surface px-6 py-6 min-[1180px]:flex">
      <div className="flex flex-wrap items-center gap-2.5 border-b border-border pb-5 mb-5">
        {branding.logo_mode === "default" && branding.title === "Telemt Panel"
          ? <span className="text-[32px] font-bold leading-[1.45] text-accent">telemt</span>
          : <><BrandMark /><span title={branding.title} className="min-w-0 flex-1 break-words text-lg font-bold text-text">{branding.title}</span></>}
        <span className="w-full text-xs font-bold text-text-muted">{s.shell.controlSpace}</span>
      </div>
      <SidebarGroup label={s.shell.overviewGroup} items={SIDEBAR_OVERVIEW_NAV_ITEMS} pathname={pathname} />
      <SidebarGroup
        label={s.shell.managementGroup}
        items={SIDEBAR_MANAGEMENT_NAV_ITEMS}
        pathname={pathname}
        className="mt-5"
      />
      <div className="mt-5"><p className="px-2.5 pb-1.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-faint">{s.shell.platformGroup}</p><PlatformLinks pathname={pathname} /></div>
      <div className="mt-auto flex flex-col gap-2 pt-4">
        {!isNavItemActive("/panel", pathname) && <StatusStrip variant="card" />}
        <div className="flex items-center gap-1">
          {!authDisabled && <button
            type="button"
            onClick={() => logout.mutate({})}
            disabled={logout.isPending}
            className={cn(
              "tap-target flex flex-1 items-center gap-2.5 rounded-md px-2.5 text-row font-medium",
              "text-text-faint transition-colors hover:bg-surface-2 hover:text-text",
              "disabled:cursor-not-allowed disabled:opacity-50",
            )}
          >
            <IconLogout className="h-4 w-4 shrink-0" />
            {s.auth.signOut}
          </button>}
          <HeaderMenu />
        </div>
      </div>
    </aside>
  );
}

function SidebarGroup({
  label,
  items,
  pathname,
  className,
}: {
  label: string;
  items: readonly NavItem[];
  pathname: string;
  className?: string;
}) {
  const s = useStrings();
  return (
    <div className={className}>
      <p className="px-2.5 pb-1.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-faint">
        {label}
      </p>
      <nav className="flex flex-col gap-1" aria-label={`${s.shell.navLabel}: ${label}`}>
        {items.map(({ to, labelKey, Icon }) => {
          const active = isNavItemActive(to, pathname);
          return (
            <Link
              key={to}
              to={to}
              aria-current={active ? "page" : undefined}
              className={cn(
                "tap-target flex items-center gap-2.5 rounded-[10px] px-2.5 text-sm font-medium",
                "transition-colors hover:bg-surface-2 hover:text-text",
                active ? "bg-[rgb(var(--lavender))] text-accent" : "text-text-muted",
              )}
            >
              <Icon className="h-[18px] w-[18px] shrink-0" />
              {s.nav[labelKey]}
            </Link>
          );
        })}
      </nav>
    </div>
  );
}

function RailLink({ item, active }: { item: NavItem; active: boolean }) {
  const s = useStrings();
  const { to, labelKey, Icon } = item;
  return (
    <Link
      to={to}
      title={s.nav[labelKey]}
      aria-label={s.nav[labelKey]}
      aria-current={active ? "page" : undefined}
      className={cn(
        "tap-target flex w-11 items-center justify-center rounded-lg transition-colors hover:bg-surface-2 hover:text-text",
        active ? "bg-[rgb(var(--lavender))] text-accent" : "text-text-muted",
      )}
    >
      <Icon className="h-5 w-5" />
    </Link>
  );
}

function BottomLink({ item, active }: { item: NavItem; active: boolean }) {
  const s = useStrings();
  const { to, labelKey, Icon } = item;
  return (
    <Link
      to={to}
      className={cn(
        "tap-target flex flex-auto flex-col items-center justify-center gap-0.5 py-1 text-[10px] font-semibold",
        active ? "text-accent" : "text-text-faint",
      )}
      aria-current={active ? "page" : undefined}
    >
      <span className={cn("flex h-7 min-w-10 items-center justify-center rounded-lg", active && "bg-accent/14")}>
        <Icon className="h-5 w-5" />
      </span>
      {s.nav[labelKey]}
    </Link>
  );
}

function SecondaryLinks({ pathname, onNavigate }: { pathname: string; onNavigate: () => void }) {
  const s = useStrings();
  const logout = useLogout();
  const authDisabled = useAuthDisabled();
  return (
    <div role="menu" className="flex flex-col gap-1">
      <PlatformLinks onNavigate={onNavigate} menu />
      {MORE_NAV_ITEMS.map(({ to, labelKey, Icon }) => (
        <Link
          key={to}
          to={to}
          role="menuitem"
          aria-current={isNavItemActive(to, pathname) ? "page" : undefined}
          onClick={onNavigate}
          className="tap-target flex items-center gap-2.5 rounded-[10px] px-2.5 text-sm font-medium text-text-muted transition-colors hover:bg-surface-2 hover:text-text"
        >
          <Icon className="h-5 w-5 shrink-0" />
          {s.nav[labelKey]}
        </Link>
      ))}
      <div className="my-1 border-t border-border" />
      {!authDisabled && <button
        type="button"
        role="menuitem"
        disabled={logout.isPending}
        onClick={() => logout.mutate({})}
        className="tap-target flex items-center gap-3 rounded-lg px-3 text-left text-row font-semibold text-text-muted transition-colors hover:bg-surface-2 hover:text-text disabled:opacity-50"
      >
        <IconLogout className="h-5 w-5 shrink-0" />
        {s.auth.signOut}
      </button>}
    </div>
  );
}

// Platform destinations never change the document-bound node scope.
function PlatformLinks({ pathname, onNavigate, menu = false }: { pathname?: string; onNavigate?: () => void; menu?: boolean }) {
  const s = useStrings();
  const items = [
    { to: "/nodes", label: s.nodes.registry, Icon: IconServer },
    { to: "/clusters", label: s.clusters.title, Icon: IconServer },
    { to: "/billing", label: s.billing.title, Icon: IconJournal },
  ];
  return <div className="flex flex-col gap-1">{items.map(({ to, label, Icon }) =>
    <Link key={to} to={to} role={menu ? "menuitem" : undefined} onClick={onNavigate}
      aria-current={pathname === to ? "page" : undefined}
      className={cn("tap-target flex items-center gap-2.5 rounded-[10px] px-2.5 text-sm font-medium hover:bg-surface-2 hover:text-text", pathname === to ? "bg-[rgb(var(--lavender))] text-accent" : "text-text-muted")}>
      <Icon className="h-[18px] w-[18px] shrink-0" />{label}
    </Link>
  )}</div>;
}
