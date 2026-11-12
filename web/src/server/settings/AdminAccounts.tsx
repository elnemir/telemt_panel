import { useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEndSession } from "../../auth/useLogout";
import { errorMessage, useStrings } from "../../i18n";
import { listAdminAccountsOptions, listAdminAccountsQueryKey } from "../../lib/api/generated/@tanstack/react-query.gen";
import { changeAdminPassword, createAdminAccount, deleteAdminAccount } from "../../lib/api/generated/sdk.gen";
import { apiErrorCode } from "../../people/apiError";
import { Button } from "../../ui/Button";
import { ErrorState } from "../../ui/ErrorState";
import { Input } from "../../ui/Input";
import { Sheet } from "../../ui/Sheet";
import { Skeleton } from "../../ui/Skeleton";
import { SettingsField } from "../../ui/SettingsField";
import { pushToast } from "../../ui/Toast";

type AccountAction = { kind: "add" | "password" } | { kind: "delete"; username: string };
const usernamePattern = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;

export function AdminAccounts() {
  const s = useStrings();
  const labels = s.server.settings.admins;
  const client = useQueryClient();
  const accounts = useQuery(listAdminAccountsOptions());
  const endSession = useEndSession();
  const [action, setAction] = useState<AccountAction | null>(null);

  async function complete(kind: AccountAction["kind"]) {
    setAction(null);
    if (kind === "password") {
      await endSession();
      pushToast(labels.passwordChanged, "ok");
      return;
    }
    void client.invalidateQueries({ queryKey: listAdminAccountsQueryKey() });
    pushToast(kind === "add" ? labels.created : labels.removed, "ok");
  }

  return (
    <section className="overflow-hidden rounded-xl bg-surface" aria-labelledby="admin-accounts-title">
      <header className="border-b border-border px-4 py-3.5">
        <h2 id="admin-accounts-title" className="text-[16px] font-bold text-text">{labels.title}</h2>
        <p className="mt-1 text-xs text-text-muted">{labels.description}</p>
      </header>
      {accounts.isPending && <div className="p-4"><Skeleton className="h-11 w-full" /></div>}
      {accounts.isError && <div className="p-4">
        <ErrorState message={errorMessage(s, apiErrorCode(accounts.error) ?? "network")} onRetry={() => accounts.refetch()} />
      </div>}
      {accounts.data && <>
        <ul className="divide-y divide-border">
          {accounts.data.accounts.map((account) => (
            <li key={account.username} className="flex min-h-14 flex-wrap items-center gap-2 px-4 py-2.5">
              <strong className="min-w-0 flex-1 break-all text-sm text-text">{account.username}</strong>
              {account.owner && <span className="rounded-full bg-accent/10 px-2 py-1 text-xs text-accent">{labels.owner}</span>}
              {account.current && <span className="rounded-full bg-ok/10 px-2 py-1 text-xs text-ok">{labels.current}</span>}
              {accounts.data?.writable && !account.owner && !account.current &&
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  aria-label={labels.removeAccount.replace("{username}", account.username)}
                  onClick={() => setAction({ kind: "delete", username: account.username })}
                >
                  {labels.remove}
                </Button>}
            </li>
          ))}
        </ul>
        {accounts.data.writable ? (
          <div className="flex flex-wrap gap-2 border-t border-border p-4">
            <Button type="button" size="sm" onClick={() => setAction({ kind: "add" })}>{labels.add}</Button>
            <Button type="button" variant="secondary" size="sm" onClick={() => setAction({ kind: "password" })}>
              {labels.changePassword}
            </Button>
          </div>
        ) : <p className="border-t border-border p-4 text-xs text-text-muted">{labels.readOnly}</p>}
      </>}
      {action && <AccountForm action={action} onClose={() => setAction(null)} onComplete={complete} />}
    </section>
  );
}

