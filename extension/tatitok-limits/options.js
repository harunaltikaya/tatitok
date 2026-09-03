// options.js — the extension's options page. Two settings: an OPTIONAL
// Claude organization-ID override (rules.claudeOrgId; blank = the service
// worker auto-discovers the org from the logged-in claude.ai session) and the
// tatitok hub URL (rules.hubUrl; loopback origins only, blank = default).
// (The poll-interval and the ChatGPT message-cap rules will join this page in
// a later chunk.)

import {
  getRules,
  RULES_KEY,
  isValidClaudeOrgId,
  isValidHubUrl,
  DEFAULT_HUB_URL,
} from "./storage.js";

const input = document.getElementById("claudeOrgId");
const hubInput = document.getElementById("hubUrl");
const status = document.getElementById("status");

// Load the current values into the fields.
(async () => {
  const rules = await getRules();
  input.value = rules.claudeOrgId ?? "";
  hubInput.value = rules.hubUrl ?? DEFAULT_HUB_URL;
})();

document.getElementById("save").addEventListener("click", async () => {
  const value = input.value.trim();
  // Blank clears the override (re-enables session auto-discovery); a non-blank
  // value must be a UUID-shaped org id before we ever store/use it.
  if (value !== "" && !isValidClaudeOrgId(value)) {
    status.style.color = "#dc2626";
    status.textContent = "Not a valid organization ID (expected a UUID).";
    return;
  }
  // Blank restores the default hub; anything else must be a loopback origin
  // (what host_permissions cover) before it is stored or ever fetched.
  const hub = hubInput.value.trim() || DEFAULT_HUB_URL;
  if (!isValidHubUrl(hub)) {
    status.style.color = "#dc2626";
    status.textContent =
      "Hub URL must be http://127.0.0.1[:port] or http://localhost[:port] (no path).";
    return;
  }
  hubInput.value = hub;
  // Merge so we never clobber the other rules (pollIntervalSeconds, …).
  const rules = await getRules();
  await chrome.storage.local.set({
    [RULES_KEY]: { ...rules, claudeOrgId: value, hubUrl: hub },
  });
  status.style.color = "";
  status.textContent = "Saved.";
  setTimeout(() => {
    status.textContent = "";
  }, 1500);
});
