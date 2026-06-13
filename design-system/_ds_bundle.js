/* @ds-bundle: {"format":3,"namespace":"TatitokDesignSystem_b39a28","components":[{"name":"Button","sourcePath":"components/buttons/Button.jsx"},{"name":"IconButton","sourcePath":"components/buttons/IconButton.jsx"},{"name":"Badge","sourcePath":"components/feedback/Badge.jsx"},{"name":"ClassDot","sourcePath":"components/feedback/ClassDot.jsx"},{"name":"MeterBar","sourcePath":"components/feedback/MeterBar.jsx"},{"name":"Select","sourcePath":"components/forms/Select.jsx"},{"name":"Switch","sourcePath":"components/forms/Switch.jsx"},{"name":"Card","sourcePath":"components/layout/Card.jsx"},{"name":"Stat","sourcePath":"components/layout/Stat.jsx"},{"name":"FilterChip","sourcePath":"components/navigation/FilterChip.jsx"},{"name":"Tabs","sourcePath":"components/navigation/Tabs.jsx"}],"sourceHashes":{"components/buttons/Button.jsx":"9404b8bf268d","components/buttons/IconButton.jsx":"d29fa0f18db8","components/feedback/Badge.jsx":"378f91a7d99e","components/feedback/ClassDot.jsx":"302a5b2ade1c","components/feedback/MeterBar.jsx":"186d84d0a986","components/forms/Select.jsx":"dde2b46d4d1c","components/forms/Switch.jsx":"1a90d205a809","components/layout/Card.jsx":"a0ffdb1e635e","components/layout/Stat.jsx":"c76e755acdb2","components/navigation/FilterChip.jsx":"6310d643823b","components/navigation/Tabs.jsx":"b9d9ba57661a","ui_kits/dashboard/app.jsx":"318a2561bfb3","ui_kits/dashboard/charts.js":"9bde0aec709e","ui_kits/dashboard/data.js":"595e980ae91b","ui_kits/dashboard/parts.jsx":"8fbf6d18daf2","ui_kits/dashboard/screens.jsx":"f507d857ce75"},"inlinedExternals":[],"unexposedExports":[]} */

(() => {

const __ds_ns = (window.TatitokDesignSystem_b39a28 = window.TatitokDesignSystem_b39a28 || {});

const __ds_scope = {};

(__ds_ns.__errors = __ds_ns.__errors || []);

// components/buttons/Button.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.tt-btn{
  display:inline-flex; align-items:center; justify-content:center; gap:8px;
  font-family:var(--font-sans); font-weight:var(--weight-medium); line-height:1;
  border-radius:var(--radius-md); border:0.5px solid transparent;
  cursor:pointer; white-space:nowrap; user-select:none;
  font-variant-numeric:tabular-nums;
  transition:background var(--dur-fast) var(--ease-out),
             border-color var(--dur-fast) var(--ease-out),
             color var(--dur-fast) var(--ease-out);
}
.tt-btn:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-btn--sm{ height:28px; padding:0 10px; font-size:13px; }
.tt-btn--md{ height:34px; padding:0 14px; font-size:14px; }
.tt-btn--default{ background:var(--surface-card); border-color:var(--border-hairline); color:var(--text-primary); }
.tt-btn--default:hover{ background:var(--surface-raised); border-color:var(--border-strong); }
.tt-btn--subtle{ background:transparent; color:var(--text-secondary); }
.tt-btn--subtle:hover{ background:var(--surface-hover); color:var(--text-primary); }
.tt-btn--primary{ background:var(--accent); color:var(--text-on-accent); }
.tt-btn--primary:hover{ filter:brightness(1.06); }
.tt-btn.is-active{ background:var(--accent-soft); border-color:var(--accent-border); color:var(--accent); }
.tt-btn:disabled{ opacity:.45; cursor:not-allowed; }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-btn-css")) {
  const el = document.createElement("style");
  el.id = "tt-btn-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Button — the system's text button. Sentence case labels only.
 */
function Button({
  variant = "default",
  size = "md",
  active = false,
  className = "",
  children,
  ...rest
}) {
  const cls = ["tt-btn", `tt-btn--${size}`, `tt-btn--${variant}`, active ? "is-active" : "", className].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement("button", _extends({
    className: cls
  }, rest), children);
}
Object.assign(__ds_scope, { Button });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/buttons/Button.jsx", error: String((e && e.message) || e) }); }

// components/buttons/IconButton.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.tt-iconbtn{
  display:inline-flex; align-items:center; justify-content:center;
  border-radius:var(--radius-sm); border:0.5px solid transparent;
  background:transparent; color:var(--text-tertiary); cursor:pointer;
  transition:background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.tt-iconbtn:hover{ background:var(--surface-hover); color:var(--text-primary); }
.tt-iconbtn:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-iconbtn--sm{ width:26px; height:26px; font-size:14px; }
.tt-iconbtn--md{ width:32px; height:32px; font-size:16px; }
.tt-iconbtn.is-active{ background:var(--accent-soft); color:var(--accent); }
.tt-iconbtn:disabled{ opacity:.4; cursor:not-allowed; }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-iconbtn-css")) {
  const el = document.createElement("style");
  el.id = "tt-iconbtn-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * IconButton — square, quiet control for panel chrome (resize, fullscreen,
 * close) and toolbar glyphs. Pass a Unicode glyph or an SVG icon as children.
 */
function IconButton({
  size = "md",
  active = false,
  label,
  className = "",
  children,
  ...rest
}) {
  const cls = ["tt-iconbtn", `tt-iconbtn--${size}`, active ? "is-active" : "", className].filter(Boolean).join(" ");
  return /*#__PURE__*/React.createElement("button", _extends({
    className: cls,
    "aria-label": label,
    title: label
  }, rest), children);
}
Object.assign(__ds_scope, { IconButton });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/buttons/IconButton.jsx", error: String((e && e.message) || e) }); }

// components/feedback/Badge.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.tt-badge{
  display:inline-flex; align-items:center; gap:5px;
  height:20px; padding:0 8px; border-radius:var(--radius-pill);
  font-family:var(--font-sans); font-size:11px; font-weight:var(--weight-medium);
  line-height:1; white-space:nowrap; border:0.5px solid transparent;
  font-variant-numeric:tabular-nums;
}
.tt-badge--neutral{ background:var(--surface-inset); border-color:var(--border-hairline); color:var(--text-secondary); }
.tt-badge--exact{ background:var(--class-free-soft); border-color:transparent; color:var(--class-free); }
.tt-badge--derived{ background:var(--class-subscription-soft); border-color:transparent; color:var(--class-subscription); }
.tt-badge--estimated{ background:var(--class-metered-soft); border-color:transparent; color:var(--class-metered); }
.tt-badge--positive{ background:color-mix(in oklab, var(--color-positive) 14%, transparent); color:var(--color-positive); }
.tt-badge--danger{ background:color-mix(in oklab, var(--color-danger) 14%, transparent); color:var(--color-danger); }
.tt-badge--tag{ background:var(--surface-inset); color:var(--text-tertiary); font-variant-numeric:tabular-nums; border-radius:var(--radius-xs); height:18px; padding:0 6px; font-weight:var(--weight-regular); }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-badge-css")) {
  const el = document.createElement("style");
  el.id = "tt-badge-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Badge — small pill. Encodes accuracy class (exact / derived / estimated),
 * cost-basis tags, or status. Sentence case content.
 */
function Badge({
  tone = "neutral",
  dot = false,
  className = "",
  children,
  ...rest
}) {
  const cls = ["tt-badge", `tt-badge--${tone}`, className].filter(Boolean).join(" ");
  const dotColor = {
    exact: "var(--class-free)",
    derived: "var(--class-subscription)",
    estimated: "var(--class-metered)"
  }[tone];
  return /*#__PURE__*/React.createElement("span", _extends({
    className: cls
  }, rest), dot && dotColor && /*#__PURE__*/React.createElement("span", {
    style: {
      width: 5,
      height: 5,
      borderRadius: "999px",
      background: dotColor
    }
  }), children);
}
Object.assign(__ds_scope, { Badge });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/feedback/Badge.jsx", error: String((e && e.message) || e) }); }

