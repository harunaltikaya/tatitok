// sw.js — tatitok-limits MV3 service worker.
//
// Polls the two REPORTED-limit providers (Claude, Codex) on a gentle,
// dashboard-gated chrome.alarms timer, normalizes their windows into
// chrome.storage.local, and feeds the whole snapshot to the tatitok hub's
// display-only ingest endpoint over loopback. tatitok renders the limits in its
// own dashboard cards from that feed — the old corner overlay has been retired,
// so the extension no longer injects anything into the page. The Claude org id
// is discovered PER-USER from the logged-in session (never hardcoded), with an
// optional override in the options page. NOT here yet: the ChatGPT message counter.

import {
  DEFAULT_RULES,
  RULES_KEY,
  getRules,
  getLimits,
  seedDefaults,
  setProviderLimit,
  getDiscoveredOrg,
  setDiscoveredOrg,
  clearDiscoveredOrg,
} from "./storage.js";

// ---- constants --------------------------------------------------------

const ALARM_NAME = "poll-limits";
const DASHBOARD_URL_PREFIX = "http://127.0.0.1:8284";

// The hub's chunk-1 display-only ingest endpoint. Derived from the dashboard
// prefix so the host:port has one source of truth.
// TODO(addr): make configurable if the owner runs the hub on a custom --addr
// (the host_permission + DASHBOARD_URL_PREFIX would move together then).
const INGEST_URL = `${DASHBOARD_URL_PREFIX}/api/v1/limits`;

// Gentle, rate-friendly band; rules.pollIntervalSeconds is clamped into it.
const MIN_INTERVAL_S = 60;
const MAX_INTERVAL_S = 120;

// Codex bearer token, cached for this service-worker lifetime. Re-fetched
// from the session when missing (e.g. after a SW restart) or when a usage
// call returns 401 (expired).
let codexToken = null;

// ---- lifecycle / scheduling ------------------------------------------

chrome.runtime.onInstalled.addListener(async () => {
  await seedDefaults();
  await rescheduleAlarm();
});

// The SW can be torn down and respawned without onInstalled firing; alarms
// persist on their own, but re-assert on browser startup to be safe.
chrome.runtime.onStartup.addListener(() => {
  rescheduleAlarm();
});

// If the poll interval changes under us, reschedule.
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "local" && changes[RULES_KEY]) rescheduleAlarm();
});

async function rescheduleAlarm() {
  const { pollIntervalSeconds } = await getRules();
  const secs = clampInterval(pollIntervalSeconds);
  await chrome.alarms.clear(ALARM_NAME);
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: secs / 60 });
  console.log(`tatitok-limits: alarm every ${secs}s`);
}

function clampInterval(s) {
  const n = Number(s);
  if (!Number.isFinite(n)) return DEFAULT_RULES.pollIntervalSeconds;
  return Math.min(MAX_INTERVAL_S, Math.max(MIN_INTERVAL_S, n));
}

// ---- triggers ---------------------------------------------------------

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name !== ALARM_NAME) return;
  if (!(await dashboardOpen())) {
    console.log("dashboard closed, skipping.");
    return;
  }
  await pollAll();
});

// Manual "poll now" from the toolbar button — bypasses the dashboard gate
// for testing convenience.
chrome.action.onClicked.addListener(async () => {
  console.log("tatitok-limits: manual poll now (gate bypassed)");
  await pollAll();
});

// ---- dashboard gate ---------------------------------------------------

// True if any open tab is the local tatitok dashboard. tab.url is visible for
// that tab because http://127.0.0.1:8284/* is in host_permissions (no "tabs"
// permission needed); tabs we lack host access to report url === undefined
// and are simply skipped.
async function dashboardOpen() {
  const tabs = await chrome.tabs.query({});
  return tabs.some(
    (t) => typeof t.url === "string" && t.url.startsWith(DASHBOARD_URL_PREFIX),
  );
}

// ---- poll orchestration ----------------------------------------------

async function pollAll() {
  // Independent: one provider failing must not affect the other.
  await Promise.allSettled([pollClaude(), pollCodex()]);
  const limits = await getLimits();
  console.log("tatitok-limits: storage.limits =", limits);
  // Chunk 2: feed the hub with the whole snapshot (display-only).
  await postSnapshotToHub(limits);
}

