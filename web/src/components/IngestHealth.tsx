// IngestHealth: one row per watch target from /api/v1/sources — when its
// harness last logged an event, when the watcher last ingested it, and
// the parse errors its last pass saw. Plain text; the only judgment is a
// non-zero error count in the warning color.

import type { SourceHealth } from "../api";
import { resetLabel } from "./Plans";

// Last ingest lives in the serve process: a source whose files have not
// changed since the serve started has no pass yet.
const when = (iso: string | null, none: string) => (iso ? resetLabel(Date.parse(iso)) : none);

export default function IngestHealth({ sources }: { sources: SourceHealth[] }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-tertiary">
          <th className="pb-2 font-normal">harness</th>
          <th className="pb-2 font-normal">root</th>
          <th className="pb-2 font-normal">watch</th>
          <th className="pb-2 font-normal">last event</th>
          <th className="pb-2 font-normal">last ingest</th>
          <th className="pb-2 text-right font-normal">errors (last pass)</th>
        </tr>
      </thead>
      <tbody>
        {sources.length === 0 && (
          <tr>
            <td colSpan={6} className="py-2 text-tertiary">
              no watched sources
            </td>
          </tr>
        )}
        {sources.map((s, i) => (
          <tr key={`${i}|${s.harness}|${s.root}`} className="border-t-[0.5px] border-hairline">
            <td className="py-1.5 pr-2 text-primary">{s.harness}</td>
            <td className="max-w-48 truncate py-1.5 pr-2 text-secondary" title={s.root}>{s.root}</td>
            <td className="py-1.5 pr-2 text-secondary">{s.watch}</td>
            <td className="py-1.5 pr-2 tabular-nums text-secondary">{when(s.last_event_at, "never")}</td>
            <td className="py-1.5 pr-2 tabular-nums text-secondary">{when(s.last_ingest_at, "none since start")}</td>
            <td className={`py-1.5 text-right tabular-nums ${s.parse_errors_last_pass > 0 ? "text-warning" : "text-secondary"}`}>
              {s.parse_errors_last_pass}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
