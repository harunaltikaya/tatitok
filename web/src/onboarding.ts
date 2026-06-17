// Pure onboarding logic — the confirm panel's state machine, kept free of
// React so it is testable with node:test. Honesty rules live here: Claude is
// never pre-selected to a tier, an ambiguous Codex Pro ($100/$200) stays
// unchosen, and metered is always an explicit choice.

import type { OnboardCard, OnboardDetect, OnboardApplyCard } from "./api.ts";

export const METERED = "metered";

export interface CardState {
  providerArg: string;
  tier: string; // "" = unchosen
  price: string; // editable USD; "" allowed (free → no price)
  metered: boolean;
  // edited tracks whether the user actually typed in the price field. An
  // untouched price is the snapshot list default, so applyPayload OMITS it and
  // the server records snapshot provenance — never a false "user-edited" label
  // (honesty, finding #3). Only setPrice flips this true.
  edited: boolean;
}

// shouldOfferOnboarding: the first-run trigger — usage exists (logs ingested)
// but no plans are declared yet.
export function shouldOfferOnboarding(d: OnboardDetect | null): boolean {
  return !!d && d.has_usage && !d.has_plans;
}

export function listPriceFor(card: OnboardCard, tier: string): string {
  const t = card.tiers.find((x) => x.tier === tier);
  return t ? t.price_usd : "";
}

export function microToUSD(micro: number): string {
  const v = micro / 1e6;
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}

// matchTierByPriceMicro finds the snapshot tier whose list price equals a
// declared monthly price (for editing an existing plan). "" if none matches.
export function matchTierByPriceMicro(card: OnboardCard, micro: number | null): string {
  if (micro == null) return "";
  for (const t of card.tiers) {
    if (Math.round(parseFloat(t.price_usd) * 1e6) === micro) return t.tier;
  }
  return "";
}

// initialCardState pre-fills from an existing declared plan (editing) first,
// else from detection. Claude is NEVER pre-selected (not derivable); Codex
// pre-fills only an UNAMBIGUOUS detection — an ambiguous Pro stays unchosen.
export function initialCardState(card: OnboardCard): CardState {
  // Every pre-fill is a default the user hasn't touched yet → edited=false.
  if (card.current.declared) {
    const tier = matchTierByPriceMicro(card, card.current.monthly_price_micro);
    const price =
      card.current.monthly_price_micro != null
        ? microToUSD(card.current.monthly_price_micro)
        : listPriceFor(card, tier);
    return { providerArg: card.provider_arg, tier, price, metered: false, edited: false };
  }
  if (card.detected_tier !== "" && !card.ambiguous) {
    return {
      providerArg: card.provider_arg,
      tier: card.detected_tier,
      price: listPriceFor(card, card.detected_tier),
      metered: false,
      edited: false,
    };
  }
  return { providerArg: card.provider_arg, tier: "", price: "", metered: false, edited: false };
}

// selectTier chooses a tier and resets the price to that tier's list default —
// a fresh default, so edited goes back to false.
export function selectTier(card: OnboardCard, s: CardState, tier: string): CardState {
  return { ...s, tier, price: listPriceFor(card, tier), metered: false, edited: false };
}

export function setMetered(s: CardState, metered: boolean): CardState {
  return { ...s, metered };
}

// setPrice records a user edit: the price is now theirs, not the list default.
export function setPrice(s: CardState, price: string): CardState {
  return { ...s, price, edited: true };
}

// cardReady: metered is always ready; otherwise a tier must be chosen.
export function cardReady(s: CardState): boolean {
  return s.metered || s.tier !== "";
}

export function allReady(states: CardState[]): boolean {
  return states.length > 0 && states.every(cardReady);
}

// applyPayload builds the POST body from per-card UI state. An untouched price
// is OMITTED (price_usd left undefined) so the server resolves the snapshot list
// default and records it as snapshot-sourced; only a user-edited price is sent,
// which the server records as "user-edited" (finding #3).
export function applyPayload(states: CardState[]): OnboardApplyCard[] {
  return states.map((s) => {
    if (s.metered) return { provider_arg: s.providerArg, tier: METERED };
    const card: OnboardApplyCard = { provider_arg: s.providerArg, tier: s.tier };
    if (s.edited) card.price_usd = s.price;
    return card;
  });
}

// tierLabel renders a tier name for humans (pro_100 → "Pro $100").
export function tierLabel(tier: string): string {
  switch (tier) {
    case "free":
      return "Free";
    case "go":
      return "Go";
    case "plus":
      return "Plus";
    case "pro":
      return "Pro";
    case "pro_100":
      return "Pro $100";
    case "pro_200":
      return "Pro $200";
    case "max_5x":
      return "Max 5×";
    case "max_20x":
      return "Max 20×";
    default:
      return tier;
  }
}

// tierProvenance is the per-tier honesty annotation shown in the picker.
export function tierProvenance(card: OnboardCard, tier: string): string {
  if (card.provider_arg === "codex") {
    if (!card.ambiguous && tier !== "" && tier === card.detected_tier) {
      return "detected from your Codex logs";
    }
    if (card.ambiguous && card.ambiguous_options.includes(tier)) {
      return "pick yours — $100 vs $200 isn't in the logs";
    }
    return "pick this";
  }
  return "pick this — not auto-detectable";
}

// windowsHint: a sub-vs-metered nudge from live limits, shown but never acted
// on automatically (the tier is always the user's choice).
export function windowsHint(card: OnboardCard): string {
  return card.windows_present
    ? "live usage-limit windows seen for this provider — you likely have a subscription"
    : "";
}
