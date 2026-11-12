import type { GeoIpConfig } from "../../lib/api/generated/types.gen";

export const geoIPKinds = ["country", "asn", "city"] as const;
export const geoIPSources = ["maxmind", "dbip", "ipinfo", "urls", "files"] as const;

export function usesGeoIPKey(source: GeoIpConfig["source"]): boolean {
  return source === "maxmind" || source === "ipinfo";
}

export function defaultGeoIPConfig(): GeoIpConfig {
  return {
    enabled: false, source: "urls", schedule: "weekly",
    country: { enabled: true, location: "" },
    asn: { enabled: true, location: "" },
    city: { enabled: false, location: "" },
  };
}

export function switchGeoIPSource(config: GeoIpConfig, source: GeoIpConfig["source"]): GeoIpConfig {
  if (source === config.source) return config;
  const next = {
    ...config, source, country: { ...config.country, location: "" }, asn: { ...config.asn, location: "" },
    city: { enabled: source !== "ipinfo" && config.city.enabled, location: "" },
  };
  delete next.account_id;
  if (source === "ipinfo" && !next.country.enabled && !next.asn.enabled) next.country.enabled = true;
  return next;
}

export function normalizeGeoIPConfig(config: GeoIpConfig): GeoIpConfig {
  return {
    ...config,
    country: { ...config.country, location: config.country.location.trim() },
    asn: { ...config.asn, location: config.asn.location.trim() },
    city: { ...config.city, location: config.city.location.trim() },
  };
}

export function sameGeoIPConfig(a: GeoIpConfig, b: GeoIpConfig): boolean {
  return a.enabled === b.enabled && a.source === b.source && a.schedule === b.schedule && (a.account_id ?? "") === (b.account_id ?? "")
    && geoIPKinds.every(kind => a[kind].enabled === b[kind].enabled && a[kind].location === b[kind].location);
}

export function geoIPConfigError(config: GeoIpConfig, key = "", hasSavedKey = false): "dataset" | "url" | "path" | "account" | "accessKey" | null {
  if (!config.enabled) return null;
  const selected = geoIPKinds.filter(kind => config[kind].enabled);
  if (!selected.length) return "dataset";
  if (config.source === "maxmind") {
    const id = config.account_id ?? "";
    if (!/^[0-9]{1,20}$/.test(id) || BigInt(id) === 0n || BigInt(id) > 18446744073709551615n) return "account";
  }
  if (usesGeoIPKey(config.source) && (key ? !/^[!-~]{1,512}$/.test(key) : !hasSavedKey)) return "accessKey";
  for (const kind of selected) {
    const location = config[kind].location.trim();
    if (config.source === "files" && (!location.startsWith("/") || location.includes("\0"))) return "path";
    if (config.source === "urls") {
      try {
        const url = new URL(location);
        if (url.protocol !== "https:" || !url.hostname || url.username || url.password || url.hash) return "url";
      } catch { return "url"; }
    }
  }
  return null;
}