// components/feedback/ClassDot.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CLASS_VARS = {
  subscription: "var(--class-subscription)",
  metered: "var(--class-metered)",
  local: "var(--class-local)",
  free: "var(--class-free)",
  positive: "var(--color-positive)",
  neutral: "var(--zinc-500)",
  live: "var(--color-live)",
  danger: "var(--color-danger)"
};

/**
 * ClassDot — the small filled circle that encodes the economic class of usage
 * (subscription / metered / local / free), liveness, or status. The system's
 * most important "icon": color carries the meaning, not a glyph.
 */
function ClassDot({
  tone = "neutral",
  size = 8,
  pulse = false,
  style,
  ...rest
}) {
  const color = CLASS_VARS[tone] || tone;
  return /*#__PURE__*/React.createElement("span", _extends({
    style: {
      display: "inline-block",
      width: size,
      height: size,
      borderRadius: "var(--radius-pill)",
      background: color,
      flex: "0 0 auto",
      boxShadow: pulse ? `0 0 0 0 ${color}` : undefined,
      animation: pulse ? "tt-dot-pulse 1.8s var(--ease-out) infinite" : undefined,
      ...style
    }
  }, rest));
}
if (typeof document !== "undefined" && !document.getElementById("tt-dot-css")) {
  const el = document.createElement("style");
  el.id = "tt-dot-css";
  el.textContent = "@keyframes tt-dot-pulse{0%{box-shadow:0 0 0 0 var(--color-live)}70%{box-shadow:0 0 0 5px transparent}100%{box-shadow:0 0 0 0 transparent}}";
  document.head.appendChild(el);
}
Object.assign(__ds_scope, { ClassDot });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/feedback/ClassDot.jsx", error: String((e && e.message) || e) }); }

// components/feedback/MeterBar.jsx
try { (() => {
/**
 * MeterBar — a thin horizontal progress meter for plan-window usage against a
 * cap. Auto-escalates to amber near the cap and red over it; otherwise renders
 * in the positive green. Flat track, no glow.
 */
function MeterBar({
  value,
  max,
  tone,
  height = 6,
  showLabel = false,
  format,
  style
}) {
  const pct = max > 0 ? Math.min(100, value / max * 100) : 0;
  const auto = pct >= 100 ? "danger" : pct >= 90 ? "warning" : "positive";
  const t = tone || auto;
  const fill = {
    positive: "var(--color-positive)",
    warning: "var(--color-warning)",
    danger: "var(--color-danger)",
    subscription: "var(--class-subscription)",
    metered: "var(--class-metered)",
    local: "var(--class-local)",
    free: "var(--class-free)"
  }[t] || "var(--color-positive)";
  const fmt = format || (v => String(v));
  return /*#__PURE__*/React.createElement("div", {
    style: style
  }, showLabel && /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      justifyContent: "space-between",
      fontSize: "var(--text-xs)",
      color: "var(--text-tertiary)",
      marginBottom: 6,
      fontVariantNumeric: "tabular-nums"
    }
  }, /*#__PURE__*/React.createElement("span", null, fmt(value)), /*#__PURE__*/React.createElement("span", null, fmt(max))), /*#__PURE__*/React.createElement("div", {
    style: {
      height,
      borderRadius: "var(--radius-pill)",
      background: "var(--surface-inset)",
      overflow: "hidden"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      height: "100%",
      width: `${pct}%`,
      background: fill,
      borderRadius: "var(--radius-pill)",
      transition: "width var(--dur-slow) var(--ease-out)"
    }
  })));
}
Object.assign(__ds_scope, { MeterBar });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/feedback/MeterBar.jsx", error: String((e && e.message) || e) }); }

