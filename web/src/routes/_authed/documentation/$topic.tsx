import { createFileRoute, notFound } from "@tanstack/react-router";
import { DocumentationPage } from "../../../documentation/DocumentationPage";
import { isHelpTopic } from "../../../documentation/topics";

export const Route = createFileRoute("/_authed/documentation/$topic")({
  component: Article,
});

function Article() {
  const { topic } = Route.useParams();
  if (!isHelpTopic(topic)) throw notFound();
  return <DocumentationPage topic={topic} />;
}
