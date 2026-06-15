// options.js — the extension's options page. Currently a single setting: an
// OPTIONAL Claude organization-ID override (rules.claudeOrgId). Blank = the
// service worker auto-discovers the org from the logged-in claude.ai session;
// set it only to override discovery. (The poll-interval and the ChatGPT
// message-cap rules will join this page in a later chunk.)

import { getRules, RULES_KEY } from "./storage.js";

const input = document.getElementById("claudeOrgId");
const status = document.getElementById("status");

// Load the current override into the field.
(async () => {
  const rules = await getRules();
  input.value = rules.claudeOrgId ?? "";
})();

document.getElementById("save").addEventListener("click", async () => {
  // Merge so we never clobber the other rules (pollIntervalSeconds, …).
  const rules = await getRules();
  await chrome.storage.local.set({
    [RULES_KEY]: { ...rules, claudeOrgId: input.value.trim() },
  });
  status.textContent = "Saved.";
  setTimeout(() => {
    status.textContent = "";
  }, 1500);
});