// components/forms/Select.jsx
try { (() => {
const CSS = `
.tt-select{ position:relative; display:inline-flex; align-items:center; }
.tt-select > select{
  appearance:none; -webkit-appearance:none;
  font-family:var(--font-sans); font-size:13px; font-weight:var(--weight-regular);
  color:var(--text-primary); background:var(--surface-card);
  border:0.5px solid var(--border-hairline); border-radius:var(--radius-md);
  height:30px; padding:0 28px 0 10px; cursor:pointer; line-height:1;
  transition:border-color var(--dur-fast) var(--ease-out), background var(--dur-fast);
}
.tt-select > select:hover{ border-color:var(--border-strong); }
.tt-select > select:focus-visible{ outline:2px solid var(--border-focus); outline-offset:1px; }
.tt-select__chev{
  position:absolute; right:9px; pointer-events:none; color:var(--text-tertiary);
  font-size:10px; line-height:1;
}
`;
if (typeof document !== "undefined" && !document.getElementById("tt-select-css")) {
  const el = document.createElement("style");
  el.id = "tt-select-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Select — a thin wrapper over a native <select> with the system's hairline
 * border and a quiet chevron. Used for the timezone picker and group-by.
 */
function Select({
  value,
  onChange,
  options = [],
  "aria-label": ariaLabel,
  className = "",
  style,
  children
}) {
  return /*#__PURE__*/React.createElement("span", {
    className: `tt-select ${className}`,
    style: style
  }, /*#__PURE__*/React.createElement("select", {
    value: value,
    onChange: onChange,
    "aria-label": ariaLabel
  }, children || options.map(o => {
    const opt = typeof o === "string" ? {
      value: o,
      label: o
    } : o;
    return /*#__PURE__*/React.createElement("option", {
      key: opt.value,
      value: opt.value
    }, opt.label);
  })), /*#__PURE__*/React.createElement("span", {
    className: "tt-select__chev",
    "aria-hidden": true
  }, "\u25BE"));
}
Object.assign(__ds_scope, { Select });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Select.jsx", error: String((e && e.message) || e) }); }

// components/forms/Switch.jsx
try { (() => {
const CSS = `
.tt-switch{ display:inline-flex; align-items:center; gap:9px; cursor:pointer; user-select:none; }
.tt-switch input{ position:absolute; opacity:0; width:0; height:0; }
.tt-switch__track{
  position:relative; width:34px; height:20px; border-radius:var(--radius-pill);
  background:var(--surface-inset); border:0.5px solid var(--border-hairline);
  transition:background var(--dur-base) var(--ease-out), border-color var(--dur-base);
  flex:0 0 auto;
}
.tt-switch__thumb{
  position:absolute; top:2px; left:2px; width:15px; height:15px;
  border-radius:var(--radius-pill); background:var(--zinc-400);
  transition:transform var(--dur-base) var(--ease-out), background var(--dur-base);
}
.tt-switch input:checked + .tt-switch__track{ background:var(--accent); border-color:transparent; }
.tt-switch input:checked + .tt-switch__track .tt-switch__thumb{ transform:translateX(14px); background:var(--text-on-accent); }
.tt-switch input:focus-visible + .tt-switch__track{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-switch__label{ font-size:13px; color:var(--text-secondary); white-space:nowrap; }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-switch-css")) {
  const el = document.createElement("style");
  el.id = "tt-switch-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Switch — a compact toggle for binary view options (include estimates,
 * dark/light). On uses the brand-green track.
 */
function Switch({
  checked = false,
  onChange,
  label,
  "aria-label": ariaLabel,
  className = "",
  style
}) {
  return /*#__PURE__*/React.createElement("label", {
    className: `tt-switch ${className}`,
    style: style
  }, /*#__PURE__*/React.createElement("input", {
    type: "checkbox",
    checked: checked,
    onChange: onChange,
    "aria-label": ariaLabel || (typeof label === "string" ? label : undefined)
  }), /*#__PURE__*/React.createElement("span", {
    className: "tt-switch__track"
  }, /*#__PURE__*/React.createElement("span", {
    className: "tt-switch__thumb"
  })), label && /*#__PURE__*/React.createElement("span", {
    className: "tt-switch__label"
  }, label));
}
Object.assign(__ds_scope, { Switch });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/forms/Switch.jsx", error: String((e && e.message) || e) }); }

// components/layout/Card.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
/**
 * Card — the flat surface primitive: solid fill, a single 0.5px hairline
 * border, soft radius, no shadow. Optional sentence-case title with right-
 * aligned meta/actions. The building block for every panel and stat tile.
 */
function Card({
  title,
  actions,
  padding = 16,
  className = "",
  style,
  children,
  ...rest
}) {
  return /*#__PURE__*/React.createElement("section", _extends({
    className: className,
    style: {
      background: "var(--surface-card)",
      border: "0.5px solid var(--border-hairline)",
      borderRadius: "var(--radius-lg)",
      padding,
      ...style
    }
  }, rest), (title || actions) && /*#__PURE__*/React.createElement("header", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: "var(--space-2)",
      marginBottom: "var(--space-3)"
    }
  }, title && /*#__PURE__*/React.createElement("h2", {
    style: {
      margin: 0,
      fontSize: "var(--text-xs)",
      fontWeight: "var(--weight-medium)",
      letterSpacing: "var(--tracking-label)",
      color: "var(--text-tertiary)",
      whiteSpace: "nowrap",
      flex: "0 0 auto"
    }
  }, title), actions && /*#__PURE__*/React.createElement("div", {
    style: {
      marginLeft: "auto",
      flex: "0 0 auto",
      display: "flex",
      alignItems: "center",
      gap: 4
    }
  }, actions)), children);
}
Object.assign(__ds_scope, { Card });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/layout/Card.jsx", error: String((e && e.message) || e) }); }

// components/layout/Stat.jsx
try { (() => {
/**
 * Stat — a quiet label over a large tabular number, with optional sub-line and
 * a trailing honesty marker. The product's primary way of stating a figure;
 * `positive` renders the value in the savings green (use for value-extracted).
 */
function Stat({
  label,
  value,
  sub,
  positive = false,
  size = "md",
  unpriced = false,
  unpricedTitle,
  className = "",
  style
}) {
  const valSize = size === "hero" ? "var(--text-hero)" : size === "lg" ? "var(--text-2xl)" : "var(--text-xl)";
  return /*#__PURE__*/React.createElement("div", {
    className: className,
    style: style
  }, label && /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: "var(--text-xs)",
      fontWeight: "var(--weight-medium)",
      letterSpacing: "var(--tracking-label)",
      color: "var(--text-tertiary)",
      marginBottom: 6
    }
  }, label), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: valSize,
      fontWeight: "var(--weight-regular)",
      lineHeight: "var(--leading-tight)",
      letterSpacing: "var(--tracking-tight)",
      fontVariantNumeric: "tabular-nums",
      color: positive ? "var(--text-positive)" : "var(--text-primary)"
    }
  }, value, unpriced && /*#__PURE__*/React.createElement("span", {
    title: unpricedTitle,
    style: {
      color: "var(--color-warning)",
      cursor: "help",
      marginLeft: 1
    }
  }, "*")), sub && /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 4,
      fontSize: "var(--text-sm)",
      color: "var(--text-tertiary)",
      fontVariantNumeric: "tabular-nums"
    }
  }, sub));
}
Object.assign(__ds_scope, { Stat });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/layout/Stat.jsx", error: String((e && e.message) || e) }); }

// components/navigation/FilterChip.jsx
try { (() => {
function _extends() { return _extends = Object.assign ? Object.assign.bind() : function (n) { for (var e = 1; e < arguments.length; e++) { var t = arguments[e]; for (var r in t) ({}).hasOwnProperty.call(t, r) && (n[r] = t[r]); } return n; }, _extends.apply(null, arguments); }
const CSS = `
.tt-chip{
  display:inline-flex; align-items:center; gap:6px; height:24px; padding:0 6px 0 9px;
  font-family:var(--font-sans); font-size:13px; font-weight:var(--weight-regular);
  color:var(--text-primary); background:var(--accent-soft);
  border:0.5px solid var(--accent-border); border-radius:var(--radius-pill);
  cursor:pointer; white-space:nowrap;
  transition:background var(--dur-fast) var(--ease-out);
}
.tt-chip:hover{ background:color-mix(in oklab, var(--accent) 26%, transparent); }
.tt-chip:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
.tt-chip__dim{ font-size:12px; color:var(--accent); }
.tt-chip__x{ display:inline-flex; color:var(--text-tertiary); font-size:13px; }
.tt-chip:hover .tt-chip__x{ color:var(--text-primary); }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-chip-css")) {
  const el = document.createElement("style");
  el.id = "tt-chip-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * FilterChip — a removable active-facet chip ("provider: anthropic ×"). Click
 * removes the filter. Uses the neutral brand-green accent, not a class hue.
 */
function FilterChip({
  dim,
  value,
  onRemove,
  className = "",
  ...rest
}) {
  return /*#__PURE__*/React.createElement("button", _extends({
    className: `tt-chip ${className}`,
    onClick: onRemove,
    title: dim ? `remove ${dim} filter` : "remove filter"
  }, rest), dim && /*#__PURE__*/React.createElement("span", {
    className: "tt-chip__dim"
  }, dim, ":"), /*#__PURE__*/React.createElement("span", null, value), /*#__PURE__*/React.createElement("span", {
    className: "tt-chip__x",
    "aria-hidden": true
  }, "\xD7"));
}
Object.assign(__ds_scope, { FilterChip });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/navigation/FilterChip.jsx", error: String((e && e.message) || e) }); }

// components/navigation/Tabs.jsx
try { (() => {
const CSS = `
.tt-tabs{ display:inline-flex; align-items:center; gap:2px; }
.tt-tab{
  display:inline-flex; align-items:center; gap:7px; height:32px; padding:0 12px;
  font-family:var(--font-sans); font-size:14px; font-weight:var(--weight-medium);
  color:var(--text-tertiary); background:transparent; border:none; cursor:pointer;
  border-radius:var(--radius-md); line-height:1;
  transition:background var(--dur-fast) var(--ease-out), color var(--dur-fast);
}
.tt-tab:hover{ color:var(--text-primary); background:var(--surface-hover); }
.tt-tab.is-active{ color:var(--text-primary); background:var(--surface-active); }
.tt-tab:focus-visible{ outline:2px solid var(--border-focus); outline-offset:2px; }
`;
if (typeof document !== "undefined" && !document.getElementById("tt-tabs-css")) {
  const el = document.createElement("style");
  el.id = "tt-tabs-css";
  el.textContent = CSS;
  document.head.appendChild(el);
}

/**
 * Tabs — quiet top-level page switcher (overview / explore / live /
 * collectors). Sentence case labels; the active tab uses a faint surface wash,
 * not an underline or accent bar.
 */
function Tabs({
  tabs = [],
  value,
  onChange,
  className = "",
  style
}) {
  return /*#__PURE__*/React.createElement("div", {
    className: `tt-tabs ${className}`,
    role: "tablist",
    style: style
  }, tabs.map(t => {
    const tab = typeof t === "string" ? {
      value: t,
      label: t
    } : t;
    const active = tab.value === value;
    return /*#__PURE__*/React.createElement("button", {
      key: tab.value,
      role: "tab",
      "aria-selected": active,
      className: `tt-tab ${active ? "is-active" : ""}`,
      onClick: () => onChange && onChange(tab.value)
    }, tab.label);
  }));
}
Object.assign(__ds_scope, { Tabs });
})(); } catch (e) { __ds_ns.__errors.push({ path: "components/navigation/Tabs.jsx", error: String((e && e.message) || e) }); }

// ui_kits/dashboard/app.jsx
try { (() => {
/* tatitok dashboard UI kit — app shell + mount. */
const {
  useState: useS
} = React;
const DSx = window.TatitokDesignSystem_b39a28;
const {
  Tabs,
  FilterChip,
  Button
} = DSx;
const {
  Header
} = window.TTParts;
const RailX = window.TTParts.FacetRail;
const {
  Overview,
  Explore,
  Live
} = window.TTScreens;
const TTx = window.TT;
const FACET_DIMS = ["harness", "provider", "model", "accuracy"];
function displayDim(d) {
  return d;
}
function App() {
  const [page, setPage] = useS("overview");
  const [range, setRange] = useS("30d");
  const [tz, setTz] = useS("Europe/Istanbul");
  const [theme, setTheme] = useS("dark");
  const [filters, setFilters] = useS({}); // dim -> [values]

  function onTheme(e) {
    const t = e.target.checked ? "light" : "dark";
    setTheme(t);
    document.documentElement.setAttribute("data-theme", t === "light" ? "light" : "");
  }
  function toggle(dim, value) {
    setFilters(f => {
      const cur = f[dim] || [];
      const next = cur.includes(value) ? cur.filter(v => v !== value) : [...cur, value];
      const out = {
        ...f,
        [dim]: next
      };
      if (next.length === 0) delete out[dim];
      return out;
    });
  }
  const chips = Object.entries(filters).flatMap(([dim, vals]) => vals.map(v => ({
    dim,
    value: v
  })));
  const Screen = page === "overview" ? Overview : page === "explore" ? Explore : page === "live" ? Live : Overview;
  return /*#__PURE__*/React.createElement("div", {
    style: {
      minHeight: "100vh",
      background: "var(--bg-app)",
      color: "var(--text-primary)",
      padding: "20px 24px"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      maxWidth: "var(--content-max)",
      margin: "0 auto"
    }
  }, /*#__PURE__*/React.createElement(Header, {
    range: range,
    onRange: setRange,
    tz: tz,
    onTz: setTz,
    theme: theme,
    onTheme: onTheme
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 12,
      marginBottom: "var(--space-4)",
      flexWrap: "wrap"
    }
  }, /*#__PURE__*/React.createElement(Tabs, {
    value: page,
    onChange: setPage,
    tabs: ["overview", "explore", "live"]
  }), page !== "live" && /*#__PURE__*/React.createElement("span", {
    style: {
      marginLeft: "auto",
      fontSize: 12,
      color: "var(--text-faint)"
    }
  }, "tatitok v0.6.2 \xB7 snapshot 2026-06-10 \xB7 db a3f9c1")), chips.length > 0 && /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 8,
      marginBottom: "var(--space-4)",
      flexWrap: "wrap"
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 12,
      color: "var(--text-tertiary)"
    }
  }, "filters"), chips.map(c => /*#__PURE__*/React.createElement(FilterChip, {
    key: c.dim + c.value,
    dim: c.dim,
    value: c.value,
    onRemove: () => toggle(c.dim, c.value)
  })), /*#__PURE__*/React.createElement(Button, {
    variant: "subtle",
    size: "sm",
    onClick: () => setFilters({})
  }, "clear all")), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      gap: "var(--space-4)",
      alignItems: "flex-start"
    }
  }, /*#__PURE__*/React.createElement(RailX, {
    facets: TTx.facets,
    filters: filters,
    onToggle: toggle
  }), /*#__PURE__*/React.createElement("main", {
    style: {
      flex: 1,
      minWidth: 0
    }
  }, /*#__PURE__*/React.createElement(Screen, {
    filters: filters,
    onToggle: toggle
  })))));
}
ReactDOM.createRoot(document.getElementById("root")).render(/*#__PURE__*/React.createElement(App, null));
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/dashboard/app.jsx", error: String((e && e.message) || e) }); }

// ui_kits/dashboard/charts.js
try { (() => {
/* tatitok dashboard UI kit — ECharts option builders (window.TTCharts).
   Calm, flat charts: transparent backgrounds, gray axes, class-colored
   series, no decorative gradients. */
(function () {
  const {
    CLASS,
    CHART,
    days,
    classKeys,
    heat,
    models
  } = window.TT;
  const axisText = {
    color: CHART.axis,
    fontSize: 11,
    fontFamily: "Jost, sans-serif"
  };
  const tooltip = {
    backgroundColor: "#161618",
    borderColor: "rgba(255,255,255,0.09)",
    borderWidth: 0.5,
    textStyle: {
      color: "#e4e4e7",
      fontSize: 12,
      fontFamily: "Jost, sans-serif"
    },
    padding: [8, 11]
  };

  // Donut — API-equivalent value by economic class.
  function donutByClass() {
    const sums = {};
    for (const c of classKeys) sums[c] = 0;
    for (const d of days) for (const c of classKeys) sums[c] += d.byClass[c].equiv;
    const data = classKeys.filter(c => sums[c] > 0).map(c => ({
      name: CLASS[c].label,
      value: Math.round(sums[c]),
      itemStyle: {
        color: CLASS[c].color
      }
    }));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: {
        ...tooltip,
        trigger: "item",
        valueFormatter: v => "$" + v.toLocaleString()
      },
      series: [{
        type: "pie",
        radius: ["62%", "86%"],
        center: ["50%", "50%"],
        avoidLabelOverlap: false,
        padAngle: 2,
        itemStyle: {
          borderRadius: 4,
          borderColor: "#161618",
          borderWidth: 2
        },
        label: {
          show: false
        },
        labelLine: {
          show: false
        },
        emphasis: {
          scale: true,
          scaleSize: 4
        },
        data
      }]
    };
  }

  // Stacked daily bars by class. metric: 'tokens' | 'equiv' | 'actual'.
  function dailyStacked(metric) {
    const dates = days.map(d => d.date.slice(5));
    const fmt = metric === "tokens" ? v => window.TT.compactTokens(v) : v => "$" + (v >= 1000 ? (v / 1000).toFixed(1) + "k" : v.toFixed(0));
    return {
      backgroundColor: "transparent",
      animation: false,
      grid: {
        left: 46,
        right: 10,
        top: 14,
        bottom: 22
      },
      tooltip: {
        ...tooltip,
        trigger: "axis",
        axisPointer: {
          type: "shadow",
          shadowStyle: {
            color: "rgba(255,255,255,0.04)"
          }
        },
        valueFormatter: v => fmt(v)
      },
      xAxis: {
        type: "category",
        data: dates,
        axisLabel: {
          ...axisText,
          interval: 4
        },
        axisLine: {
          lineStyle: {
            color: CHART.split
          }
        },
        axisTick: {
          show: false
        }
      },
      yAxis: {
        type: "value",
        axisLabel: {
          ...axisText,
          formatter: v => fmt(v)
        },
        splitLine: {
          lineStyle: {
            color: CHART.split
          }
        }
      },
      series: classKeys.map(c => ({
        name: CLASS[c].label,
        type: "bar",
        stack: "t",
        itemStyle: {
          color: CLASS[c].color
        },
        emphasis: {
          focus: "series"
        },
        barWidth: "62%",
        data: days.map(d => Math.round(metric === "tokens" ? d.byClass[c].tokens : d.byClass[c][metric]))
      }))
    };
  }

  // Hour × weekday heatmap — when you work.
  function activityHeatmap() {
    const dows = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
    const data = heat.map(([h, w, v]) => [h, (w + 6) % 7, v]); // shift so Mon=0
    const max = Math.max(...heat.map(x => x[2]));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: {
        ...tooltip,
        formatter: p => `${dows[p.value[1]]} ${String(p.value[0]).padStart(2, "0")}:00 · <b>${p.value[2].toFixed(2)}</b>`
      },
      grid: {
        left: 38,
        right: 12,
        top: 10,
        bottom: 26
      },
      xAxis: {
        type: "category",
        data: Array.from({
          length: 24
        }, (_, i) => i),
        axisLabel: {
          ...axisText,
          interval: 2,
          formatter: v => v + "h"
        },
        axisLine: {
          show: false
        },
        axisTick: {
          show: false
        },
        splitArea: {
          show: false
        }
      },
      yAxis: {
        type: "category",
        data: dows,
        axisLabel: axisText,
        axisLine: {
          show: false
        },
        axisTick: {
          show: false
        }
      },
      visualMap: {
        min: 0,
        max,
        show: false,
        inRange: {
          color: ["#161618", "#173a30", "#1c9d74", "#34d399"]
        }
      },
      series: [{
        type: "heatmap",
        data,
        itemStyle: {
          borderColor: "#09090b",
          borderWidth: 2,
          borderRadius: 3
        },
        emphasis: {
          itemStyle: {
            borderColor: "#52525b"
          }
        }
      }]
    };
  }

  // Treemap — API-equivalent value by model, colored by class.
  function valueTreemap() {
    const data = models.map(m => ({
      name: m.key,
      value: Math.round(m.equiv),
      itemStyle: {
        color: CLASS[m.cls].color
      }
    }));
    return {
      backgroundColor: "transparent",
      animation: false,
      tooltip: {
        ...tooltip,
        formatter: p => `${p.name}<br/><b>$${p.value.toLocaleString()}</b> API-equiv`
      },
      series: [{
        type: "treemap",
        roam: false,
        nodeClick: false,
        breadcrumb: {
          show: false
        },
        width: "100%",
        height: "100%",
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        itemStyle: {
          borderColor: "#09090b",
          borderWidth: 3,
          gapWidth: 3,
          borderRadius: 4
        },
        label: {
          show: true,
          fontFamily: "Jost, sans-serif",
          fontSize: 13,
          color: "#09090b",
          formatter: p => `{n|${p.name}}\n{v|$${p.value.toLocaleString()}}`,
          rich: {
            n: {
              fontSize: 13,
              fontWeight: 500,
              color: "rgba(9,9,11,0.9)",
              lineHeight: 18
            },
            v: {
              fontSize: 12,
              color: "rgba(9,9,11,0.66)",
              lineHeight: 16,
              fontFeatureSettings: "tnum"
            }
          }
        },
        data
      }]
    };
  }
  window.TTCharts = {
    donutByClass,
    dailyStacked,
    activityHeatmap,
    valueTreemap
  };
})();
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/dashboard/charts.js", error: String((e && e.message) || e) }); }

// ui_kits/dashboard/data.js
try { (() => {
/* tatitok dashboard UI kit — mock data + format helpers (window.TT).
   Numbers mirror the product's real shape: plan-included usage carries a
   large API-equivalent value; metered API is the actual out-of-pocket spend;
   local is self-hosted; free is free. Deterministic so the view is stable. */
(function () {
  // ---- number format (one format everywhere) ----------------------------
  function usd(v) {
    if (v !== 0 && Math.abs(v) < 0.01) return "$" + v.toFixed(4).replace(/0+$/, "");
    return "$" + v.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2
    });
  }
  function compactTokens(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(Math.round(n));
  }

  // ---- the economic classes (the only categorical hues) -----------------
  const CLASS = {
    subscription: {
      label: "subscription",
      color: "#a78bfa"
    },
    metered: {
      label: "metered API",
      color: "#f5b547"
    },
    local: {
      label: "local",
      color: "#2dd4bf"
    },
    free: {
      label: "free",
      color: "#4ade80"
    }
  };
  const CHART = {
    axis: "#71717a",
    split: "#27272a",
    positive: "#34d399",
    grid: "#3f3f46"
  };

  // ---- providers / harnesses / models, each tagged with a class ---------
  const providers = [{
    key: "anthropic",
    cls: "subscription",
    events: 184213
  }, {
    key: "openai",
    cls: "metered",
    events: 52840
  }, {
    key: "deepseek",
    cls: "metered",
    events: 38110
  }, {
    key: "local-vllm",
    cls: "local",
    events: 96420
  }, {
    key: "local-ollama",
    cls: "local",
    events: 21755
  }];
  const harnesses = [{
    key: "claude-code",
    cls: "subscription",
    events: 142880
  }, {
    key: "codex",
    cls: "metered",
    events: 61240
  }, {
    key: "opencode",
    cls: "metered",
    events: 40930
  }, {
    key: "web-claude",
    cls: "subscription",
    events: 33110
  }, {
    key: "vllm-proxy",
    cls: "local",
    events: 118200
  }];
  const models = [{
    key: "claude-fable-5",
    cls: "subscription",
    basis: "plan_included",
    equiv: 2618.40,
    actual: 0,
    tokens: 612_400_000
  }, {
    key: "claude-haiku-4.5",
    cls: "subscription",
    basis: "plan_included",
    equiv: 712.18,
    actual: 0,
    tokens: 188_900_000
  }, {
    key: "gpt-5-codex",
    cls: "metered",
    basis: "api_price",
    equiv: 548.92,
    actual: 548.92,
    tokens: 96_300_000
  }, {
    key: "deepseek-v3.2",
    cls: "metered",
    basis: "api_price",
    equiv: 359.57,
    actual: 359.57,
    tokens: 141_460_000
  }, {
    key: "qwen-3.5-35b-a3b",
    cls: "local",
    basis: "local_energy",
    equiv: 681.30,
    actual: 12.04,
    tokens: 121_800_000
  }, {
    key: "llama-4-scout",
    cls: "local",
    basis: "local_energy",
    equiv: 214.66,
    actual: 4.91,
    tokens: 38_700_000
  }];

  // ---- 30 days of daily-by-class series (tokens + equiv + actual) -------
  function rng(seed) {
    let s = seed;
    return () => (s = s * 1103515245 + 12345 & 0x7fffffff) / 0x7fffffff;
  }
  const r = rng(42);
  const days = [];
  const classKeys = ["subscription", "metered", "local", "free"];
  const weights = {
    subscription: 0.52,
    metered: 0.22,
    local: 0.24,
    free: 0.02
  };
  for (let i = 29; i >= 0; i--) {
    const d = new Date(Date.UTC(2026, 5, 13) - i * 86400000);
    const iso = d.toISOString().slice(0, 10);
    const dow = d.getUTCDay();
    const workday = dow >= 1 && dow <= 5 ? 1 : 0.34;
    const base = (0.6 + r() * 0.8) * workday;
    const row = {
      date: iso,
      byClass: {}
    };
    for (const c of classKeys) {
      const tok = base * weights[c] * (28e6 + r() * 22e6);
      const equivPerTok = c === "subscription" ? 4.4e-6 : c === "metered" ? 3.7e-6 : c === "local" ? 5.0e-6 : 0;
      const actualPerTok = c === "metered" ? 3.7e-6 : c === "local" ? 0.09e-6 : 0;
      row.byClass[c] = {
        tokens: tok,
        equiv: tok * equivPerTok,
        actual: tok * actualPerTok
      };
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
  const plans = [{
    name: "Claude Max 20×",
    windowLabel: "5h",
    monthlyPrice: 200,
    monthEquiv: 3330.58,
    weekEquiv: 842.10,
    weeklyCap: 1100,
    windowEquiv: 41.20,
    windowTokens: 9_240_000,
    windowEvents: 612,
    resetsIn: "2h 14m"
  }, {
    name: "ChatGPT Pro",
    windowLabel: "weekly",
    monthlyPrice: 200,
    monthEquiv: 548.92,
    weekEquiv: 131.40,
    weeklyCap: null,
    windowEquiv: null,
    windowTokens: 0,
    windowEvents: 0,
    resetsIn: null
  }];

  // ---- facets (rail) -----------------------------------------------------
  const facets = {
    harness: harnesses,
    provider: providers,
    model: models.map(m => ({
      key: m.key,
      cls: m.cls,
      events: Math.round(m.tokens / 9000)
    })),
    accuracy: [{
      key: "exact",
      cls: null,
      events: 376540
    }, {
      key: "derived",
      cls: null,
      events: 18230
    }, {
      key: "estimated",
      cls: null,
      events: 33110
    }]
  };
  const totals = {
    rangeEquiv: 4182.55,
    rangeActual: 908.49,
    rangeTokens: 1_182_960_000,
    activeDays: 30,
    todayActual: 27.41,
    todayTokens: 6_810_000,
    todayEquiv: 214.90,
    todayUnpriced: 3,
    valueExtracted: 4182.55,
    monthlyPlanOutlay: 920,
    ratio: 4.5
  };
  window.TT = {
    usd,
    compactTokens,
    CLASS,
    CHART,
    providers,
    harnesses,
    models,
    days,
    heat,
    plans,
    facets,
    totals,
    classKeys
  };
})();
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/dashboard/data.js", error: String((e && e.message) || e) }); }

// ui_kits/dashboard/parts.jsx
try { (() => {
/* tatitok dashboard UI kit — shared parts (window.TTParts).
   Composes the design-system primitives; never re-implements them. */
const {
  useEffect,
  useRef,
  useState
} = React;
const DS = window.TatitokDesignSystem_b39a28;
const {
  Button,
  IconButton,
  Card,
  Stat,
  Badge,
  ClassDot,
  MeterBar,
  Select,
  Switch,
  Tabs,
  FilterChip
} = DS;
const {
  usd,
  compactTokens,
  CLASS,
  classKeys
} = window.TT;

// ---- ECharts wrapper (ResizeObserver-driven init, per the real app) -------
function EChart({
  option,
  height = 240
}) {
  const el = React.useRef(null);
  const chart = React.useRef(null);
  const latest = React.useRef(option);
  latest.current = option;
  React.useEffect(() => {
    const node = el.current;
    if (!node) return;
    let timer = 0,
      tries = 0;
    const tryInit = () => {
      if (chart.current) return;
      tries++;
      if (node.clientWidth === 0 || node.clientHeight === 0) {
        if (tries < 60) timer = setTimeout(tryInit, 50);
        return;
      }
      chart.current = window.echarts.init(node, null, {
        renderer: "svg"
      });
      chart.current.setOption(latest.current, {
        notMerge: true
      });
    };
    tryInit();
    const ro = new ResizeObserver(() => {
      if (!chart.current) tryInit();else chart.current.resize();
    });
    ro.observe(node);
    return () => {
      clearTimeout(timer);
      ro.disconnect();
      chart.current && chart.current.dispose();
      chart.current = null;
    };
  }, []);
  React.useEffect(() => {
    if (chart.current) chart.current.setOption(option, {
      notMerge: true
    });
  }, [option]);
  return /*#__PURE__*/React.createElement("div", {
    ref: el,
    style: {
      height,
      width: "100%"
    }
  });
}

// ---- brand mark (inline so currentColor themes) ---------------------------
function Mark({
  size = 26
}) {
  return /*#__PURE__*/React.createElement("svg", {
    width: size,
    height: size,
    viewBox: "0 0 120 120",
    fill: "none",
    "aria-hidden": "true",
    style: {
      display: "block"
    }
  }, /*#__PURE__*/React.createElement("g", {
    stroke: "currentColor",
    strokeWidth: "12",
    strokeLinecap: "round",
    strokeLinejoin: "round"
  }, /*#__PURE__*/React.createElement("path", {
    d: "M50 16 H18 V104 H50"
  }), /*#__PURE__*/React.createElement("path", {
    d: "M70 16 H102 V104 H70"
  })), /*#__PURE__*/React.createElement("circle", {
    cx: "60",
    cy: "60",
    r: "14",
    fill: "#1c9d74"
  }));
}

// ---- class legend ---------------------------------------------------------
function ClassLegend({
  active,
  onToggle
}) {
  return /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      gap: 16,
      flexWrap: "wrap"
    }
  }, classKeys.map(c => /*#__PURE__*/React.createElement("button", {
    key: c,
    onClick: () => onToggle && onToggle(c),
    style: {
      display: "inline-flex",
      alignItems: "center",
      gap: 7,
      background: "none",
      border: "none",
      cursor: onToggle ? "pointer" : "default",
      padding: 0,
      fontFamily: "var(--font-sans)",
      fontSize: 13,
      color: active && !active.includes(c) ? "var(--text-faint)" : "var(--text-secondary)"
    }
  }, /*#__PURE__*/React.createElement(ClassDot, {
    tone: c
  }), " ", CLASS[c].label)));
}

// ---- header ---------------------------------------------------------------
const PRESETS = ["7d", "30d", "90d", "all"];
function Header({
  range,
  onRange,
  tz,
  onTz,
  theme,
  onTheme,
  live
}) {
  return /*#__PURE__*/React.createElement("header", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: "var(--space-4)",
      flexWrap: "wrap",
      marginBottom: "var(--space-5)"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 11
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-primary)"
    }
  }, /*#__PURE__*/React.createElement(Mark, {
    size: 26
  })), /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 21,
      fontWeight: 500,
      letterSpacing: "-0.01em",
      color: "var(--text-primary)"
    }
  }, "tatitok"), /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 13,
      color: "var(--text-faint)"
    }
  }, "local AI usage")), /*#__PURE__*/React.createElement("div", {
    style: {
      marginLeft: "auto",
      display: "flex",
      alignItems: "center",
      gap: 8,
      flexWrap: "wrap"
    }
  }, PRESETS.map(p => /*#__PURE__*/React.createElement(Button, {
    key: p,
    size: "sm",
    active: range === p,
    onClick: () => onRange(p)
  }, p)), /*#__PURE__*/React.createElement("span", {
    style: {
      display: "inline-flex",
      alignItems: "center",
      gap: 6,
      marginLeft: 4
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 12,
      color: "var(--text-tertiary)"
    }
  }, "days in"), /*#__PURE__*/React.createElement(Select, {
    value: tz,
    onChange: e => onTz(e.target.value),
    options: ["UTC", "Europe/Istanbul", "America/New_York", "Asia/Tokyo"],
    "aria-label": "timezone"
  })), /*#__PURE__*/React.createElement(Switch, {
    checked: theme === "light",
    onChange: onTheme,
    "aria-label": "light mode"
  })));
}

