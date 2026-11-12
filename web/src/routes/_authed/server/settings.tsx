import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_authed/server/settings")({
  beforeLoad: ({ location }) => {
    const { hash, search } = location;
    const to = hash === "storage" || hash === "geography" ? "/server/node-settings" : "/panel";
    throw redirect({ to, hash, search, replace: true });
  },
});
