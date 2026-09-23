// Day-range defaults and preset matching (pure; node --test).
//
// The range lives in the URL (from/to). With NO range in the URL the
// dashboard opens on the last 7 days in the selected timezone — today−6d →
// today — and the "7d" preset reads as active. An explicit URL range always
// wins (shareable links, refresh, back/forward). Presets are defined here so
// the header buttons, the default and the active-state test agree on one
// table.

import { dayInTZ } from "./api.ts";

export const presets = [
  { label: "7d", days: 7 },
  { label: "30d", days: 30 },
  { label: "90d", days: 90 },
  { label: "all", days: 0 },
] as const;
export type PresetLabel = (typeof presets)[number]["label"];

export const ALL_FROM = "1970-01-01";
export const DEFAULT_PRESET: PresetLabel = "7d";

// daysAgo: the calendar day n days before `now` in tz (0 = today). The zoned
// YYYY-MM-DD is derived FIRST, then n is subtracted on the date parts as
// calendar days (Date.UTC over the parts — a DST-free proleptic calendar), so
// the answer is always exactly n local calendar days back. Subtracting n UTC
// days before zoning (the old way) is n×24 h, which across a spring-forward
// in a negative-offset zone lands one local day early (an eight-day "7d").
export function daysAgo(tz: string, n: number, now: Date = new Date()): string {
  return shiftDay(dayInTZ(now, tz), -n);
}

// shiftDay: YYYY-MM-DD ± n calendar days, timezone-free (the date parts only).
export function shiftDay(day: string, n: number): string {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

// presetRange: the from/to a preset selects at `now` in tz. "all" is an
// open start (1970-01-01) ending today.
export function presetRange(label: PresetLabel, tz: string, now: Date = new Date()): { from: string; to: string } {
  const p = presets.find((x) => x.label === label) ?? presets[0];
  return { from: p.days === 0 ? ALL_FROM : daysAgo(tz, p.days - 1, now), to: daysAgo(tz, 0, now) };
}

// defaultRange: what the dashboard opens on when the URL carries no range.
export function defaultRange(tz: string, now: Date = new Date()): { from: string; to: string } {
  return presetRange(DEFAULT_PRESET, tz, now);
}

// initialRange: explicit URL values win (each independently); missing ones
// fall back to the default range.
export function initialRange(
  urlFrom: string | null,
  urlTo: string | null,
  tz: string,
  now: Date = new Date(),
): { from: string; to: string } {
  const def = defaultRange(tz, now);
  return { from: urlFrom ?? def.from, to: urlTo ?? def.to };
}

// activePreset: which preset button (if any) exactly matches the current
// range at `now` in tz — null when the range is custom.
export function activePreset(from: string, to: string, tz: string, now: Date = new Date()): PresetLabel | null {
  for (const p of presets) {
    const r = presetRange(p.label, tz, now);
    if (r.from === from && r.to === to) return p.label;
  }
  return null;
}