// ---- facet rail -----------------------------------------------------------
function FacetSection({
  dim,
  values,
  active,
  onToggle
}) {
  return /*#__PURE__*/React.createElement(Card, {
    padding: 12,
    style: {
      borderRadius: "var(--radius-md)"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 12,
      fontWeight: 500,
      color: "var(--text-tertiary)",
      marginBottom: 8
    }
  }, dim), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: 1
    }
  }, values.map(v => {
    const sel = active.includes(v.key);
    return /*#__PURE__*/React.createElement("button", {
      key: v.key,
      onClick: () => onToggle(dim, v.key),
      style: {
        display: "flex",
        alignItems: "center",
        gap: 7,
        width: "100%",
        textAlign: "left",
        padding: "3px 7px",
        borderRadius: "var(--radius-sm)",
        border: "none",
        cursor: "pointer",
        fontFamily: "var(--font-sans)",
        fontSize: 13,
        background: sel ? "var(--accent-soft)" : "transparent",
        color: sel ? "var(--accent)" : "var(--text-secondary)"
      },
      onMouseEnter: e => {
        if (!sel) e.currentTarget.style.background = "var(--surface-hover)";
      },
      onMouseLeave: e => {
        if (!sel) e.currentTarget.style.background = "transparent";
      }
    }, v.cls ? /*#__PURE__*/React.createElement(ClassDot, {
      tone: v.cls,
      size: 7
    }) : /*#__PURE__*/React.createElement("span", {
      style: {
        width: 7
      }
    }), /*#__PURE__*/React.createElement("span", {
      style: {
        flex: 1,
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap"
      }
    }, v.key), /*#__PURE__*/React.createElement("span", {
      style: {
        fontSize: 11,
        color: "var(--text-faint)",
        fontVariantNumeric: "tabular-nums"
      }
    }, compactTokens(v.events)));
  })));
}
function FacetRail({
  facets,
  filters,
  onToggle
}) {
  return /*#__PURE__*/React.createElement("aside", {
    style: {
      width: "var(--rail-width)",
      flex: "0 0 auto",
      display: "flex",
      flexDirection: "column",
      gap: "var(--space-3)"
    }
  }, Object.keys(facets).map(dim => /*#__PURE__*/React.createElement(FacetSection, {
    key: dim,
    dim: dim,
    values: facets[dim],
    active: filters[dim] || [],
    onToggle: onToggle
  })));
}

