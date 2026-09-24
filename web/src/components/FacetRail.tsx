import { useEffect, useState } from "react";
import type { FacetValue } from "../api";
import { compactTokens } from "../api";
import { displayValue, facetDims, hasValue, projectLabel, type FacetDim, type FilterState } from "../filters";
import { railItems, FILTER_TOP_N, type Locals } from "../aggregate";
import Card from "../ui/Card";

// The left facet rail (M5 Task 4, the owner's Qlik-style direction): every
// filterable dimension with its stored values and event counts (from
// /api/v1/meta/facets — all-time counts, the value inventory). Clicking a value
// toggles it in the SAME global filter state every chart, table and total
// obeys. Project values render locally only — the hub serves localhost, and
// nothing here is ever exported.
//
// M8 1G — density on BOTH rails (home + detail): family collapse (the
// local-basis providers, and the local-basis models, → one "local" group on
// their dim; membership comes from the served inventory, never from the name) + top-N "+others" on the long dims, rendered
// as EXPANDABLE groups. Display-only (railItems is a pure regroup of the served
// counts); group headers are expand toggles, the leaves filter by exact value.

// Long dims get the top-N + "others" tail; short dims (harness/provider/basis)
// show every (family-collapsed) value.
const ROLLED_DIMS: FacetDim[] = ["model", "project"];

// Expansion (which family/others groups are open) is presentation DENSITY — it
// persists to localStorage like theme/layout, never the URL (state-location
// ruling). Default: all collapsed.
const RAIL_KEY = "tatitok.rail.v1";

function loadExpanded(): Set<string> {
  if (typeof localStorage === "undefined") return new Set();
  try {
    const arr = JSON.parse(localStorage.getItem(RAIL_KEY) ?? "[]");
    return Array.isArray(arr) ? new Set(arr.filter((x: unknown): x is string => typeof x === "string")) : new Set();
  } catch {
    return new Set();
  }
}

export default function FacetRail({
  facets,
  filters,
  localsFor,
  onToggle,
}: {
  facets: Record<string, FacetValue[]>;
  filters: FilterState;
  // The local-basis set for a dim (App: localProviders / localModels on the
  // provider / model dims; undefined elsewhere → plain leaves).
  localsFor?: (dim: FacetDim) => Locals | undefined;
  onToggle: (dim: FacetDim, value: string) => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(loadExpanded);
  useEffect(() => {
    try {
      localStorage.setItem(RAIL_KEY, JSON.stringify([...expanded]));
    } catch {
      /* storage disabled — expansion just won't persist */
    }
  }, [expanded]);
  const toggleExpand = (id: string) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  // A leaf row: clicking toggles its EXACT value in the global filter state.
  // indent marks a value revealed inside an expanded group.
  const leaf = (dim: FacetDim, value: string, events: number, indent = false) => {
    const selected = hasValue(filters, dim, value);
    return (
      <button
        className={`flex w-full items-center justify-between rounded-[6px] py-1 text-left ${indent ? "pl-5 pr-1.5" : "px-1.5"}`}
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
        onClick={() => onToggle(dim, value)}
        title={`${displayValue(value)} — ${events} events (click to ${selected ? "unfilter" : "filter"})`}
      >
        {dim === "project" ? (
          <span className="truncate" title={value || undefined}>{projectLabel(value)}</span>
        ) : (
          <span className="truncate">{displayValue(value)}</span>
        )}
        <span className="ml-2 shrink-0 text-xs tabular-nums" style={{ color: "var(--text-faint)" }}>
          {compactTokens(events)}
        </span>
      </button>
    );
  };

  return (
    <aside className="w-56 shrink-0 space-y-3">
      {facetDims.map((dim) => {
        const all = facets[dim] ?? [];
        if (all.length === 0) return null;
        const items = railItems(all, ROLLED_DIMS.includes(dim), FILTER_TOP_N, localsFor?.(dim));
        return (
          <Card key={dim} padding={12} title={dim}>
            <ul className="space-y-px text-sm">
              {items.map((item) => {
                if (item.kind === "leaf") {
                  return <li key={item.value}>{leaf(dim, item.value, item.events)}</li>;
                }
                // family / others GROUP: the header is an expand toggle ONLY
                // (no filter semantics); its members are leaves, revealed when
                // open and each filtering by its exact value.
                const id = `${dim}:${item.key}`;
                const open = expanded.has(id);
                const header = item.kind === "others" ? `+${item.members.length} others` : item.label;
                return (
                  <li key={id}>
                    <button
                      className="flex w-full items-center justify-between rounded-[6px] px-1.5 py-1 text-left"
                      style={{ background: "transparent", color: "var(--text-tertiary)" }}
                      onMouseEnter={(e) => (e.currentTarget.style.background = "var(--surface-hover)")}
                      onMouseLeave={(e) => (e.currentTarget.style.background = "transparent")}
                      onClick={() => toggleExpand(id)}
                      aria-expanded={open}
                      title={`${open ? "collapse" : "expand"} ${item.members.length} ${dim} value${item.members.length === 1 ? "" : "s"}`}
                    >
                      <span className="truncate">
                        <span aria-hidden="true" className="mr-1 inline-block w-2 text-faint">{open ? "▾" : "▸"}</span>
                        {header}
                      </span>
                      <span className="ml-2 shrink-0 text-xs tabular-nums" style={{ color: "var(--text-faint)" }}>
                        {compactTokens(item.events)}
                      </span>
                    </button>
                    {open && (
                      <ul className="space-y-px">
                        {item.members.map((m) => (
                          <li key={m.value}>{leaf(dim, m.value, m.events, true)}</li>
                        ))}
                      </ul>
                    )}
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
