// Dark theme tones (M7 Task 2). tatitok ships DARK ONLY — this is a
// choice among three distinct dark CANVASES, never a light mode. The
// selection is LOCAL presentation state: it persists in the browser
// (localStorage) and never enters the URL, consistent with the M6
// layout-state ruling (URLs share what you are looking at, not how the
// surface is themed). A default is always available; an unknown or
// corrupt stored value can never strand the canvas — it falls back.
//
// The exact canvas/surface/hairline hexes live in
// styles/tokens/themes.css (the [data-theme] blocks); this module owns
// only the id set, persistence, and applying the choice to <html>.

export interface ThemeDef {
  id: string;
  label: string; // sentence case
}

// Three distinct dark canvases. Candidate tones — the owner picks the
// default and tunes the values from the rendered result.
export const THEMES: ThemeDef[] = [
  { id: "dark-gray", label: "dark gray" },
  { id: "light-gray", label: "light gray" },
];

// Provisional default (owner to confirm). Kept in sync with the inline
// no-flash script in index.html.
export const DEFAULT_THEME = "dark-gray";

export const THEME_KEY = "tatitok.theme.v1";

const VALID = new Set(THEMES.map((t) => t.id));

// normalizeTheme maps any input to a valid theme id, falling back to the
// default — so a corrupt/stale stored value never strands the canvas.
export function normalizeTheme(v: unknown): string {
  return typeof v === "string" && VALID.has(v) ? v : DEFAULT_THEME;
}

export function loadTheme(storage?: Pick<Storage, "getItem">): string {
  try {
    const s = storage ?? (typeof localStorage !== "undefined" ? localStorage : undefined);
    return normalizeTheme(s?.getItem(THEME_KEY) ?? null);
  } catch {
    return DEFAULT_THEME;
  }
}

export function saveTheme(id: string, storage?: Pick<Storage, "setItem">): void {
  try {
    const s = storage ?? (typeof localStorage !== "undefined" ? localStorage : undefined);
    s?.setItem(THEME_KEY, normalizeTheme(id));
  } catch {
    /* private mode / storage disabled — the selection just won't persist */
  }
}

// applyTheme reflects the choice onto <html data-theme>, which the
// [data-theme] blocks in themes.css override the canvas + surface steps
// on. Idempotent; safe to call on every change.
export function applyTheme(id: string): void {
  if (typeof document !== "undefined") {
    document.documentElement.dataset.theme = normalizeTheme(id);
  }
}