// ---- breakdown table ------------------------------------------------------
function Breakdown({
  rows,
  active,
  onSelect
}) {
  return /*#__PURE__*/React.createElement("table", {
    style: {
      width: "100%",
      borderCollapse: "collapse",
      fontSize: 14
    }
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", {
    style: {
      textAlign: "left",
      color: "var(--text-tertiary)",
      fontSize: 12
    }
  }, /*#__PURE__*/React.createElement("th", {
    style: {
      fontWeight: 400,
      padding: "0 0 8px"
    }
  }, "key"), /*#__PURE__*/React.createElement("th", {
    style: {
      fontWeight: 400,
      textAlign: "right",
      padding: "0 0 8px"
    }
  }, "tokens"), /*#__PURE__*/React.createElement("th", {
    style: {
      fontWeight: 400,
      textAlign: "right",
      padding: "0 0 8px"
    }
  }, "API-equiv"), /*#__PURE__*/React.createElement("th", {
    style: {
      fontWeight: 400,
      textAlign: "right",
      padding: "0 0 8px"
    }
  }, "actual"))), /*#__PURE__*/React.createElement("tbody", null, rows.map(m => {
    const sel = active.includes(m.key);
    return /*#__PURE__*/React.createElement("tr", {
      key: m.key,
      onClick: () => onSelect(m.key),
      style: {
        cursor: "pointer",
        borderTop: "0.5px solid var(--border-hairline)",
        background: sel ? "var(--accent-soft)" : "transparent"
      },
      onMouseEnter: e => {
        if (!sel) e.currentTarget.style.background = "var(--surface-hover)";
      },
      onMouseLeave: e => {
        if (!sel) e.currentTarget.style.background = "transparent";
      }
    }, /*#__PURE__*/React.createElement("td", {
      style: {
        padding: "9px 0"
      }
    }, /*#__PURE__*/React.createElement("span", {
      style: {
        display: "inline-flex",
        alignItems: "center",
        gap: 8
      }
    }, /*#__PURE__*/React.createElement(ClassDot, {
      tone: m.cls,
      size: 7
    }), /*#__PURE__*/React.createElement("span", {
      style: {
        color: "var(--text-primary)"
      }
    }, m.key), /*#__PURE__*/React.createElement(Badge, {
      tone: "tag"
    }, m.basis))), /*#__PURE__*/React.createElement("td", {
      style: {
        textAlign: "right",
        color: "var(--text-secondary)",
        fontVariantNumeric: "tabular-nums"
      }
    }, compactTokens(m.tokens)), /*#__PURE__*/React.createElement("td", {
      style: {
        textAlign: "right",
        color: "var(--text-primary)",
        fontVariantNumeric: "tabular-nums"
      }
    }, usd(m.equiv)), /*#__PURE__*/React.createElement("td", {
      style: {
        textAlign: "right",
        color: m.actual === 0 ? "var(--text-faint)" : "var(--text-secondary)",
        fontVariantNumeric: "tabular-nums"
      }
    }, m.actual === 0 ? "—" : usd(m.actual)));
  })));
}

