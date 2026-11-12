import { createFileRoute } from "@tanstack/react-router";
import { NodeSettingsPage } from "../../../server/settings/NodeSettingsPage";

export const Route = createFileRoute("/_authed/server/node-settings")({
  component: NodeSettingsPage,
});
