import { createFileRoute } from "@tanstack/react-router";
import { ConfigFilesPage } from "../../../server/config/ConfigFilesPage";
export const Route = createFileRoute("/_authed/server/files")({ component: ConfigFilesPage });
