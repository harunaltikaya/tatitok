// Panel layout model (M6 Task 4). Layout is LOCAL presentation state:
// it persists in the browser (localStorage) and NEVER enters the URL —
// URLs share what you are looking at (filters, range, timezone), not how
// your monitor is arranged. This module is the pure machinery — panel
// set, ordering, column spans, persistence, reset — kept free of React
// and chart internals so M7 can restyle the chrome without touching it
// (and so Node's test runner can exercise it). Fullscreen is transient
// (App state), deliberately not modelled here.

// PanelKind drives only the body sizing (charts need an explicit height
// for ECharts; tables grow with content) — a layout concern, not a
// chart concern.
export type PanelKind = "chart" | "table";

export interface PanelDef {
  id: string;
  title: string;
  defaultSpan: number;
  kind: PanelKind;
}

// GRID_COLS is the column count the spans are expressed in; MIN_SPAN/
// GRID_COLS clamp resizing.
export const GRID_COLS = 6;
export const MIN_SPAN = 1;

// PANELS is the canonical panel set in default order with default spans.
// Adding a panel here makes it appear even over an older saved layout
// (loadLayout appends unknown-to-the-save panels), so a code change
// never strands a saved layout or hides a new panel.
export const PANELS: PanelDef[] = [
  { id: "chart-equiv", title: "daily API-equivalent (by provider)", defaultSpan: 3, kind: "chart" },
  { id: "chart-actual", title: "daily actual cost (by provider)", defaultSpan: 3, kind: "chart" },
  { id: "chart-tokens", title: "daily tokens (by provider)", defaultSpan: 6, kind: "chart" },
  { id: "break-harness", title: "by harness", defaultSpan: 2, kind: "table" },
  { id: "break-provider", title: "by provider", defaultSpan: 2, kind: "table" },
  { id: "break-model", title: "by model", defaultSpan: 2, kind: "table" },
  { id: "ingest-health", title: "ingest health", defaultSpan: 4, kind: "table" },
];

export interface PanelState {
  id: string;
  span: number;
}
export type Layout = PanelState[];

// LAYOUT_KEY is the localStorage key; v1 lets a future shape migrate
// cleanly rather than silently misread.
export const LAYOUT_KEY = "tatitok.layout.v1";

export function defaultLayout(): Layout {
  return PANELS.map((p) => ({ id: p.id, span: p.defaultSpan }));
}

export function clampSpan(v: unknown): number {
  const n = Math.round(Number(v));
  if (!Number.isFinite(n)) return MIN_SPAN;
  return Math.max(MIN_SPAN, Math.min(GRID_COLS, n));
}

// loadLayout merges a saved layout with the canonical panel set: known
// panels keep their saved order and clamped span, ids no longer in the
// code are dropped, and panels new in the code are appended in default
// position. A missing or corrupt save yields the default — the layout is
// a convenience, never load-bearing.
export function loadLayout(raw: string | null): Layout {
  const def = defaultLayout();
  if (!raw) return def;
  let saved: unknown;
  try {
    saved = JSON.parse(raw);
  } catch {
    return def;
  }
  if (!Array.isArray(saved)) return def;
  const known = new Set(def.map((p) => p.id));
  const seen = new Set<string>();
  const out: Layout = [];
  for (const item of saved) {
    if (!item || typeof item !== "object") continue;
    const id = (item as { id?: unknown }).id;
    if (typeof id !== "string" || !known.has(id) || seen.has(id)) continue;
    out.push({ id, span: clampSpan((item as { span?: unknown }).span) });
    seen.add(id);
  }
  for (const p of def) {
    if (!seen.has(p.id)) out.push(p);
  }
  return out;
}

export function serializeLayout(l: Layout): string {
  return JSON.stringify(l);
}

// reorder moves fromId to toId's position (array move) — the drop target
// of a drag-reorder.
export function reorder(l: Layout, fromId: string, toId: string): Layout {
  const from = l.findIndex((p) => p.id === fromId);
  const to = l.findIndex((p) => p.id === toId);
  if (from < 0 || to < 0 || from === to) return l;
  const copy = l.slice();
  const [moved] = copy.splice(from, 1);
  copy.splice(to, 0, moved);
  return copy;
}

// setSpan resizes one panel, clamped to [MIN_SPAN, GRID_COLS].
export function setSpan(l: Layout, id: string, span: number): Layout {
  return l.map((p) => (p.id === id ? { id, span: clampSpan(span) } : p));
}
