// PanelGrid (M6 Task 4): lays the panels out in a CSS grid by their
// saved order and column spans, and wires drag-to-reorder. It is pure
// presentation over the layout model (layout.ts) and the content map
// App passes in — no chart or filter logic lives here. Fullscreen is a
// single transient id (never persisted, never in the URL); a fullscreen
// panel restyles in place so its content (and every click-to-filter
// handler inside it) keeps working.

import { useState } from "react";
import type { ReactNode } from "react";
import {
  GRID_COLS,
  PANELS,
  reorder,
  setSpan,
  type Layout,
} from "../layout";
import Panel from "./Panel";

const defs = new Map(PANELS.map((p) => [p.id, p]));

export default function PanelGrid({
  layout,
  content,
  fullscreen,
  onLayout,
  onFullscreen,
}: {
  layout: Layout;
  content: Record<string, ReactNode>;
  fullscreen: string | null;
  onLayout: (l: Layout) => void;
  onFullscreen: (id: string | null) => void;
}) {
  const [dragId, setDragId] = useState<string | null>(null);
  return (
    <div
      className="grid gap-4"
      style={{ gridTemplateColumns: `repeat(${GRID_COLS}, minmax(0, 1fr))` }}
    >
      {layout.map((ps) => {
        const def = defs.get(ps.id);
        if (!def) return null;
        return (
          <Panel
            key={ps.id}
            def={def}
            span={ps.span}
            fullscreen={fullscreen === ps.id}
            dragging={dragId === ps.id}
            onToggleFullscreen={() => onFullscreen(fullscreen === ps.id ? null : ps.id)}
            onGrow={() => onLayout(setSpan(layout, ps.id, ps.span + 1))}
            onShrink={() => onLayout(setSpan(layout, ps.id, ps.span - 1))}
            onDragStart={() => setDragId(ps.id)}
            onDragOver={(e) => {
              if (dragId && dragId !== ps.id) e.preventDefault();
            }}
            onDrop={() => {
              if (dragId) onLayout(reorder(layout, dragId, ps.id));
              setDragId(null);
            }}
          >
            {content[ps.id]}
          </Panel>
        );
      })}
    </div>
  );
}
