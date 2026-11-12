import { describe, expect, it, vi } from "vitest";
import { nodeFromSearch, nodeAPIPath, nodeNavigationURL, nodeStorageKey } from "./scope";

describe("immutable document node routing", () => {
  it("defaults only an absent choice, never an invalid or removed ID", () => {
    expect(nodeFromSearch("")).toBe("default");
    expect(nodeFromSearch("?node=missing")).toBe("missing");
    expect(nodeFromSearch("?node=")).toBe("");
  });
  it("routes every node-owned family and preserves global admin boundaries", () => {
    for (const path of ["/api/users/alice", "/api/telemt/config/toml", "/api/events?topics=users", "/api/snapshot", "/api/history", "/api/traffic/summary", "/api/geography", "/api/audit", "/api/settings/storage", "/api/settings/quota-schedule", "/api/host", "/api/updates/telemt/apply", "/api/events/logs"]) {
      expect(nodeAPIPath(path, "n_a")).toBe("/api/nodes/n_a/manage/" + path.slice(5));
      expect(nodeAPIPath(path, "default")).toBe(path);
    }
    for (const path of ["/api/auth/me", "/api/nodes", "/api/nodes/n_b/config", "/api/settings/branding", "/api/settings/tls", "/api/settings/geoip", "/api/health"]) {
      expect(nodeAPIPath(path, "n_a")).toBe(path);
    }
  });
  it("keeps old requests bound to their captured scope", async () => {
    const previous = window.location.href;
    try {
      window.history.replaceState({}, "", "?node=n_a");
      vi.resetModules();
      const oldDocument = await import("./scope");
      window.history.replaceState({}, "", "?node=n_b");
      expect(oldDocument.nodeAPIPath("/api/users/alice")).toBe("/api/nodes/n_a/manage/users/alice");
      vi.resetModules();
      const nextDocument = await import("./scope");
      expect(nextDocument.nodeAPIPath("/api/users/alice")).toBe("/api/nodes/n_b/manage/users/alice");
      expect(oldDocument.nodeAPIPath("/api/users/alice")).toBe("/api/nodes/n_a/manage/users/alice");
    } finally {
      window.history.replaceState({}, "", previous);
      vi.resetModules();
    }
  });
  it("creates a full navigation URL retaining base path and other search fields", () => {
    expect(nodeNavigationURL("https://panel.test/panel/people?filter=active#details", "n_b")).toBe("https://panel.test/panel/people?filter=active&node=n_b#details");
  });
});

it("isolates persisted operation and pending-config recovery across nodes", () => {
  const key="telemt-panel:pending-config:v1";
  expect(nodeStorageKey(key,"/panel","default")).toBe(key);
  expect(nodeStorageKey(key,"/panel","n_a")).not.toBe(nodeStorageKey(key,"/panel","n_b"));
  expect(nodeStorageKey(key,"/one","n_a")).not.toBe(nodeStorageKey(key,"/two","n_a"));
});
