import type { ConfigTree } from "../../lib/api/generated/types.gen";
import { Button } from "../../ui/Button";
import { useStrings } from "../../i18n";

export type ConfigPath = Array<string | number>;
export type FieldChoice = { path: ConfigPath; kind: ConfigTree["kind"]; value: string; add?: boolean; array?: boolean };

export function ConfigTreeEditor({ tree, path = [], name = "", disabled, choose, remove }: {
  tree: ConfigTree; path?: ConfigPath; name?: string; disabled: boolean;
  choose: (field: FieldChoice) => void; remove: (path: ConfigPath) => void;
}) {
  const t = useStrings().configFiles;
  const container = tree.kind === "table" || tree.kind === "array";
  if (!container) return <div className="flex min-w-0 flex-wrap items-center gap-2 border-t border-border py-2">
    <span className="min-w-0 flex-1 break-all text-sm"><strong>{name}</strong><span className="ml-2 text-xs text-text-muted">{t.types[tree.kind]}</span><code className="mt-1 block max-h-24 overflow-auto whitespace-pre-wrap">{tree.value}</code></span>
    <Button size="sm" variant="secondary" disabled={disabled} onClick={() => choose({path,kind:tree.kind,value:tree.value})}>{t.edit}</Button>
    <Button size="sm" variant="secondary" disabled={disabled} onClick={() => remove(path)}>{t.remove}</Button>
  </div>;
  return <details open={path.length === 0} className="min-w-0 rounded-lg border border-border p-3">
    <summary className="cursor-pointer break-all font-medium">{name || t.sections} <span className="text-xs text-text-muted">{tree.children.length}</span></summary>
    <div className="mt-2 flex min-w-0 flex-col gap-2">
      {tree.children.map(child => <ConfigTreeEditor key={child.key} tree={child.node} name={child.key} path={[...path,tree.kind === "array" ? Number(child.key) : child.key]} disabled={disabled} choose={choose} remove={remove} />)}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" disabled={disabled} onClick={() => choose({path:tree.kind === "array" ? [...path,tree.children.length] : path,kind:"string",value:"",add:true,array:tree.kind === "array"})}>{t.add}</Button>
        {path.length > 0 && <Button size="sm" variant="secondary" disabled={disabled} onClick={() => remove(path)}>{t.remove}</Button>}
      </div>
    </div>
  </details>;
}
