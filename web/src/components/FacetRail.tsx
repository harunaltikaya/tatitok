import type { FacetValue } from "../api";
import { compactTokens } from "../api";
import { displayValue, facetDims, hasValue, type FacetDim, type FilterState } from "../filters";

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
    <aside className="w-56 shrink-0 space-y-4">
      {facetDims.map((dim) => {
        const vals = facets[dim] ?? [];
        if (vals.length === 0) return null;
        return (
          <section key={dim} className="rounded-xl border border-zinc-800 bg-zinc-900/60 p-3">
            <h3 className="mb-2 text-xs font-semibold uppercase tracking-wider text-zinc-500">{dim}</h3>
            <ul className="space-y-0.5 text-sm">
              {vals.map((v) => {
                const selected = hasValue(filters, dim, v.value);
                return (
                  <li key={v.value}>
                    <button
                      className={`flex w-full items-center justify-between rounded px-1.5 py-0.5 text-left hover:bg-zinc-800 ${
                        selected ? "bg-sky-950/60 text-sky-300" : "text-zinc-300"
                      }`}
                      onClick={() => onToggle(dim, v.value)}
                      title={`${displayValue(v.value)} — ${v.events} events (click to ${selected ? "unfilter" : "filter"})`}
                    >
                      <span className="truncate">{displayValue(v.value)}</span>
                      <span className="ml-2 shrink-0 text-xs tabular-nums text-zinc-500">
                        {compactTokens(v.events)}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </aside>
  );
}
