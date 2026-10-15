import { test } from "node:test";
import assert from "node:assert/strict";
import { makeCoalescer } from "./coalesce.ts";

// A fake clock + timer queue so the trailing schedule is deterministic.
function harness(minMs: number) {
  let t = 1_000_000;
  const timers: { at: number; fn: () => void; id: number }[] = [];
  let nextId = 1;
  const c = makeCoalescer(
    minMs,
    () => t,
    (fn, ms) => {
      const id = nextId++;
      timers.push({ at: t + ms, fn, id });
      return id;
    },
    (id) => {
      const i = timers.findIndex((x) => x.id === id);
      if (i >= 0) timers.splice(i, 1);
    },
  );
  const advance = (ms: number) => {
    t += ms;
    for (const x of [...timers].sort((a, b) => a.at - b.at)) {
      if (x.at <= t) {
        timers.splice(timers.indexOf(x), 1);
        x.fn();
      }
    }
  };
  return { c, advance, timers };
}

test("first request runs at once; a burst inside the window folds into ONE trailing run with the latest work", () => {
  const { c, advance, timers } = harness(10_000);
  const runs: string[] = [];
  c.request(() => runs.push("a"));
  assert.deepEqual(runs, ["a"]);
  advance(2_000);
  c.request(() => runs.push("b"));
  advance(2_000);
  c.request(() => runs.push("c"));
  advance(2_000);
  c.request(() => runs.push("d"));
  assert.deepEqual(runs, ["a"], "nothing runs inside the window");
  assert.equal(timers.length, 1, "one trailing timer");
  advance(4_000); // t = last + 10 s
  assert.deepEqual(runs, ["a", "d"], "trailing run uses the latest work");
  // Quiet afterwards: a request past the window runs at once again.
  advance(10_000);
  c.request(() => runs.push("e"));
  assert.deepEqual(runs, ["a", "d", "e"]);
});

test("steady 2 s passes produce one run per window, not per pass", () => {
  const { c, advance } = harness(10_000);
  let runs = 0;
  for (let i = 0; i < 30; i++) {
    c.request(() => runs++);
    advance(2_000);
  }
  // 60 s of passes: the immediate run, then one trailing run per 10 s window.
  assert.ok(runs >= 6 && runs <= 7, `runs = ${runs}`);
});

test("cancel drops the pending run", () => {
  const { c, advance, timers } = harness(10_000);
  let runs = 0;
  c.request(() => runs++);
  c.request(() => runs++);
  assert.equal(timers.length, 1);
  c.cancel();
  assert.equal(timers.length, 0);
  advance(20_000);
  assert.equal(runs, 1);
});
