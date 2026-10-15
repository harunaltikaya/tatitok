// Trailing coalescer for the live refetch (hot loop 2026-10-15). While
// sessions are active, ingest passes arrive every ~2 s and each
// dashboard refetch is several seconds of SQL across eight requests;
// refetching per pass kept ~3 cores busy per open tab. The coalescer
// runs the work at once when the last run is older than minMs, and
// otherwise schedules ONE trailing run at last+minMs — every request in
// between folds into it. The work closure is replaced by the latest
// request, so the trailing run sees current state. Pure (clock and
// timer injectable) so node --test can drive it.

export interface Coalescer {
  request(work: () => void): void;
  cancel(): void;
}

export function makeCoalescer(
  minMs: number,
  now: () => number = Date.now,
  setTimer: (fn: () => void, ms: number) => unknown = setTimeout,
  clearTimer: (id: unknown) => void = (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
): Coalescer {
  let last = -Infinity;
  let timer: unknown = null;
  let pending: (() => void) | null = null;
  const fire = () => {
    timer = null;
    const work = pending;
    pending = null;
    last = now();
    work?.();
  };
  return {
    request(work) {
      pending = work;
      if (timer !== null) return;
      const wait = last + minMs - now();
      if (wait <= 0) fire();
      else timer = setTimer(fire, wait);
    },
    cancel() {
      if (timer !== null) clearTimer(timer);
      timer = null;
      pending = null;
    },
  };
}
