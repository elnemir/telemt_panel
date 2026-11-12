import {Link} from "@tanstack/react-router";
import {useStrings} from "../i18n";
import {useAuthDisabled} from "./useAuthDisabled";

export function AuthDisabledNotice() {
  const disabled=useAuthDisabled(),s=useStrings();
  if(!disabled)return null;
  return <Link to="/panel" data-testid="auth-disabled-notice" className="m-2 block shrink-0 rounded-lg border border-warn/25 bg-warn/10 px-3 py-2 text-meta text-warn">{s.auth.disabledTitle}</Link>;
}

export function AuthDisabledDetails() {
  const s=useStrings();
  return <section data-testid="auth-disabled-details" className="rounded-xl border border-warn/25 bg-warn/5 p-4"><h2 className="text-lg font-bold text-warn">{s.auth.disabledTitle}</h2><p className="mt-2 text-sm leading-relaxed text-text-muted">{s.auth.disabledNote}</p><p className="mt-3 text-sm text-text-muted">{s.auth.disabledRestore}</p></section>;
}
