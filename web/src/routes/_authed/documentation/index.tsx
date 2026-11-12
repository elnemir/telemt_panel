import { createFileRoute } from "@tanstack/react-router";
import { DocumentationPage } from "../../../documentation/DocumentationPage";

export const Route = createFileRoute("/_authed/documentation/")({
  component: DocumentationPage,
});
