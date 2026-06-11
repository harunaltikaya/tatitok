// SSE consumption (M4 Task 3 contract): there is no replay — on
// reconnect, and on the server's `stale` event, the right move is to
// refetch the REST endpoints. EventSource reconnects on its own.

import { useEffect, useRef, useState } from "react";
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

export function useStream(): StreamState {
  const [state, setState] = useState<StreamState>({
    connected: false,
    lastPass: null,
    bump: 0,
    touchedDays: [],
  });
  const wasConnected = useRef(false);

  useEffect(() => {
    const es = new EventSource("/api/v1/stream");

    es.addEventListener("hello", () => {
      const reconnected = wasConnected.current;
      wasConnected.current = true;
      setState((s) => ({
        ...s,
        connected: true,
        // A hello after a previous connection means we may have missed
        // events — refetch.
        bump: reconnected ? s.bump + 1 : s.bump,
      }));
    });

    es.addEventListener("ingest_pass", (ev) => {
      const pass = JSON.parse((ev as MessageEvent).data) as IngestPass;
      setState((s) => ({
        ...s,
        lastPass: pass,
        bump: s.bump + 1,
        touchedDays: pass.touched_days ?? [],
      }));
    });

    es.addEventListener("stale", () => {
      // The hub dropped events for us (we were slow): refetch everything.
      setState((s) => ({ ...s, bump: s.bump + 1, touchedDays: [] }));
    });

    es.onerror = () => {
      setState((s) => ({ ...s, connected: false }));
    };

    return () => es.close();
  }, []);

  return state;
}
