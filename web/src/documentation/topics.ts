// Child paths come before their sections. Match a path boundary so a new
// sibling can't silently inherit an unrelated article.
export const helpRoutes = [
  ["/people/", "user"],
  ["/pulse/diag/", "diagnostics"],
  ["/server/config", "config"],
  ["/server/files", "files"],
  ["/server/node-settings", "node-settings"],
  ["/server/platform", "platform"],
  ["/server/security", "security"],
  ["/server/updates", "node-updates"],
  ["/panel/updates", "panel-updates"],
  ["/overview", "overview"],
  ["/people", "people"],
  ["/pulse", "pulse"],
  ["/geography", "geography"],
  ["/journal", "journal"],
  ["/web", "web"],
  ["/server", "server"],
  ["/panel", "panel"],
  ["/nodes", "nodes"],
  ["/clusters", "clusters"],
  ["/billing", "billing"],
] as const;

export type HelpTopic = typeof helpRoutes[number][1];
export type HelpArticle = readonly [title: string, purpose: string, procedure: string, check: string];
export type HelpArticles = Record<HelpTopic, HelpArticle>;

export const helpSections: readonly (readonly HelpTopic[])[] = [
  ["overview", "people", "user", "pulse", "diagnostics", "geography", "journal", "web"],
  ["server", "platform", "config", "files", "node-settings", "security", "node-updates", "panel", "panel-updates"],
  ["nodes", "clusters", "billing"],
];

export function helpTopicForPath(pathname: string): HelpTopic | undefined {
  const path = pathname.replace(/\/+$/, "");
  return helpRoutes.find(([route]) =>
    route.endsWith("/") ? path.startsWith(route) : path === route || path.startsWith(route + "/"),
  )?.[1];
}

export function isHelpTopic(topic: string): topic is HelpTopic {
  return helpRoutes.some(([, id]) => id === topic);
}
