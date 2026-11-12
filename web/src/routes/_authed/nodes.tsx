import { createFileRoute } from "@tanstack/react-router";
import { NodesPage } from "../../nodes/NodesPage";

export const Route = createFileRoute("/_authed/nodes")({ component: NodesPage });