// ---- plan card ------------------------------------------------------------
function PlanCard({
  plan
}) {
  return /*#__PURE__*/React.createElement(Card, {
    title: /*#__PURE__*/React.createElement("span", null, plan.name, " ", /*#__PURE__*/React.createElement("span", {
      style: {
        color: "var(--text-faint)",
        fontWeight: 400
      }
    }, "\xB7 ", plan.windowLabel, " windows"))
  }, plan.windowEquiv !== null ? /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(Stat, {
    value: usd(plan.windowEquiv),
    sub: `${compactTokens(plan.windowTokens)} tokens · ${plan.windowEvents} events`,
    size: "lg"
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 6,
      fontSize: 12,
      color: "var(--text-faint)"
    }
  }, "resets in ", plan.resetsIn)) : /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 14,
      color: "var(--text-tertiary)",
      padding: "6px 0"
    }
  }, "no active window \u2014 the next event opens one"), plan.weeklyCap && /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 14
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      justifyContent: "space-between",
      fontSize: 12,
      color: "var(--text-tertiary)",
      marginBottom: 7,
      fontVariantNumeric: "tabular-nums"
    }
  }, /*#__PURE__*/React.createElement("span", null, "week"), /*#__PURE__*/React.createElement("span", null, usd(plan.weekEquiv), " of ", usd(plan.weeklyCap), " cap")), /*#__PURE__*/React.createElement(MeterBar, {
    value: plan.weekEquiv,
    max: plan.weeklyCap
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 14,
      paddingTop: 12,
      borderTop: "0.5px solid var(--border-hairline)",
      fontSize: 14,
      fontVariantNumeric: "tabular-nums",
      display: "flex",
      alignItems: "baseline",
      gap: 6,
      flexWrap: "wrap"
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-positive)",
      fontWeight: 500
    }
  }, usd(plan.monthEquiv)), /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-tertiary)"
    }
  }, "extracted vs"), /*#__PURE__*/React.createElement("span", {
    style: {
      color: "var(--text-primary)"
    }
  }, usd(plan.monthlyPrice), "/mo"), /*#__PURE__*/React.createElement(Badge, {
    tone: "positive"
  }, (plan.monthEquiv / plan.monthlyPrice).toFixed(1), "\xD7")));
}
window.TTParts = {
  EChart,
  Mark,
  ClassLegend,
  Header,
  FacetRail,
  Breakdown,
  PlanCard
};
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/dashboard/parts.jsx", error: String((e && e.message) || e) }); }

