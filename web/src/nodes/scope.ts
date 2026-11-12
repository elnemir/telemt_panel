// Immutable for the lifetime of this document. Switching nodes navigates to a
// new document; an old async closure can never acquire the next node's target.
export function nodeFromSearch(search: string): string {
  return new URLSearchParams(search).get("node") ?? "default";
}
export const documentNodeId = nodeFromSearch(window.location.search);

const roots = ["users", "telemt", "history", "traffic", "geography", "audit", "events", "snapshot", "host", "updates", "logs"];
const settings = ["storage", "quota-schedule", "geography", "links"];
export function nodeAPIPath(path: string, nodeId = documentNodeId): string {
  if (nodeId === "default" || !path.startsWith("/api/")) return path;
  const parts = path.slice(5).split(/[/?]/);
  const owned = roots.includes(parts[0]!) || (parts[0] === "settings" && settings.includes(parts[1]!));
  return owned ? `/api/nodes/${encodeURIComponent(nodeId)}/manage/${path.slice(5)}` : path;
}

export function nodeNavigationURL(href: string, id: string): string {
  const url = new URL(href);
  url.searchParams.set("node", id);
  return url.href;
}

// Keep the document scope on links rendered outside TanStack Router.
export function withDocumentNode(path: string): string {
  if (documentNodeId === "default") return path;
  const url = new URL(path, window.location.origin);
  url.searchParams.set("node", documentNodeId);
  return url.pathname + url.search + url.hash;
}

// Preserve existing default-node recovery keys; other nodes never read them.
export function nodeStorageKey(key: string, basePath = "", nodeId = documentNodeId): string {
  return nodeId === "default" ? key : `${key}:node:${encodeURIComponent(basePath)}:${nodeId}`;
}
