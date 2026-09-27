// SSE consumption (M4 Task 3 contract): there is no replay — on
// reconnect, and on the server's `stale` event, the right move is to
// refetch the REST endpoints. EventSource reconnects on its own.
//
// The stream is held only while the tab is visible. Every open
// EventSource keeps one HTTP/1.1 socket, and Chrome allows 6 per host
// across all tabs, so hidden dashboard tabs holding streams starve every
// fetch (diag 0927). Hidden closes the stream; visible opens a new one,
// and its hello refetches like any reconnect.

import { useEffect, useState } from "react";
import type { IngestPass } from "./api";

export interface StreamState {
  connected: boolean;
  lastPass: IngestPass | null;
  // bump increments whenever data may have changed server-side
  // (ingest_pass, stale, reconnect) — effects keyed on it refetch.
  bump: number;
  // touchedDays from the most recent pass, for range-aware refetch.
  touchedDays: string[];
}

// streamWhileVisible opens a stream from `open` while doc is visible and
// closes it while hidden, feeding events to `update`. It returns the
// cleanup: remove the listener, close the stream. Plain function (no
// React) so node --test can drive it with a fake document and stream.
export function streamWhileVisible(
  doc: Pick<Document, "visibilityState" | "addEventListener" | "removeEventListener">,
  open: () => EventSource,
  update: (fn: (s: StreamState) => StreamState) => void,
): () => void {
  let es: EventSource | null = null;
  let wasConnected = false;

  const start = () => {
    if (es) return;
    const src = open();
    es = src;

    src.addEventListener("hello", () => {
      const reconnected = wasConnected;
      wasConnected = true;
      update((s) => ({
        ...s,
        connected: true,
        // A hello after a previous connection means we may have missed
        // events — refetch. touchedDays is cleared on every (re)open so
        // the refetch covers the whole range.
        bump: reconnected ? s.bump + 1 : s.bump,
        touchedDays: [],
      }));
    });

    src.addEventListener("ingest_pass", (ev) => {
      const pass = JSON.parse((ev as MessageEvent).data) as IngestPass;
      update((s) => ({
        ...s,
        lastPass: pass,
        bump: s.bump + 1,
        touchedDays: pass.touched_days ?? [],
      }));
    });

    src.addEventListener("stale", () => {
      // The hub dropped events for us (we were slow): refetch everything.
      update((s) => ({ ...s, bump: s.bump + 1, touchedDays: [] }));
    });

    src.onerror = () => {
      update((s) => ({ ...s, connected: false }));
    };
  };

  const stop = () => {
    if (!es) return;
    es.close();
    es = null;
    update((s) => ({ ...s, connected: false }));
  };

  const onVisibility = () => (doc.visibilityState === "visible" ? start() : stop());
  doc.addEventListener("visibilitychange", onVisibility);
  onVisibility();

  return () => {
    doc.removeEventListener("visibilitychange", onVisibility);
    es?.close();
    es = null;
  };
}

export function useStream(): StreamState {
  const [state, setState] = useState<StreamState>({
    connected: false,
    lastPass: null,
    bump: 0,
    touchedDays: [],
  });

  useEffect(() => streamWhileVisible(document, () => new EventSource("/api/v1/stream"), setState), []);

  return state;
}