// ui_kits/dashboard/screens.jsx
try { (() => {
/* tatitok dashboard UI kit — screens (window.TTScreens). */
const {
  useState,
  useRef,
  useEffect
} = React;
const {
  Card: C2,
  Stat: S2,
  Badge: B2,
  ClassDot: CD2,
  Select: Sel2,
  Tabs: T2
} = window.TatitokDesignSystem_b39a28;
const {
  EChart,
  ClassLegend,
  FacetRail: Rail,
  Breakdown,
  PlanCard
} = window.TTParts;
const TT = window.TT;
const TTC = window.TTCharts;

// ---- Overview -------------------------------------------------------------
function Overview({
  filters,
  onToggle
}) {
  const t = TT.totals;
  const [donut] = useState(() => TTC.donutByClass());
  const [equivChart] = useState(() => TTC.dailyStacked("equiv"));
  const [tokenChart] = useState(() => TTC.dailyStacked("tokens"));
  const modelFilter = filters.model || [];
  return /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1.35fr 1fr 1.75fr",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement(C2, {
    title: "value extracted"
  }, /*#__PURE__*/React.createElement(S2, {
    value: TT.usd(t.valueExtracted),
    positive: true,
    size: "hero",
    sub: `${t.ratio}× your ${TT.usd(t.monthlyPlanOutlay)}/mo in plans`
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 10,
      fontSize: 12,
      color: "var(--text-faint)"
    }
  }, "API-equivalent value of plan-included usage")), /*#__PURE__*/React.createElement(C2, {
    title: /*#__PURE__*/React.createElement("span", {
      style: {
        display: "inline-flex",
        alignItems: "center",
        gap: 7
      }
    }, "today ", /*#__PURE__*/React.createElement(CD2, {
      tone: "live",
      size: 8,
      pulse: true
    }))
  }, /*#__PURE__*/React.createElement(S2, {
    value: TT.usd(t.todayActual),
    size: "lg",
    unpriced: true,
    unpricedTitle: `${t.todayUnpriced} events today carry no resolvable price — cost is a floor.`,
    sub: `${TT.compactTokens(t.todayTokens)} tokens`
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 8,
      fontSize: 12,
      color: "var(--text-tertiary)",
      fontVariantNumeric: "tabular-nums"
    }
  }, "\u2248 ", TT.usd(t.todayEquiv), " API-equiv")), /*#__PURE__*/React.createElement(C2, {
    title: "range totals \xB7 last 30 days"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1fr 1fr 1fr 1fr",
      gap: "var(--space-4) var(--space-5)",
      marginTop: 2
    }
  }, /*#__PURE__*/React.createElement(S2, {
    value: TT.usd(t.rangeEquiv),
    sub: "API-equivalent"
  }), /*#__PURE__*/React.createElement(S2, {
    value: TT.usd(t.rangeActual),
    sub: "actual cost"
  }), /*#__PURE__*/React.createElement(S2, {
    value: TT.compactTokens(t.rangeTokens),
    sub: "tokens"
  }), /*#__PURE__*/React.createElement(S2, {
    value: String(t.activeDays),
    sub: "active days"
  })))), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1fr 1fr",
      gap: "var(--space-4)"
    }
  }, TT.plans.map(p => /*#__PURE__*/React.createElement(PlanCard, {
    key: p.name,
    plan: p
  }))), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1.75fr 1fr",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement(C2, {
    title: "daily API-equivalent value",
    actions: /*#__PURE__*/React.createElement(ClassLegend, {
      active: null
    })
  }, /*#__PURE__*/React.createElement(EChart, {
    option: equivChart,
    height: 236
  })), /*#__PURE__*/React.createElement(C2, {
    title: "value by class"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      position: "relative"
    }
  }, /*#__PURE__*/React.createElement(EChart, {
    option: donut,
    height: 188
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      position: "absolute",
      inset: 0,
      display: "flex",
      flexDirection: "column",
      alignItems: "center",
      justifyContent: "center",
      pointerEvents: "none"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 24,
      fontWeight: 400,
      color: "var(--text-primary)",
      letterSpacing: "-0.02em",
      fontVariantNumeric: "tabular-nums"
    }
  }, TT.usd(t.rangeEquiv)), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 11,
      color: "var(--text-tertiary)"
    }
  }, "total value"))), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 12
    }
  }, /*#__PURE__*/React.createElement(ClassLegend, {
    active: null
  })))), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1.75fr 1fr",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement(C2, {
    title: "usage by model",
    actions: /*#__PURE__*/React.createElement("span", {
      style: {
        fontSize: 12,
        color: "var(--text-faint)"
      }
    }, "click a row to filter")
  }, /*#__PURE__*/React.createElement(Breakdown, {
    rows: TT.models,
    active: modelFilter,
    onSelect: k => onToggle("model", k)
  })), /*#__PURE__*/React.createElement(C2, {
    title: "daily tokens"
  }, /*#__PURE__*/React.createElement(EChart, {
    option: tokenChart,
    height: 236
  }))));
}

