// Panel chrome (M6 Task 4): the reusable frame the panel grid wraps each
// chart/table in — a draggable header with the title and the layout
// controls (resize narrower/wider, fullscreen), plus a body that sizes
// charts (explicit height for ECharts) vs tables (grows, scrolls when
// fullscreen). It owns NO chart logic — content is passed as children —
// so M7's design reshape touches this file, not the charts, and filter
// interactions inside the content survive every layout state (the same
// element is restyled, never remounted, when it goes fullscreen).

import type { ReactNode } from "react";
import type { PanelDef } from "../layout";

export default function Panel({
  def,
  span,
  fullscreen,
  dragging,
  onToggleFullscreen,
  onGrow,
  onShrink,
  onDragStart,
  onDragOver,
  onDrop,
  children,
}: {
  def: PanelDef;
  span: number;
  fullscreen: boolean;
  dragging: boolean;
  onToggleFullscreen: () => void;
  onGrow: () => void;
  onShrink: () => void;
  onDragStart: () => void;
  onDragOver: (e: React.DragEvent) => void;
  onDrop: () => void;
  children: ReactNode;
}) {
  const isChart = def.kind === "chart";
  const bodyClass = isChart
    ? fullscreen
      ? "min-h-0 flex-1"
      : "h-72"
    : fullscreen
      ? "min-h-0 flex-1 overflow-auto"
      : "overflow-auto";
  return (
    <section
      className={`flex flex-col rounded-[14px] border-[0.5px] border-hairline ${
        fullscreen ? "fixed inset-3 z-50 bg-raised" : "bg-card"
      } ${dragging ? "opacity-40" : ""}`}
      style={fullscreen ? { boxShadow: "var(--shadow-overlay)" } : { gridColumn: `span ${span} / span ${span}` }}
      onDragOver={onDragOver}
      onDrop={onDrop}
      aria-label={def.title}
    >
      <header
        className="flex items-center gap-2 px-4 pt-3 pb-2"
        draggable={!fullscreen}
        onDragStart={onDragStart}
        title={fullscreen ? undefined : "drag to reorder"}
      >
        <h2 className={`truncate text-xs font-medium text-tertiary ${fullscreen ? "" : "cursor-move"}`}>
          {def.title}
        </h2>
        <div className="ml-auto flex shrink-0 items-center gap-0.5 text-tertiary">
          {!fullscreen && (
            <>
              <button
                className="rounded-[6px] px-1.5 py-0.5 hover:bg-[var(--surface-hover)] hover:text-primary"
                onClick={onShrink}
                aria-label={`make ${def.title} narrower`}
                title="narrower"
              >
                −
              </button>
              <button
                className="rounded-[6px] px-1.5 py-0.5 hover:bg-[var(--surface-hover)] hover:text-primary"
                onClick={onGrow}
                aria-label={`make ${def.title} wider`}
                title="wider"
              >
                +
              </button>
            </>
          )}
          <button
            className="rounded-[6px] px-1.5 py-0.5 hover:bg-[var(--surface-hover)] hover:text-primary"
            onClick={onToggleFullscreen}
            aria-label={fullscreen ? `exit fullscreen for ${def.title}` : `fullscreen ${def.title}`}
            title={fullscreen ? "exit fullscreen (Esc)" : "fullscreen"}
          >
            {fullscreen ? "✕" : "⤢"}
          </button>
        </div>
      </header>
      <div className={`px-4 pb-4 ${bodyClass}`}>{children}</div>
    </section>
  );
}
