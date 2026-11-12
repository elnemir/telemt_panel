import { useStrings } from "../../i18n";
import { StatePill } from "../../ui/StatePill";
import { IconRefresh, IconWarning } from "../../ui/icons";
import { HAPPY_PATH, updatePhaseStep, type UpdatePhase } from "./updatePhase.helpers";

export function UpdateStepper({
  phase,
  detail,
  streamFallback = false,
}: {
  phase: UpdatePhase;
  detail?: string;
  streamFallback?: boolean;
}) {
  const s = useStrings();

  if (phase === "rolling_back" || phase === "rolled_back") {
    return (
      <div className="flex items-start gap-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-warn/10 text-warn"><IconRefresh aria-hidden="true" /></span>
        <div className="min-w-0">
          <StatePill state="warn">{s.server.updates.phases[phase]}</StatePill>
          <p className="mt-1.5 text-meta font-semibold text-text">
            {phase === "rolled_back"
              ? s.server.updates.rollbackCompleted
              : s.server.updates.rollbackInProgress}
          </p>
          {detail && <p className="mt-1 break-words font-mono text-micro leading-relaxed text-text-muted">{detail}</p>}
        </div>
      </div>
    );
  }

  if (phase === "failed") {
    return (
      <div className="flex items-start gap-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-error/10 text-error"><IconWarning aria-hidden="true" /></span>
        <div className="min-w-0">
          <StatePill state="error">{s.server.updates.phases.failed}</StatePill>
          {detail && <p className="mt-1.5 break-words font-mono text-micro leading-relaxed text-text-muted">{detail}</p>}
        </div>
      </div>
    );
  }

  const { stepIndex, terminal } = updatePhaseStep(phase);
  const t = s.server.updates;

  return (
    <div className="workspace-card">
      <p className="mb-3 font-semibold" role="status">{t.phases[phase] ?? t.progressWaiting}</p>
      <ol className="divide-y divide-border">
        {HAPPY_PATH.map((step, index) => {
          const active = !terminal && index === stepIndex;
          const done = terminal || index < stepIndex;
          return <li key={step} aria-current={active ? "step" : undefined} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
            <span>{t.phases[step]}</span>
            <StatePill state={done ? "ok" : "muted"}>{done ? t.progressDone : active ? t.running : t.progressWaiting}</StatePill>
          </li>;
        })}
      </ol>

      {detail && <p className="mt-2 break-words font-mono text-micro leading-relaxed text-text-faint">{detail}</p>}
      {streamFallback && (
        <p className="mt-2 flex items-start gap-1.5 text-micro leading-relaxed text-text-faint">
          <span className="mt-[5px] h-1.5 w-1.5 shrink-0 rounded-full bg-warn" aria-hidden="true" />
          {s.server.updates.sseStale}
        </p>
      )}
    </div>
  );
}
