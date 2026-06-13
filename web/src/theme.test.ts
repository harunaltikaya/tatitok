// Dark-theme state tests (M7 Task 2), Node's built-in runner. The header
// selector wiring + the live canvas swap are exercised in the hard-stop-0
// drive; this pins the pure machinery: id validation, persistence
// round-trip, and the corrupt-value fallback (so a stale localStorage
// value can never strand the canvas).

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  THEMES,
  DEFAULT_THEME,
  THEME_KEY,
  normalizeTheme,
  loadTheme,
  saveTheme,
} from "./theme.ts";

test("DEFAULT_THEME is one of the declared tones", () => {
  assert.ok(THEMES.some((t) => t.id === DEFAULT_THEME));
});

test("normalizeTheme accepts known ids and falls back otherwise", () => {
  for (const t of THEMES) assert.equal(normalizeTheme(t.id), t.id);
  assert.equal(normalizeTheme("bogus"), DEFAULT_THEME);
  assert.equal(normalizeTheme(null), DEFAULT_THEME);
  assert.equal(normalizeTheme(undefined), DEFAULT_THEME);
  assert.equal(normalizeTheme(123), DEFAULT_THEME);
});

test("saveTheme/loadTheme round-trip every tone through a storage stub", () => {
  const m = new Map<string, string>();
  const storage = {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
  for (const t of THEMES) {
    saveTheme(t.id, storage);
    assert.equal(m.get(THEME_KEY), t.id);
    assert.equal(loadTheme(storage), t.id);
  }
});

test("a corrupt stored value loads as the default", () => {
  assert.equal(loadTheme({ getItem: () => "not-a-theme" }), DEFAULT_THEME);
  assert.equal(loadTheme({ getItem: () => null }), DEFAULT_THEME);
});
