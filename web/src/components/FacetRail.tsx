import type { FacetValue } from "../api";
import { compactTokens } from "../api";
import { displayValue, facetDims, hasValue, type FacetDim, type FilterState } from "../filters";
import Card from "../ui/Card";

// The left facet rail (M5 Task 4, the owner's Qlik-style direction):
// every filterable dimension with its stored values and event counts
// (from /api/v1/meta/facets — all-time counts, the value inventory).
// Clicking a value toggles it in the SAME global filter state every
// chart, table and total obeys. Project values render locally only —
// the hub serves localhost, and nothing here is ever exported.

export default function FacetRail({
  facets,
  filters,
  onToggle,
}: {
  facets: Record<string, FacetValue[]>;
  filters: FilterState;
  onToggle: (dim: FacetDim, value: string) => void;
}) {
  return (
    <aside className="w-56 shrink-0 space-y-3">
      {facetDims.map((dim) => {
        const vals = facets[dim] ?? [];
        if (vals.length === 0) return null;
        return (
          <Card key={dim} padding={12} title={dim}>
            <ul className="space-y-px text-sm">
              {vals.map((v) => {
                const selected = hasValue(filters, dim, v.value);
                return (
                  <li key={v.value}>
                    <button
                      className="flex w-full items-center justify-between rounded-[6px] px-1.5 py-1 text-left"
                      style={
                        selected
                          ? { background: "var(--accent-soft)", color: "var(--accent)" }
                          : { background: "transparent", color: "var(--text-secondary)" }
                      }
                      onMouseEnter={(e) => {
                        if (!selected) e.currentTarget.style.background = "var(--surface-hover)";
                      }}
                      onMouseLeave={(e) => {
                        if (!selected) e.currentTarget.style.background = "transparent";
                      }}
                      onClick={() => onToggle(dim, v.value)}
                      title={`${displayValue(v.value)} — ${v.events} events (click to ${selected ? "unfilter" : "filter"})`}
                    >
                      <span className="truncate">{displayValue(v.value)}</span>
                      <span className="ml-2 shrink-0 text-xs tabular-nums" style={{ color: "var(--text-faint)" }}>
                        {compactTokens(v.events)}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </Card>
        );
      })}
    </aside>
  );
}