// postSnapshotToHub sends the WHOLE current snapshot to the hub in ONE request.
// The hub's store is last-write-wins (it REPLACES the snapshot, never merges),
// so two per-provider POSTs would clobber each other and drop a provider —
// always send both providers together. Providers still null (never successfully
// polled) are omitted; a present provider carries its last-good value (storage
// only ever holds successes, so a transient poll failure can't blank it).
//
// The body already matches the hub's chunk-1 JSON contract verbatim: the stored
// Provider/Window shape ({ fetchedAt, windows: [{ label, usedPercent, resetAt }] },
// resetAt + fetchedAt in epoch-ms) is exactly what the Go Snapshot decodes, so
// no field translation is needed. No clamping either — the hub accepts
// usedPercent > 100 and treats resetAt 0 as "unknown".
//
// Best-effort and isolated: a hub that's down or answers non-2xx is warned and
// swallowed in its own try/catch, so it can NEVER break the poll cycle. The next
// cycle re-sends the latest, so a missed POST self-heals (last-write-wins).
async function postSnapshotToHub(limits) {
  const snapshot = {};
  for (const [provider, snap] of Object.entries(limits)) {
    if (snap != null) snapshot[provider] = snap; // omit null (never-polled) providers
  }
  if (Object.keys(snapshot).length === 0) return; // nothing polled yet — nothing to send

  try {
    const res = await fetch(INGEST_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(snapshot),
    });
    if (!res.ok) {
      console.warn(`tatitok-limits: hub ingest POST → ${res.status} ${res.statusText} (continuing)`);
      return;
    }
    console.log(`tatitok-limits: hub ingest POST ok (${res.status}) [${Object.keys(snapshot).join(", ")}]`);
  } catch (err) {
    console.warn("tatitok-limits: hub ingest POST failed (hub down?) — continuing:", err);
  }
}

// ---- Claude (cookie session) -----------------------------------------
//
// The Claude org id is PER-USER and never hardcoded. Resolution order:
//   1. owner override — rules.claudeOrgId, set in the options page (wins);
//   2. cached discovery — chrome.storage "claudeOrg" from a previous run;
//   3. discover now — read it from the logged-in session (same cookie auth as
//      the usage call) and cache it.
// With none of those, the Claude poll is SKIPPED (logged), never crashed.

const CLAUDE_HEADERS = {
  "Anthropic-Client-Platform": "web_claude_ai",
  "Anthropic-Client-Version": "1.0.0",
};

function fetchClaudeUsage(orgId) {
  return fetch(`https://claude.ai/api/organizations/${orgId}/usage`, {
    credentials: "include",
    headers: CLAUDE_HEADERS,
  });
}

async function getClaudeOrgId() {
  const { claudeOrgId } = await getRules();
  if (claudeOrgId) return claudeOrgId.trim(); // owner override wins
  const cached = await getDiscoveredOrg();
  if (cached) return cached;
  const discovered = await discoverClaudeOrgId();
  if (discovered) await setDiscoveredOrg(discovered);
  return discovered; // may be null → caller skips the poll
}

// Discover the user's Claude org from the logged-in session. /api/organizations
// returns the orgs the cookie can see (an array, or { organizations: [...] }).
// Pick deterministically: the sole org, else the first whose usage endpoint
// answers 200 (the one that actually carries limit data). null when none usable.
async function discoverClaudeOrgId() {
  const res = await fetch("https://claude.ai/api/organizations", {
    credentials: "include",
    headers: CLAUDE_HEADERS,
  });
  if (!res.ok) throw new Error(`org list HTTP ${res.status} ${res.statusText}`);
  const body = await res.json();
  const list = Array.isArray(body)
    ? body
    : Array.isArray(body && body.organizations)
      ? body.organizations
      : [];
  const ids = list
    .map((o) => o && (o.uuid || o.id))
    .filter((id) => typeof id === "string" && id.length > 0);
  if (ids.length === 0) return null;
  if (ids.length === 1) return ids[0];
  for (const id of ids) {
    try {
      if ((await fetchClaudeUsage(id)).ok) return id; // first org with usage data
    } catch {
      /* try the next org */
    }
  }
  return null;
}

