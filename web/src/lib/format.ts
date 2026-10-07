// The one place a timestamp, duration, count, size, or amount becomes text
// (work-frontend.md rule 9, NFR-7). Values are stored UTC and shown in the user's
// configured timezone; a component never calls toLocaleString itself.

export type FormatContext = { timeZone?: string; locale?: string };

function zone(ctx: FormatContext): string | undefined {
  if (!ctx.timeZone) return undefined;
  try {
    new Intl.DateTimeFormat("en", { timeZone: ctx.timeZone });
    return ctx.timeZone;
  } catch {
    // An unknown zone name in a profile must not take the page down.
    return undefined;
  }
}

function toDate(value: string | number | Date): Date {
  return value instanceof Date ? value : new Date(value);
}

/** "7 Oct 2026, 14:05" in the user's zone. */
export function formatDateTime(value: string | number | Date, ctx: FormatContext = {}): string {
  return new Intl.DateTimeFormat(ctx.locale ?? "en-GB", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: zone(ctx),
  }).format(toDate(value));
}

export function formatDate(value: string | number | Date, ctx: FormatContext = {}): string {
  return new Intl.DateTimeFormat(ctx.locale ?? "en-GB", {
    dateStyle: "medium",
    timeZone: zone(ctx),
  }).format(toDate(value));
}

/** "3 minutes ago", "in 2 hours". */
export function formatRelative(
  value: string | number | Date,
  ctx: FormatContext & { now?: Date } = {},
): string {
  const seconds = Math.round((toDate(value).getTime() - (ctx.now ?? new Date()).getTime()) / 1000);
  const rtf = new Intl.RelativeTimeFormat(ctx.locale ?? "en", { numeric: "auto" });
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return rtf.format(Math.trunc(seconds / size), unit);
  }
  return rtf.format(seconds, "second");
}

/** 2m 14s, 1h 03m, 850ms. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "–";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

export function formatNumber(value: number, ctx: FormatContext = {}): string {
  return new Intl.NumberFormat(ctx.locale ?? "en-GB").format(value);
}

export function formatPercent(fraction: number, ctx: FormatContext = {}): string {
  return new Intl.NumberFormat(ctx.locale ?? "en-GB", {
    style: "percent",
    maximumFractionDigits: 1,
  }).format(fraction);
}

/** AI spend is small and precise: show cents, and more digits below a cent. */
export function formatUSD(value: number, ctx: FormatContext = {}): string {
  return new Intl.NumberFormat(ctx.locale ?? "en-GB", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: Math.abs(value) > 0 && Math.abs(value) < 0.01 ? 4 : 2,
  }).format(value);
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "–";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? value : value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
}
