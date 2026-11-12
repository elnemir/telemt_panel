import { Link } from "@tanstack/react-router";
import { useLocale, useStrings } from "../i18n";
import { cn } from "../lib/cn";
import { Card } from "../ui/Card";
import { PageHeader } from "../ui/PageHeader";
import { IconChevronLeft, IconChevronRight } from "../ui/icons";
import { helpArticlesEn } from "../i18n/documentation.en";
import { helpArticlesRu } from "../i18n/documentation.ru";
import { helpSections, type HelpTopic } from "./topics";

export function DocumentationPage({ topic }: { topic?: HelpTopic }) {
  const s = useStrings();
  const t = s.documentation;
  const articles = useLocale() === "ru" ? helpArticlesRu : helpArticlesEn;
  const article = topic ? articles[topic] : undefined;

  return (
    <div className="min-w-0">
      <PageHeader title={article?.[0] ?? s.nav.documentation}
        description={article?.[1] ?? t.description}
        back={article && <Link to="/documentation"><IconChevronLeft aria-hidden="true" />{s.nav.documentation}</Link>} />
      <div className={cn("grid items-start gap-4",
        article ? "lg:grid-cols-[minmax(0,1fr)_18rem]" : "md:grid-cols-3")}>
        {article && <Card className="space-y-6">
          <section className="space-y-2">
            <h2 className="text-lg font-semibold">{t.procedure}</h2>
            <p className="leading-relaxed text-text-muted">{article[2]}</p>
          </section>
          <section className="space-y-2">
            <h2 className="text-lg font-semibold">{t.check}</h2>
            <p className="leading-relaxed text-text-muted">{article[3]}</p>
          </section>
        </Card>}
        <nav aria-label={t.contents} className={cn("contents", article && "lg:block lg:space-y-3")}>
          {helpSections.map((section, index) => (
            <Card key={section[0]} className="min-w-0">
              <h2 className="px-2 pb-2 text-sm font-bold text-text">{t.groups[index]}</h2>
              <ul>
                {section.map(id => (
                  <li key={id}>
                    <Link to="/documentation/$topic" params={{ topic: id }}
                      aria-current={topic === id ? "page" : undefined}
                      className={cn("tap-target flex min-w-0 items-center justify-between gap-2 rounded-lg px-2 text-sm hover:bg-surface-2",
                        topic === id ? "bg-accent/14 font-semibold text-accent" : "text-text-muted")}>
                      <span>{articles[id][0]}</span><IconChevronRight aria-hidden="true" className="h-4 w-4 shrink-0" />
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          ))}
        </nav>
      </div>
    </div>
  );
}
