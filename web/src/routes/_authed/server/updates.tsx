import { createFileRoute } from "@tanstack/react-router";
import { UpdatesPage } from "../../../server/updates/UpdatesPage";

import { NodeUpgradePage } from "../../../server/updates/NodeUpgradePage";
import { documentNodeId } from "../../../nodes/scope";

export const Route = createFileRoute("/_authed/server/updates")({
  component: documentNodeId === "default" ? UpdatesPage : NodeUpgradePage,
});
