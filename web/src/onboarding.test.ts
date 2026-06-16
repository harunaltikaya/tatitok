import { test } from "node:test";
import assert from "node:assert/strict";
import type { OnboardCard, OnboardDetect } from "./api.ts";
import {
  shouldOfferOnboarding,
  initialCardState,
  selectTier,
  setMetered,
  cardReady,
  allReady,
  applyPayload,
  matchTierByPriceMicro,
  tierLabel,
  tierProvenance,
  METERED,
} from "./onboarding.ts";

function card(over: Partial<OnboardCard>): OnboardCard {
  return {
    provider_arg: "codex",
    plan_name: "chatgpt-plus",
    provider: "openai",
    detected_tier: "",
    detected_raw: "",
    ambiguous: false,
    ambiguous_options: [],
    detection_source: "",
    subscription_signal: "",
    note: "",
    windows_present: false,
    metered_available: true,
    tiers: [
      { tier: "free", price_usd: "0" },
      { tier: "go", price_usd: "8" },
      { tier: "plus", price_usd: "20" },
      { tier: "pro_100", price_usd: "100" },
      { tier: "pro_200", price_usd: "200" },
    ],
    current: { declared: false, monthly_price_micro: null, window_seconds: 0, window_start: "" },
    ...over,
  };
}

const claudeCard = (over: Partial<OnboardCard> = {}): OnboardCard =>
  card({
    provider_arg: "claude",
    plan_name: "claude-max",
    provider: "anthropic",
    tiers: [
      { tier: "free", price_usd: "0" },
      { tier: "pro", price_usd: "20" },
      { tier: "max_5x", price_usd: "100" },
      { tier: "max_20x", price_usd: "200" },
    ],
    ...over,
  });

test("first-run trigger: usage + no plans → offer; otherwise not", () => {
  const base = { snapshot_version: "v", cards: [] };
  assert.equal(shouldOfferOnboarding({ ...base, has_usage: true, has_plans: false } as OnboardDetect), true);
  assert.equal(shouldOfferOnboarding({ ...base, has_usage: true, has_plans: true } as OnboardDetect), false);
  assert.equal(shouldOfferOnboarding({ ...base, has_usage: false, has_plans: false } as OnboardDetect), false);
  assert.equal(shouldOfferOnboarding(null), false);
});

test("initial state: unambiguous codex detection pre-fills tier + list price", () => {
  const s = initialCardState(card({ detected_tier: "plus" }));
  assert.equal(s.tier, "plus");
  assert.equal(s.price, "20");
  assert.equal(s.metered, false);
});

test("initial state: ambiguous codex Pro is NOT pre-filled", () => {
  const s = initialCardState(card({ detected_raw: "pro", ambiguous: true, ambiguous_options: ["pro_100", "pro_200"] }));
  assert.equal(s.tier, "");
});

test("initial state: claude is never pre-selected to a tier", () => {
  const s = initialCardState(claudeCard());
  assert.equal(s.tier, "");
});

test("initial state: editing an existing plan pre-fills the matching tier + price", () => {
  const s = initialCardState(
    claudeCard({ current: { declared: true, monthly_price_micro: 200_000_000, window_seconds: 18000, window_start: "floored" } }),
  );
  assert.equal(s.tier, "max_20x");
  assert.equal(s.price, "200");
});

test("matchTierByPriceMicro maps a declared price back to its tier", () => {
  assert.equal(matchTierByPriceMicro(card({}), 100_000_000), "pro_100");
  assert.equal(matchTierByPriceMicro(card({}), 999_000_000), ""); // custom price → no tier
  assert.equal(matchTierByPriceMicro(card({}), null), "");
});

test("selectTier resets price to that tier's list default and clears metered", () => {
  const c = card({});
  let s = setMetered(initialCardState(c), true);
  s = selectTier(c, s, "pro_200");
  assert.equal(s.tier, "pro_200");
  assert.equal(s.price, "200");
  assert.equal(s.metered, false);
});

test("readiness: metered always ready; otherwise a tier must be chosen", () => {
  assert.equal(cardReady({ providerArg: "claude", tier: "", price: "", metered: false }), false);
  assert.equal(cardReady({ providerArg: "claude", tier: "", price: "", metered: true }), true);
  assert.equal(cardReady({ providerArg: "claude", tier: "max_20x", price: "200", metered: false }), true);
  assert.equal(
    allReady([
      { providerArg: "claude", tier: "max_20x", price: "200", metered: false },
      { providerArg: "codex", tier: "", price: "", metered: false },
    ]),
    false,
  );
});

test("applyPayload: metered omits price; chosen tier carries the price", () => {
  const payload = applyPayload([
    { providerArg: "claude", tier: "", price: "", metered: true },
    { providerArg: "codex", tier: "plus", price: "20", metered: false },
  ]);
  assert.deepEqual(payload[0], { provider_arg: "claude", tier: METERED });
  assert.deepEqual(payload[1], { provider_arg: "codex", tier: "plus", price_usd: "20" });
});

test("tierLabel + tierProvenance render the honesty cues", () => {
  assert.equal(tierLabel("pro_200"), "Pro $200");
  assert.equal(tierLabel("max_5x"), "Max 5×");
  // Codex detected tier → "detected from your Codex logs".
  assert.match(tierProvenance(card({ detected_tier: "plus" }), "plus"), /detected from your Codex logs/);
  // Ambiguous Pro sub-tier → "$100 vs $200 isn't in the logs".
  assert.match(
    tierProvenance(card({ ambiguous: true, ambiguous_options: ["pro_100", "pro_200"] }), "pro_200"),
    /\$100 vs \$200/,
  );
  // Claude → "not auto-detectable".
  assert.match(tierProvenance(claudeCard(), "max_20x"), /not auto-detectable/);
});
