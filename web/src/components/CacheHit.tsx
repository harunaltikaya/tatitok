// CacheHit: prompt-cache hit rate per harness over the selected range —
// cache-read ÷ (input + cache-read), with both counts beside it and a
// total row. Rows come from cacheHitRows (../aggregate, tested).

import { compactTokens, type DailyByRow } from "../api";
import { cacheHitRows } from "../aggregate";

const rateTitle = "cache-read ÷ (input + cache-read); — when both are 0";

export default function CacheHit({ rows }: { rows: DailyByRow[] }) {
  const { rows: harnesses, total } = cacheHitRows(rows);
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-tertiary">
          <th className="pb-2 font-normal">harness</th>
          <th className="pb-2 text-right font-normal" title={rateTitle}>hit rate</th>
          <th className="pb-2 text-right font-normal">cache read</th>
          <th className="pb-2 text-right font-normal">input</th>
        </tr>
      </thead>
      <tbody>
        {harnesses.length === 0 && (
          <tr>
            <td colSpan={4} className="py-2 text-tertiary">
              no data in range
            </td>
          </tr>
        )}
        {harnesses.map((h) => (
          <tr key={h.raw} className="border-t-[0.5px] border-hairline">
            <td className="max-w-48 truncate py-1.5 pr-2 text-primary" title={h.raw || h.key}>{h.key}</td>
            <td className="py-1.5 text-right tabular-nums text-primary">{h.hitRate}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(h.cacheRead)}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(h.input)}</td>
          </tr>
        ))}
      </tbody>
      {harnesses.length > 0 && (
        <tfoot>
          <tr className="border-t-[0.5px] border-hairline">
            <td className="py-1.5 pr-2 text-tertiary">{total.key}</td>
            <td className="py-1.5 text-right tabular-nums text-primary">{total.hitRate}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(total.cacheRead)}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(total.input)}</td>
          </tr>
        </tfoot>
      )}
    </table>
  );
}
