const unsignedMax: Readonly<Record<string, number>> = {
  u8: 255,
  u16: 65_535,
  u32: 4_294_967_295,
  u64: Number.MAX_SAFE_INTEGER,
  usize: Number.MAX_SAFE_INTEGER,
};

export function configIntegerMax(dataType: string): number {
  return unsignedMax[dataType] ?? Number.MAX_SAFE_INTEGER;
}

export function parseConfigNumber(text: string, integer: boolean, dataType: string): number | undefined {
  if (integer) {
    if (!/^\d+$/.test(text)) return undefined;
    const number = Number(text);
    return Number.isSafeInteger(number) && number <= configIntegerMax(dataType) ? number : undefined;
  }
  if (!/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(text)) return undefined;
  const number = Number(text);
  return Number.isFinite(dataType === "f32" ? Math.fround(number) : number) ? number : undefined;
}

export function isConfigIPAddress(text: string): boolean {
  if (text.includes(":")) {
    if (!/^[a-f0-9:.]+$/i.test(text)) return false;
    try {
      return new URL(`http://[${text}]/`).hostname.startsWith("[");
    } catch {
      return false;
    }
  }
  const octets = text.split(".");
  return octets.length === 4 && octets.every((part) => /^(?:0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255);
}

export function isConfigSocketAddress(text: string, allowHost = false): boolean {
  const ipv6 = /^\[([^\]]+)\]:(\d+)$/.exec(text);
  const match = ipv6 ?? /^([^:]+):(\d+)$/.exec(text);
  if (!match || Number(match[2]) > 65_535 || (allowHost && Number(match[2]) === 0)) return false;
  return isConfigIPAddress(match[1]!) || (allowHost && !ipv6 && isConfigHostname(match[1]!));
}

export function isConfigHostname(text: string): boolean {
  const host = text.replace(/\.$/, "");
  return host.length <= 253 && host.split(".").every((part) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(part));
}

export function isConfigNetwork(text: string): boolean {
  const [address, bits, extra] = text.split("/");
  return !!address && !!bits && extra === undefined && /^\d+$/.test(bits) &&
    isConfigIPAddress(address) && Number(bits) <= (address.includes(":") ? 128 : 32);
}
