// Sessions: one row per session with events in the range, sorted by
// API-equivalent (the hub's order), capped at the payload's limit. A row
// click toggles the session filter. Rows come from sessionLines
// (../aggregate, tested).

import { compactTokens, usd, type SessionsPayload } from "../api";
import { sessionLines } from "../aggregate";

export default function Sessions({
  data,
  tz,
  onSelect,
  active,
}: {
  data: SessionsPayload;
  tz: string;
  onSelect: (raw: string) => void;
  active: string[]; // currently filtered session ids
}) {
  const lines = sessionLines(data.sessions, tz);
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-tertiary">
          <th className="pb-2 font-normal">session</th>
          <th className="pb-2 font-normal">harness</th>
          <th className="pb-2 font-normal">project</th>
          <th className="pb-2 font-normal">start</th>
          <th className="pb-2 text-right font-normal">events</th>
          <th className="pb-2 text-right font-normal">tokens</th>
          <th className="pb-2 text-right font-normal">API-equivalent</th>
        </tr>
      </thead>
      <tbody>
        {lines.length === 0 && (
          <tr>
            <td colSpan={7} className="py-2 text-tertiary">
              no data in range
            </td>
          </tr>
        )}
        {lines.map((l, i) => (
          <tr
            key={`${i}|${l.raw}`}
            className="cursor-pointer border-t-[0.5px] border-hairline hover:bg-[var(--surface-hover)]"
            style={active.includes(l.raw) ? { background: "var(--accent-soft)" } : undefined}
            onClick={() => onSelect(l.raw)}
            title="click to filter"
          >
            <td className="py-1.5 pr-2 tabular-nums text-primary" title={l.raw || l.label}>{l.label}</td>
            <td className="py-1.5 pr-2 text-secondary">{l.harness}</td>
            <td className="max-w-48 truncate py-1.5 pr-2 text-secondary" title={l.projectRaw || l.project}>{l.project}</td>
            <td className="py-1.5 pr-2 tabular-nums text-secondary">{l.start}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{l.events}</td>
            <td className="py-1.5 text-right tabular-nums text-secondary">{compactTokens(l.tokens)}</td>
            <td className="py-1.5 text-right tabular-nums text-primary">{usd(l.equivMicro)}</td>
          </tr>
        ))}
      </tbody>
      {data.total > data.limit && (
        <tfoot>
          <tr>
            <td colSpan={7} className="pt-2 text-xs text-faint tabular-nums">
              {data.sessions.length} of {data.total} sessions
            </td>
          </tr>
        </tfoot>
      )}
    </table>
  );
}
