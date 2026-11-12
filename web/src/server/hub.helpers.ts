import type {
  HostInfo,
  TelemtConfig,
  UpdatesStatus,
} from "../lib/api/generated/types.gen";
import type { RuntimeGates, RuntimeUpstreamQualityData } from "../realtime/topics";

type UpdateTarget = UpdatesStatus["targets"][number];

export type ServerRouteMode = "fallback" | "me" | "direct" | "unknown";
export type ServerTransportMode = "tls" | "secure" | "classic" | "unknown";
export type EgressType = "direct" | "socks4" | "socks5" | "shadowsocks" | "unknown";
export interface EgressSummary {
  routes: Array<{ type: EgressType; count: number; healthy: number }>;
  scoped: number;
  total: number;
  healthy: number;
}

export interface ServerConfigSummary {
  transport: ServerTransportMode;
  masking: boolean | null;
  dcOverrides: number | null;
}

// Runtime mode comes from Telemt's route controller, not fallback permission.
export function runtimeRouteMode(gates: RuntimeGates | null | undefined): ServerRouteMode {
  if (!gates) return "unknown";
  if (gates.route_mode === "middle" && gates.reroute_active === false) return "me";
  if (gates.route_mode === "direct") {
    if (gates.reroute_active === true) return "fallback";
    if (gates.reroute_active === false) return "direct";
  }
  return "unknown";
}

// Live pool health does not identify the upstream chosen by an active socket.
// Keep scoped entries: this is the entire runtime pool, not a config projection.
export function summarizeEgress(quality: RuntimeUpstreamQualityData | null | undefined): EgressSummary | null {
  if (!quality?.enabled) return null;
  const rows = quality.upstreams ?? (quality.summary?.configured_total === 0 ? [] : null);
  if (!Array.isArray(rows)) return null;
  if (quality.summary && quality.summary.configured_total !== rows.length) return null;
  const counts: Record<EgressType, { count: number; healthy: number }> = {
    direct: { count: 0, healthy: 0 }, socks4: { count: 0, healthy: 0 },
    socks5: { count: 0, healthy: 0 }, shadowsocks: { count: 0, healthy: 0 }, unknown: { count: 0, healthy: 0 },
  };
  let healthy = 0;
  let scoped = 0;
  for (const row of rows) {
    if (!row || typeof row.healthy !== "boolean") return null;
    if (row.scopes) scoped++;
    const type = row.route_kind;
    const known = type === "direct" || type === "socks4" || type === "socks5" || type === "shadowsocks";
    const group = counts[known ? type : "unknown"];
    group.count++;
    if (row.healthy) { group.healthy++; healthy++; }
  }
  return {
    routes: (Object.keys(counts) as EgressType[]).filter((type) => counts[type].count > 0).map((type) => ({ type, ...counts[type] })),
    scoped, total: rows.length, healthy,
  };
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

export function summarizeServerConfig(
  config: TelemtConfig | undefined,
): ServerConfigSummary {
  if (!config) {
    return {
      transport: "unknown",
      masking: null,
      dcOverrides: null,
    };
  }

  const general = asRecord(config.sections["general"]);
  const censorship = asRecord(config.sections["censorship"]);
  const modes = asRecord(general?.["modes"]);
  const overrides = asRecord(config.sections["dc_overrides"]);

  let transport: ServerTransportMode = "unknown";
  if (modes?.["tls"] === true) transport = "tls";
  else if (modes?.["secure"] === true) transport = "secure";
  else if (modes?.["classic"] === true) transport = "classic";

  return {
    transport,
    masking:
      typeof censorship?.["mask"] === "boolean" ? censorship["mask"] : null,
    dcOverrides: overrides ? Object.keys(overrides).length : null,
  };
}

export function newestAvailableRelease(target: UpdateTarget | undefined) {
  return target?.releases
    .filter((release) => release.newer)
    .toSorted((a, b) => b.published_at.localeCompare(a.published_at))[0];
}

export function activeUpdateRun(targets: UpdatesStatus["targets"] | undefined) {
  return targets?.find((target) => {
    const phase = target.active_run?.phase;
    return phase && !["done", "rolled_back", "failed"].includes(phase);
  })?.active_run;
}

export function hostCapabilityCount(caps: HostInfo["caps"] | undefined, remoteNode = false) {
  const entries = caps ? Object.entries(caps).filter(([name]) =>
    remoteNode ? name !== "restart_panel" : name !== "config_file") : [];
  return { available: entries.filter(([, available]) => available).length, total: entries.length };
}

// Remote SSH upgrades run in the central panel. The agent's self_update
// describes its own local updater and is deliberately false.
export function remoteHostCapabilities(caps: HostInfo["caps"], updateAvailable: boolean): HostInfo["caps"] {
  return { ...caps, self_update: updateAvailable };
}

export function needsFullNodeManagement(caps: HostInfo["caps"] | undefined) {
  return Boolean(caps && !(caps.config_file && caps.start_telemt && caps.stop_telemt &&
    caps.restart_telemt && caps.log_tail && caps.log_stream));
}
