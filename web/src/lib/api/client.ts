import { nodeAPIPath } from "../../nodes/scope";
import { client } from "./generated/client.gen";
import { getMeQueryKey } from "./generated/@tanstack/react-query.gen";
import { getBasePath, withBasePath } from "../base-path";
import { getRouterInstance } from "../router-instance";
import { queryClient } from "../query-client";
import { removeGeography } from "../../geography/queries";

// Wires the hey-api generated client to this deployment: same-origin
// requests carry the session cookie (`credentials: "include"` — the panel
// authenticates via an HttpOnly cookie, see 05-auth.md), and every request
// is prefixed with the base_path injected by internal/webui so this works
// unmodified whether the panel is served at "/" or under a reverse-proxy
// sub-path like "/panel".
client.setConfig({
  // "" is valid (root deployment) — the generated client concatenates
  // baseUrl + path directly (see generated/client/utils.gen.ts), so an
  // empty prefix and no trailing slash is exactly right, matching
  // config.Config.BasePath's own normalization.
  baseUrl: getBasePath(),
  credentials: "include",
});

// Capture the immutable document scope before the fetch, never a mutable
// selection. Explicit registry/scoped URLs and global admin APIs are unchanged.
client.interceptors.request.use(async (request) => {
  const url = new URL(request.url);
  const base = getBasePath();
  if (url.origin !== window.location.origin || !url.pathname.startsWith(base + "/api/")) return request;
  const path = nodeAPIPath(url.pathname.slice(base.length));
  if (base + path === url.pathname) return request;
  url.pathname = base + path;
  // Request.body is a ReadableStream: passing the Request as RequestInit
  // would turn ordinary JSON uploads into streaming uploads (HTTP/2-only in
  // Chromium). Materialize the existing body while preserving fetch options.
  return new Request(url, {
    method: request.method, headers: request.headers,
    body: request.body ? await request.clone().blob() : undefined,
    credentials: request.credentials, mode: request.mode, cache: request.cache,
    redirect: request.redirect, referrer: request.referrer,
    referrerPolicy: request.referrerPolicy, integrity: request.integrity,
    keepalive: request.keepalive, signal: request.signal,
  });
});

// Global 401 handling (Task 4 deliverable A): any authenticated request
// that comes back 401 mid-session (RequireSession's session_expired —
// 05-auth.md) drops the app to /login exactly once. /api/auth/login's own
// 401 (bad credentials) and /api/auth/me's own 401 are excluded — the login
// form and the route guards (auth/guards.ts) already handle those
// specifically and more precisely. `redirecting` guards against a burst of
// concurrent requests all failing at once triggering repeat navigations —
// it clears on the next non-401 response, so a fresh session (re-login)
// re-arms it.
const EXCLUDED_PATHS = [
  withBasePath("/api/auth/login"),
  withBasePath("/api/auth/webauthn/login/finish"),
  withBasePath("/api/auth/me"),
];
let redirecting = false;

client.interceptors.response.use((response, request) => {
  if (response.status !== 401) {
    if (response.ok) redirecting = false;
    return response;
  }

  const requestUrl = new URL(request.url);
  // Customer authentication has its own cookie, cache and login screen.
  if(requestUrl.pathname.startsWith(getBasePath()+"/api/customer/")) return response;
  removeGeography(queryClient);
  if (EXCLUDED_PATHS.some((p) => requestUrl.pathname.endsWith(p))) {
    return response;
  }
  if (window.location.pathname === withBasePath("/login")) {
    return response;
  }

  if (!redirecting) {
    redirecting = true;
    queryClient.removeQueries({ queryKey: getMeQueryKey() });
    const router = getRouterInstance();
    if (router) {
      const redirectTarget = window.location.pathname + window.location.search + window.location.hash;
      void router.navigate({ to: "/login", search: { redirect: redirectTarget } });
    }
  }
  return response;
});

export { client };
