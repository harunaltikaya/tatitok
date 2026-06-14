// Provider brand-colour tests (M8 chunk 1F), Node's built-in runner. The live
// brand colour on the by-provider chart/donut/table is exercised in the
// chunk-F drive; this pins the pure machinery: brandColorFor maps anthropic →
// its brand token and every other provider → null (fall back to per-entity),
// plus a COLLISION GUARD — the chosen clay must stay perceptually distinct from
// metered-amber (#f5b547) and danger coral-red (#f0726f), so an accidental
// shade that collides with either is caught. Colour only: no number changes.

import { test } from "node:test";
import assert from "node:assert/strict";
import { brandColorFor } from "./aggregate.ts";

// rgb parses a #rrggbb hex; perceptualDist is the low-cost "redmean"
// approximation of perceptual colour distance — good enough to flag a shade
// that has drifted into a neighbour's territory.
function rgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "");
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}
function perceptualDist(a: string, b: string): number {
  const [r1, g1, b1] = rgb(a);
  const [r2, g2, b2] = rgb(b);
  const rmean = (r1 + r2) / 2;
  const dr = r1 - r2;
  const dg = g1 - g2;
  const db = b1 - b2;
  return Math.sqrt((2 + rmean / 256) * dr * dr + 4 * dg * dg + (2 + (255 - rmean) / 256) * db * db);
}

test("brand: anthropic → brand colour, others → none, clear of amber + danger", () => {
  // Only anthropic carries a brand colour; everyone else falls back (null).
  assert.equal(brandColorFor("anthropic"), "#cc785c");
  assert.equal(brandColorFor("openai"), null);
  assert.equal(brandColorFor("deepseek"), null);
  assert.equal(brandColorFor("google"), null);
  assert.equal(brandColorFor("vllm"), null); // collapsed family bucket
  assert.equal(brandColorFor("others"), null); // aggregate bucket

  // Collision guard: the brand clay must read distinct from the two warm tones
  // it sits near. Self-distance is 0, so the threshold is a real separation.
  const brand = brandColorFor("anthropic")!;
  const THRESHOLD = 40;
  assert.equal(perceptualDist(brand, brand), 0);
  assert.ok(perceptualDist(brand, "#f5b547") > THRESHOLD, "brand collides with metered-amber");
  assert.ok(perceptualDist(brand, "#f0726f") > THRESHOLD, "brand collides with danger coral-red");
});