async function pollClaude() {
  try {
    const orgId = await getClaudeOrgId();
    if (!orgId) {
      console.warn(
        "CLAUDE: no organization id (none discovered, none set in options) — " +
          "skipping. Set your Claude org ID in the extension options if this persists.",
      );
      return;
    }
    const res = await fetchClaudeUsage(orgId);
    if (!res.ok) {
      // A cached/discovered org that 403/404s is stale or wrong — drop the cache
      // so the next cycle re-discovers (an owner override is left untouched).
      if (res.status === 403 || res.status === 404) await clearDiscoveredOrg();
      throw new Error(`HTTP ${res.status} ${res.statusText}`);
    }
    const data = await res.json();
    const windows = normalizeClaude(data);
    if (windows.length === 0) console.warn("CLAUDE: no buckets matched; raw:", data);
    await setProviderLimit("claude", { fetchedAt: Date.now(), windows });
    console.log("CLAUDE ok:", windows);
  } catch (err) {
    console.error("CLAUDE poll failed (keeping last snapshot):", err);
  }
}

// Read EVERY non-null bucket shaped { utilization, resets_at } — five_hour,
// seven_day, seven_day_sonnet, and whatever else the account exposes. Don't
// hardcode the set: iterate over present, non-null buckets and label by key.
const CLAUDE_LABELS = {
  five_hour: "5h",
  seven_day: "7d",
  seven_day_sonnet: "Sonnet 7d",
  seven_day_opus: "Opus 7d",
};

function normalizeClaude(data) {
  const windows = [];
  if (data && typeof data === "object") {
    for (const [key, bucket] of Object.entries(data)) {
      if (
        bucket &&
        typeof bucket === "object" &&
        bucket.utilization != null &&
        bucket.resets_at != null
      ) {
        windows.push({
          label: CLAUDE_LABELS[key] ?? key,
          usedPercent: Number(bucket.utilization),
          resetAt: Date.parse(bucket.resets_at), // ISO 8601 → epoch ms
        });
      }
    }
  }
  return windows;
}

// ---- Codex (bearer token, two steps) ---------------------------------

async function pollCodex() {
  try {
    let token = await getCodexToken();
    let res = await fetchCodexUsage(token);
    if (res.status === 401) {
      console.log("CODEX: usage 401 — refreshing session token");
      token = await getCodexToken(true);
      res = await fetchCodexUsage(token);
    }
    if (!res.ok) throw new Error(`usage HTTP ${res.status} ${res.statusText}`);
    const data = await res.json();
    const windows = normalizeCodex(data);
    if (windows.length === 0) console.warn("CODEX: no windows matched; raw:", data);
    await setProviderLimit("codex", { fetchedAt: Date.now(), windows });
    console.log("CODEX ok:", windows);
  } catch (err) {
    console.error("CODEX poll failed (keeping last snapshot):", err);
  }
}

// Cached bearer token; fetch the session only when missing or forced (401).
async function getCodexToken(forceRefresh = false) {
  if (codexToken && !forceRefresh) return codexToken;
  const res = await fetch("https://chatgpt.com/api/auth/session", {
    credentials: "include",
  });
  if (!res.ok) throw new Error(`session HTTP ${res.status} ${res.statusText}`);
  const session = await res.json();
  const token = session && session.accessToken;
  if (!token) throw new Error("no accessToken in session (logged out?)");
  codexToken = token;
  return token;
}

function fetchCodexUsage(token) {
  return fetch("https://chatgpt.com/backend-api/wham/usage", {
    headers: { Authorization: "Bearer " + token },
  });
}

// primary_window → "5h", secondary_window → "Weekly". reset_at is Unix
// SECONDS here (Claude sent an ISO string); both normalize to epoch ms.
function normalizeCodex(data) {
  const windows = [];
  const rl = data && data.rate_limit;
  if (rl) {
    pushCodexWindow(windows, "5h", rl.primary_window);
    pushCodexWindow(windows, "Weekly", rl.secondary_window);
  }
  return windows;
}

function pushCodexWindow(windows, label, w) {
  if (w && w.used_percent != null && w.reset_at != null) {
    windows.push({
      label,
      usedPercent: Number(w.used_percent),
      resetAt: Number(w.reset_at) * 1000, // Unix seconds → epoch ms
    });
  }
}
