import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useStrings } from "../i18n";
import { IconChevronLeft } from "../ui/icons";
import { PageHeader } from "../ui/PageHeader";

// Shared page chrome; each subpage retains its own content layout.
export function ServerShell({
  title,
  children,
  section = "server",
}: {
  title: string;
  children: ReactNode;
  section?: "server" | "panel";
}) {
  const s = useStrings();
  const parent = section === "panel" ? { to: "/panel", label: s.nav.panel, back: s.panel.back } as const : { to: "/server", label: s.server.title, back: s.server.back } as const;
  return (
    <div className="min-w-0">
      <PageHeader title={title} back={<Link to={parent.to} aria-label={parent.back}><IconChevronLeft aria-hidden="true" />{parent.label}</Link>} />
      <div className="flex min-w-0 flex-col gap-2.5">{children}</div>
    </div>
  );
}
