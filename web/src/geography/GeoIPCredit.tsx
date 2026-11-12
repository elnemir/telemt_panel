import { useStrings } from "../i18n";
import type { GeoIpStatus } from "../lib/api/generated/types.gen";

const providers = {
  maxmind: "https://www.maxmind.com/",
  dbip: "https://db-ip.com/",
  ipinfo: "https://ipinfo.io/",
};

export function GeoIPCredit({ source, className = "" }: { source?: GeoIpStatus["active_source"]; className?: string }) {
  const { geoip } = useStrings();
  if (source !== "maxmind" && source !== "dbip" && source !== "ipinfo") return null;
  return <a className={`underline underline-offset-2 ${className}`} href={providers[source]} target="_blank" rel="noreferrer">{geoip.credit[source]}</a>;
}