function AccountForm({ action, onClose, onComplete }: {
  action: AccountAction;
  onClose: () => void;
  onComplete: (kind: AccountAction["kind"]) => Promise<void>;
}) {
  const s = useStrings();
  const labels = s.server.settings.admins;
  const firstInput = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<{ field?: string; text: string }>();
  const errorFor = (field: string) => error?.field === field ? error.text : undefined;
  const [pending, setPending] = useState(false);
  const title = action.kind === "add" ? labels.add : action.kind === "delete" ? labels.remove : labels.changePassword;
  const note = action.kind === "add" ? labels.addNote : action.kind === "delete"
    ? labels.removeNote.replace("{username}", action.username) : labels.changeNote;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    const form = new FormData(event.currentTarget);
    const username = String(form.get("username") ?? "");
    const currentPassword = String(form.get("current_password") ?? "");
    const newPassword = String(form.get("new_password") ?? "");
    if (action.kind === "add" && !usernamePattern.test(username)) {
      setError({ field: "username", text: errorMessage(s, "admin_username_invalid") });
      return;
    }
    if (action.kind !== "delete") {
      if ([...newPassword].length < 12 || new TextEncoder().encode(newPassword).length > 72) {
        setError({ field: "new_password", text: labels.passwordHint });
        return;
      }
      if (newPassword !== form.get("repeat_password")) {
        setError({ field: "repeat_password", text: labels.passwordMismatch });
        return;
      }
    }

    setError(undefined);
    setPending(true);
    try {
      if (action.kind === "add") {
        await createAdminAccount({
          body: { username, current_password: currentPassword, new_password: newPassword }, throwOnError: true,
        });
      } else if (action.kind === "delete") {
        await deleteAdminAccount({
          path: { username: action.username }, body: { current_password: currentPassword }, throwOnError: true,
        });
      } else {
        await changeAdminPassword({
          body: { current_password: currentPassword, new_password: newPassword }, throwOnError: true,
        });
      }
      await onComplete(action.kind);
    } catch (err) {
      const code = apiErrorCode(err) ?? "network";
      const field = code === "admin_password_incorrect" ? "current_password" :
        code === "admin_exists" || code === "admin_username_invalid" ? "username" :
        code === "admin_password_policy" ? "new_password" : undefined;
      setError({ field, text: errorMessage(s, code) });
      setPending(false);
    }
  }

  return (
    <Sheet open onClose={() => { if (!pending) onClose(); }} title={title} subtitle={note} initialFocusRef={firstInput}>
      <form className="grid gap-4" onSubmit={submit} onChange={() => setError(undefined)}>
        <fieldset className="grid min-w-0 gap-4" disabled={pending}>
          {action.kind === "add" && <SettingsField label={labels.username} limits={labels.usernameHint} error={errorFor("username")}>
            {(attrs) => <Input {...attrs} ref={firstInput} name="username" required maxLength={64} autoComplete="off" autoCapitalize="none" spellCheck={false} />}
          </SettingsField>}
          <SettingsField label={labels.currentPassword} hint={labels.currentPasswordHint} error={errorFor("current_password")}>
            {(attrs) => <Input {...attrs} ref={action.kind === "add" ? undefined : firstInput} name="current_password" type="password" autoComplete="current-password" required />}
          </SettingsField>
          {action.kind !== "delete" && <>
            <SettingsField label={labels.newPassword} limits={labels.passwordHint} error={errorFor("new_password")}>
              {(attrs) => <Input {...attrs} name="new_password" type="password" autoComplete="new-password" required />}
            </SettingsField>
            <SettingsField label={labels.repeatPassword} hint={labels.repeatPasswordHint} error={errorFor("repeat_password")}>
              {(attrs) => <Input {...attrs} name="repeat_password" type="password" autoComplete="new-password" required />}
            </SettingsField>
          </>}
        </fieldset>
        {error && !error.field && <p role="alert" className="text-sm text-error">{error.text}</p>}
        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" variant="secondary" disabled={pending} onClick={onClose}>{s.common.cancel}</Button>
          <Button type="submit" variant={action.kind === "delete" ? "danger" : "primary"} disabled={pending}>
            {pending ? labels.saving : title}
          </Button>
        </div>
      </form>
    </Sheet>
  );
}
