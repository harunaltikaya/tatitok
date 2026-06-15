// Reported usage-limits resolver tests (M9, Codex NO-SHIP follow-up).
//
// HIGH blocker regression guard: the backend validly accepts a provider with no
// windows, which Go marshals as "windows": null. PlanCard used to do
// `bucket.windows.length` on that → TypeError → whole-dashboard render crash.
// reportedFor() must coerce null/absent windows to [] so the card falls through
// to the empty-state instead of throwing.
//
// PlanCard itself can't be rendered here — `node --test` strips TS types but
// does NOT transform JSX/.tsx (confirmed: ERR_UNKNOWN_FILE_EXTENSION) and the
// test runner has no loader. So we pin the pure resolver the card renders from;
// it is the exact code path that crashed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { reportedFor, providerForPlan } from "./api.ts";

test("reportedFor: null windows → [] (the HIGH crash case) — no throw", () => {
  // Go marshals an empty windows slice as JSON null; model that exactly.
  const limits = { claude: { fetchedAt: 1_700_000_000_000, windows: null } };
  const got = reportedFor("claude-max", limits);
  assert.deepEqual(got.windows, []); // empty → the card renders the placeholder
  assert.equal(got.fetchedAt, 1_700_000_000_000);
});

test("reportedFor: missing bucket / no limits → [] (empty-state, fetchedAt 0)", () => {
  assert.deepEqual(reportedFor("claude-max", {}).windows, []);
  assert.deepEqual(reportedFor("claude-max", undefined).windows, []);
  assert.equal(reportedFor("claude-max", {}).fetchedAt, 0);
});

test("reportedFor: populated windows pass through unchanged", () => {
  const limits = {
    codex: { fetchedAt: 9, windows: [{ label: "5h", usedPercent: 90, resetAt: 0 }] },
  };
  const got = reportedFor("chatgpt-plus", limits);
  assert.equal(got.windows.length, 1);
  assert.equal(got.windows[0].label, "5h");
  assert.equal(got.fetchedAt, 9);
});

test("providerForPlan: the two declared plans resolve", () => {
  assert.equal(providerForPlan("claude-max"), "claude");
  assert.equal(providerForPlan("chatgpt-plus"), "codex");
  assert.equal(providerForPlan("Claude-Max"), "claude"); // case-insensitive
});

test("providerForPlan: tightened — bare 'gpt' no longer mis-maps; unknown → null", () => {
  assert.equal(providerForPlan("my-gpt-helper"), null); // bare "gpt" dropped
  assert.equal(providerForPlan("some-random-plan"), null);
  assert.equal(providerForPlan("openai-pro"), "codex"); // explicit "openai" still maps
  assert.equal(providerForPlan("anthropic-team"), "claude");
});
