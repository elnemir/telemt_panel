import { useStrings } from "../i18n";
import { copyText } from "../lib/copyText";
import { Button } from "../ui/Button";
import { pushToast } from "../ui/Toast";

const telemtExample = `[server.api]
enabled = true
listen = "10.42.0.20:9091"
whitelist = ["10.42.0.10/32"]
auth_header = "Bearer REPLACE_WITH_RANDOM_TOKEN"
read_only = false`;

const panelEnvironmentExample = 'TELEMT_NODE_A_AUTH="Bearer REPLACE_WITH_RANDOM_TOKEN"';
export const nodeExampleURL = "http://10.42.0.20:9091";

export function NodeSetupGuide() {
  const { nodes: { setup }, common } = useStrings();

  async function copyExample() {
    const result = await copyText(telemtExample);
    if (result === "failed") pushToast(common.copyManually, "error");
    else pushToast(common.copied, "ok");
  }

  return (
    <details className="min-w-0 rounded-xl border border-border bg-surface-2 px-3">
      <summary className="min-h-11 cursor-pointer py-3 text-sm font-semibold text-text">{setup.title}</summary>
      <ol className="list-decimal space-y-4 pb-3 ps-5 text-sm text-text-muted">
        <li>
          <h3 className="font-semibold text-text">{setup.telemt}</h3>
          <p className="mt-1">{setup.telemtNote}</p>
          <pre className="my-2 overflow-x-auto rounded-lg bg-bg p-3 text-xs text-text"><code>{telemtExample}</code></pre>
          <Button type="button" size="sm" variant="secondary" onClick={() => void copyExample()}>{setup.copy}</Button>
          <p className="mt-2">{setup.networkNote}</p>
          <p className="mt-1">{setup.restartTelemt}</p>
        </li>
        <li>
          <h3 className="font-semibold text-text">{setup.panel}</h3>
          <p className="mt-1">{setup.panelNote}</p>
          <pre className="mt-2 overflow-x-auto rounded-lg bg-bg p-3 text-xs text-text"><code>{panelEnvironmentExample}</code></pre>
        </li>
        <li>
          <h3 className="font-semibold text-text">{setup.registry}</h3>
          <p className="mt-1">{setup.registryNote}</p>
          <p className="mt-1">{setup.healthNote}</p>
        </li>
      </ol>
    </details>
  );
}
