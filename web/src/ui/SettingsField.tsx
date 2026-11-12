import { useId, type ReactNode } from "react";

interface FieldAttributes {
  id: string;
  "aria-describedby": string;
  "aria-invalid": boolean;
}

export function SettingsField({ label, hint, limits, error, children }: {
  label: string;
  hint?: string;
  limits?: string;
  error?: string;
  children: (attrs: FieldAttributes) => ReactNode;
}) {
  const id = useId();
  const descriptions = [hint && id + "-hint", limits && id + "-limits", error && id + "-error"].filter(Boolean);
  return (
    <div className="grid min-w-0 gap-1.5">
      <label htmlFor={id} className="grid gap-1.5 text-sm font-semibold text-text">
        {label}
        {children({ id, "aria-describedby": descriptions.join(" "), "aria-invalid": !!error })}
      </label>
      {hint && <p id={id + "-hint"} className="text-xs text-text-muted">{hint}</p>}
      {limits && <p id={id + "-limits"} className="text-xs leading-relaxed text-text-muted">{limits}</p>}
      {error && <p id={id + "-error"} role="alert" className="text-sm text-error">{error}</p>}
    </div>
  );
}