// ---- Explore --------------------------------------------------------------
function Explore({
  filters,
  onToggle
}) {
  const [by, setBy] = useState("model");
  const [heat] = useState(() => TTC.activityHeatmap());
  const [tree] = useState(() => TTC.valueTreemap());
  const dimRows = {
    model: TT.models,
    provider: TT.providers.map(p => ({
      key: p.key,
      cls: p.cls,
      basis: p.cls === "subscription" ? "plan_included" : p.cls === "metered" ? "api_price" : "local_energy",
      tokens: p.events * 9000,
      equiv: p.events * 0.012,
      actual: p.cls === "metered" ? p.events * 0.011 : p.events * 0.0002
    })),
    harness: TT.harnesses.map(h => ({
      key: h.key,
      cls: h.cls,
      basis: h.cls === "subscription" ? "plan_included" : h.cls === "metered" ? "api_price" : "local_energy",
      tokens: h.events * 9000,
      equiv: h.events * 0.013,
      actual: h.cls === "metered" ? h.events * 0.012 : h.events * 0.0002
    }))
  };
  const rows = dimRows[by];
  return /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      alignItems: "center",
      gap: 10
    }
  }, /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 13,
      color: "var(--text-tertiary)"
    }
  }, "primary dimension"), /*#__PURE__*/React.createElement(Sel2, {
    value: by,
    onChange: e => setBy(e.target.value),
    options: ["model", "provider", "harness"],
    "aria-label": "group by"
  }), /*#__PURE__*/React.createElement("span", {
    style: {
      marginLeft: "auto",
      fontSize: 12,
      color: "var(--text-faint)"
    }
  }, "export current slice \xB7 CSV / JSON")), /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1.4fr 1fr",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement(C2, {
    title: `by ${by}`
  }, /*#__PURE__*/React.createElement(Breakdown, {
    rows: rows,
    active: filters[by] || [],
    onSelect: k => onToggle(by, k)
  })), /*#__PURE__*/React.createElement(C2, {
    title: "when you work \xB7 hour \xD7 weekday"
  }, /*#__PURE__*/React.createElement(EChart, {
    option: heat,
    height: 232
  }))), /*#__PURE__*/React.createElement(C2, {
    title: "where the value concentrates \xB7 API-equivalent by model"
  }, /*#__PURE__*/React.createElement(EChart, {
    option: tree,
    height: 240
  })));
}

// ---- Live -----------------------------------------------------------------
const SAMPLE_EVENTS = [{
  harness: "claude-code",
  model: "claude-fable-5",
  cls: "subscription",
  tok: 18420,
  cost: 0
}, {
  harness: "codex",
  model: "gpt-5-codex",
  cls: "metered",
  tok: 9260,
  cost: 0.41
}, {
  harness: "vllm-proxy",
  model: "qwen-3.5-35b-a3b",
  cls: "local",
  tok: 24110,
  cost: 0.002
}, {
  harness: "opencode",
  model: "deepseek-v3.2",
  cls: "metered",
  tok: 6180,
  cost: 0.18
}, {
  harness: "claude-code",
  model: "claude-haiku-4.5",
  cls: "subscription",
  tok: 4120,
  cost: 0
}, {
  harness: "web-claude",
  model: "claude-fable-5",
  cls: "subscription",
  tok: 7740,
  cost: 0
}];
function Live() {
  const [feed, setFeed] = useState(() => Array.from({
    length: 7
  }, (_, i) => ({
    ...SAMPLE_EVENTS[i % SAMPLE_EVENTS.length],
    id: i,
    t: new Date(Date.now() - i * 4200)
  })));
  const idRef = useRef(100);
  useEffect(() => {
    const iv = setInterval(() => {
      const e = SAMPLE_EVENTS[Math.floor(Math.random() * SAMPLE_EVENTS.length)];
      setFeed(f => [{
        ...e,
        id: idRef.current++,
        t: new Date()
      }, ...f].slice(0, 12));
    }, 2600);
    return () => clearInterval(iv);
  }, []);
  const hhmmss = d => d.toTimeString().slice(0, 8);
  return /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "grid",
      gridTemplateColumns: "1fr 1fr 1fr",
      gap: "var(--space-4)"
    }
  }, /*#__PURE__*/React.createElement(C2, {
    title: /*#__PURE__*/React.createElement("span", {
      style: {
        display: "inline-flex",
        alignItems: "center",
        gap: 7
      }
    }, "burn rate ", /*#__PURE__*/React.createElement(CD2, {
      tone: "live",
      size: 8,
      pulse: true
    }))
  }, /*#__PURE__*/React.createElement(S2, {
    value: "1.42M",
    sub: "tokens / min \xB7 5-min EMA",
    size: "lg"
  })), /*#__PURE__*/React.createElement(C2, {
    title: "spend rate"
  }, /*#__PURE__*/React.createElement(S2, {
    value: "$3.18",
    sub: "per hour \xB7 actual",
    size: "lg"
  })), /*#__PURE__*/React.createElement(C2, {
    title: "plan-window projection"
  }, /*#__PURE__*/React.createElement(S2, {
    value: "17:42",
    sub: "Claude Max limit at this rate",
    size: "lg"
  }))), /*#__PURE__*/React.createElement(C2, {
    title: "live event stream",
    actions: /*#__PURE__*/React.createElement("span", {
      style: {
        fontSize: 12,
        color: "var(--text-faint)"
      }
    }, "last ", feed.length, " events")
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: "flex",
      flexDirection: "column"
    }
  }, feed.map(e => /*#__PURE__*/React.createElement("div", {
    key: e.id,
    style: {
      display: "grid",
      gridTemplateColumns: "84px 1fr 1fr 92px 78px",
      alignItems: "center",
      gap: 10,
      padding: "8px 0",
      borderTop: "0.5px solid var(--border-hairline)",
      fontSize: 13,
      fontVariantNumeric: "tabular-nums"
    }
  }, /*#__PURE__*/React.createElement("span", {
    className: "mono",
    style: {
      color: "var(--text-faint)",
      fontSize: 12
    }
  }, hhmmss(e.t)), /*#__PURE__*/React.createElement("span", {
    style: {
      display: "inline-flex",
      alignItems: "center",
      gap: 7,
      color: "var(--text-secondary)"
    }
  }, /*#__PURE__*/React.createElement(CD2, {
    tone: e.cls,
    size: 7
  }), e.harness), /*#__PURE__*/React.createElement("span", {
    className: "mono",
    style: {
      color: "var(--text-tertiary)",
      fontSize: 12
    }
  }, e.model), /*#__PURE__*/React.createElement("span", {
    style: {
      textAlign: "right",
      color: "var(--text-secondary)"
    }
  }, TT.compactTokens(e.tok)), /*#__PURE__*/React.createElement("span", {
    style: {
      textAlign: "right",
      color: e.cost === 0 ? "var(--text-faint)" : "var(--text-primary)"
    }
  }, e.cost === 0 ? "plan" : TT.usd(e.cost)))))));
}
window.TTScreens = {
  Overview,
  Explore,
  Live
};
})(); } catch (e) { __ds_ns.__errors.push({ path: "ui_kits/dashboard/screens.jsx", error: String((e && e.message) || e) }); }

__ds_ns.Button = __ds_scope.Button;

__ds_ns.IconButton = __ds_scope.IconButton;

__ds_ns.Badge = __ds_scope.Badge;

__ds_ns.ClassDot = __ds_scope.ClassDot;

__ds_ns.MeterBar = __ds_scope.MeterBar;

__ds_ns.Select = __ds_scope.Select;

__ds_ns.Switch = __ds_scope.Switch;

__ds_ns.Card = __ds_scope.Card;

__ds_ns.Stat = __ds_scope.Stat;

__ds_ns.FilterChip = __ds_scope.FilterChip;

__ds_ns.Tabs = __ds_scope.Tabs;

})();
