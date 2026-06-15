// storage.js — the extension's chrome.storage.local shape, in one place.
//
// Two top-level keys:
//
//   "limits": the latest normalized snapshot per provider. Written by the
//             pollers (sw.js) and read back by sw.js to POST to the hub.
//
//     {
//       claude: { fetchedAt: <epoch-ms>, windows: [ Window, ... ] } | null,
//       codex:  { fetchedAt: <epoch-ms>, windows: [ Window, ... ] } | null
//     }
//
//     Window = { label: string, usedPercent: number, resetAt: <epoch-ms> }
//       label       — window name ("5h", "Weekly", "7d", "Sonnet 7d", …)
//       usedPercent — 0..100 utilization the provider reports
//       resetAt     — when the window resets, epoch MILLISECONDS. Both
//                     providers are normalized to ms even though Claude
//                     sends an ISO string and Codex sends Unix seconds.
//
//   "rules": knobs.
//     { pollIntervalSeconds: number, claudeOrgId: string }
//       claudeOrgId — OPTIONAL owner override (options page); "" → the Claude
//                     org is auto-discovered from the logged-in session.
//     (The ChatGPT message-cap rules arrive with the counter chunk, not here.)
//
//   "claudeOrg": the Claude org id discovered from the live claude.ai session,
//     cached so we don't re-discover every poll (string, or absent until first
//     discovered). Cleared when a usage call shows it's stale (403/404), so the
//     next cycle re-discovers. NEVER a hardcoded value — it is per-user.

export const LIMITS_KEY = "limits";
export const RULES_KEY = "rules";
export const ORG_KEY = "claudeOrg"; // cached discovered Claude org id (per-user)

// claudeOrgId is the OPTIONAL owner override (options page); "" → auto-discover.
export const DEFAULT_RULES = { pollIntervalSeconds: 90, claudeOrgId: "" };

// Documents the "limits" shape before the first successful poll.
export const EMPTY_LIMITS = { claude: null, codex: null };

// Seed defaults on install WITHOUT clobbering anything already stored.
export async function seedDefaults() {
  const cur = await chrome.storage.local.get([RULES_KEY, LIMITS_KEY]);
  const patch = {};
  if (cur[RULES_KEY] === undefined) patch[RULES_KEY] = DEFAULT_RULES;
  if (cur[LIMITS_KEY] === undefined) patch[LIMITS_KEY] = EMPTY_LIMITS;
  if (Object.keys(patch).length > 0) await chrome.storage.local.set(patch);
}

export async function getRules() {
  const cur = await chrome.storage.local.get(RULES_KEY);
  return { ...DEFAULT_RULES, ...(cur[RULES_KEY] ?? {}) };
}

export async function getLimits() {
  const cur = await chrome.storage.local.get(LIMITS_KEY);
  return { ...EMPTY_LIMITS, ...(cur[LIMITS_KEY] ?? {}) };
}

// Replace one provider's snapshot, leaving the other untouched. Pollers call
// this only on success; on failure they don't, so the last good snapshot for
// that provider is preserved.
export async function setProviderLimit(provider, snapshot) {
  const limits = await getLimits();
  limits[provider] = snapshot;
  await chrome.storage.local.set({ [LIMITS_KEY]: limits });
}

// Discovered Claude org id cache (sw.js discovers it from the live session and
// caches it here; the owner override lives separately in rules.claudeOrgId).
export async function getDiscoveredOrg() {
  const cur = await chrome.storage.local.get(ORG_KEY);
  return cur[ORG_KEY] ?? null;
}
export async function setDiscoveredOrg(id) {
  await chrome.storage.local.set({ [ORG_KEY]: id });
}
export async function clearDiscoveredOrg() {
  await chrome.storage.local.remove(ORG_KEY);
}
