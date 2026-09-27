// Stream-while-visible test (diag 0927), Node's built-in runner. The hook
// needs a DOM and a React renderer, which this harness does not have, so
// the test drives the plain function useStream calls, with a fake
// document and fake EventSources.

import { test } from "node:test";
import assert from "node:assert/strict";
import { streamWhileVisible, type StreamState } from "./useStream.ts";

class FakeDoc {
  visibilityState = "visible";
  listeners = new Set<() => void>();
  addEventListener(_type: string, fn: () => void) {
    this.listeners.add(fn);
  }
  removeEventListener(_type: string, fn: () => void) {
    this.listeners.delete(fn);
  }
  setVisibility(v: string) {
    this.visibilityState = v;
    for (const fn of [...this.listeners]) fn();
  }
}

class FakeSource {
  closed = false;
  onerror: (() => void) | null = null;
  handlers = new Map<string, (ev: { data?: string }) => void>();
  addEventListener(type: string, fn: (ev: { data?: string }) => void) {
    this.handlers.set(type, fn);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data?: string) {
    if (!this.closed) this.handlers.get(type)?.({ data });
  }
}

test("hidden closes the stream; visible reopens it and its hello refetches with touchedDays empty", () => {
  const doc = new FakeDoc();
  const sources: FakeSource[] = [];
  let s: StreamState = { connected: false, lastPass: null, bump: 0, touchedDays: [] };
  const stop = streamWhileVisible(
    doc as never,
    () => {
      const src = new FakeSource();
      sources.push(src);
      return src as never;
    },
    (fn) => {
      s = fn(s);
    },
  );

  // Visible at mount: one stream; the first hello does not bump.
  assert.equal(sources.length, 1);
  sources[0].emit("hello");
  assert.equal(s.connected, true);
  assert.equal(s.bump, 0);
  sources[0].emit("ingest_pass", JSON.stringify({ touched_days: ["2026-09-27"] }));
  assert.equal(s.bump, 1);
  assert.deepEqual(s.touchedDays, ["2026-09-27"]);

  // Hidden: the stream is closed and nothing new opens.
  doc.setVisibility("hidden");
  assert.equal(sources[0].closed, true);
  assert.equal(sources.length, 1);
  assert.equal(s.connected, false);

  // Visible again: a new stream; its hello bumps and clears touchedDays,
  // so the range refetches whatever the last pass touched.
  doc.setVisibility("visible");
  assert.equal(sources.length, 2);
  sources[1].emit("hello");
  assert.equal(s.connected, true);
  assert.equal(s.bump, 2);
  assert.deepEqual(s.touchedDays, []);

  // Unmount: the stream closes and visibility no longer opens one.
  stop();
  assert.equal(sources[1].closed, true);
  assert.equal(doc.listeners.size, 0);
  doc.setVisibility("hidden");
  doc.setVisibility("visible");
  assert.equal(sources.length, 2);
});
