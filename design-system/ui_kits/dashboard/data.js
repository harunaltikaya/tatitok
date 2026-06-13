/* tatitok dashboard UI kit — mock data + format helpers (window.TT).
   Numbers mirror the product's real shape: plan-included usage carries a
   large API-equivalent value; metered API is the actual out-of-pocket spend;
   local is self-hosted; free is free. Deterministic so the view is stable. */
(function () {
  // ---- number format (one format everywhere) ----------------------------
  function usd(v) {
    if (v !== 0 && Math.abs(v) < 0.01) return "$" + v.toFixed(4).replace(/0+$/, "");
    return "$" + v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  }
  function compactTokens(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(Math.round(n));
  }

  // ---- the economic classes (the only categorical hues) -----------------
  const CLASS = {
    subscription: { label: "subscription", color: "#a78bfa" },
    metered:      { label: "metered API",  color: "#f5b547" },
    local:        { label: "local",        color: "#2dd4bf" },
    free:         { label: "free",         color: "#4ade80" },
  };
  const CHART = { axis: "#71717a", split: "#27272a", positive: "#34d399", grid: "#3f3f46" };

  // ---- providers / harnesses / models, each tagged with a class ---------
  const providers = [
    { key: "anthropic",     cls: "subscription", events: 184213 },
    { key: "openai",        cls: "metered",      events: 52840 },
    { key: "deepseek",      cls: "metered",      events: 38110 },
    { key: "local-vllm",    cls: "local",        events: 96420 },
    { key: "local-ollama",  cls: "local",        events: 21755 },
  ];
  const harnesses = [
    { key: "claude-code", cls: "subscription", events: 142880 },
    { key: "codex",       cls: "metered",      events: 61240 },
    { key: "opencode",    cls: "metered",      events: 40930 },
    { key: "web-claude",  cls: "subscription", events: 33110 },
    { key: "vllm-proxy",  cls: "local",        events: 118200 },
  ];
  const models = [
    { key: "claude-fable-5",    cls: "subscription", basis: "plan_included", equiv: 2618.40, actual: 0,       tokens: 612_400_000 },
    { key: "claude-haiku-4.5",  cls: "subscription", basis: "plan_included", equiv: 712.18,  actual: 0,       tokens: 188_900_000 },
    { key: "gpt-5-codex",       cls: "metered",      basis: "api_price",     equiv: 548.92,  actual: 548.92,  tokens: 96_300_000 },
    { key: "deepseek-v3.2",     cls: "metered",      basis: "api_price",     equiv: 359.57,  actual: 359.57,  tokens: 141_460_000 },
    { key: "qwen-3.5-35b-a3b",  cls: "local",        basis: "local_energy",  equiv: 681.30,  actual: 12.04,   tokens: 121_800_000 },
    { key: "llama-4-scout",     cls: "local",        basis: "local_energy",  equiv: 214.66,  actual: 4.91,    tokens: 38_700_000 },
  ];

  // ---- 30 days of daily-by-class series (tokens + equiv + actual) -------
  function rng(seed) { let s = seed; return () => (s = (s * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff; }
  const r = rng(42);
  const days = [];
  const classKeys = ["subscription", "metered", "local", "free"];
  const weights = { subscription: 0.52, metered: 0.22, local: 0.24, free: 0.02 };
  for (let i = 29; i >= 0; i--) {
    const d = new Date(Date.UTC(2026, 5, 13) - i * 86400000);
    const iso = d.toISOString().slice(0, 10);
    const dow = d.getUTCDay();
    const workday = dow >= 1 && dow <= 5 ? 1 : 0.34;
    const base = (0.6 + r() * 0.8) * workday;
    const row = { date: iso, byClass: {} };
    for (const c of classKeys) {
      const tok = base * weights[c] * (28e6 + r() * 22e6);
      const equivPerTok = c === "subscription" ? 4.4e-6 : c === "metered" ? 3.7e-6 : c === "local" ? 5.0e-6 : 0;
      const actualPerTok = c === "metered" ? 3.7e-6 : c === "local" ? 0.09e-6 : 0;
      row.byClass[c] = { tokens: tok, equiv: tok * equivPerTok, actual: tok * actualPerTok };
    }
    days.push(row);
  }

  // ---- hour × weekday activity (heatmap) --------------------------------
  const heat = [];
  const r2 = rng(7);
  for (let h = 0; h < 24; h++) for (let w = 0; w < 7; w++) {
    const work = w < 5 ? 1 : 0.3;
    const peak = Math.exp(-Math.pow(h - 15, 2) / 36) + 0.35 * Math.exp(-Math.pow(h - 22, 2) / 18);
    const v = h >= 7 && h <= 23 ? peak * work * (0.6 + r2() * 0.7) : 0.02 * r2();
    heat.push([h, w, Math.round(v * 100) / 100]);
  }

  // ---- plan windows ------------------------------------------------------
  const plans = [
    { name: "Claude Max 20×", windowLabel: "5h", monthlyPrice: 200, monthEquiv: 3330.58, weekEquiv: 842.10, weeklyCap: 1100, windowEquiv: 41.20, windowTokens: 9_240_000, windowEvents: 612, resetsIn: "2h 14m" },
    { name: "ChatGPT Pro",    windowLabel: "weekly", monthlyPrice: 200, monthEquiv: 548.92, weekEquiv: 131.40, weeklyCap: null, windowEquiv: null, windowTokens: 0, windowEvents: 0, resetsIn: null },
  ];

  // ---- facets (rail) -----------------------------------------------------
  const facets = {
    harness: harnesses, provider: providers,
    model: models.map((m) => ({ key: m.key, cls: m.cls, events: Math.round(m.tokens / 9000) })),
    accuracy: [
      { key: "exact", cls: null, events: 376540 },
      { key: "derived", cls: null, events: 18230 },
      { key: "estimated", cls: null, events: 33110 },
    ],
  };

  const totals = {
    rangeEquiv: 4182.55, rangeActual: 908.49, rangeTokens: 1_182_960_000, activeDays: 30,
    todayActual: 27.41, todayTokens: 6_810_000, todayEquiv: 214.90, todayUnpriced: 3,
    valueExtracted: 4182.55, monthlyPlanOutlay: 920, ratio: 4.5,
  };

  window.TT = { usd, compactTokens, CLASS, CHART, providers, harnesses, models, days, heat, plans, facets, totals, classKeys };
})();
