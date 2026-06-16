import { useState } from "react";
import {
  applyOnboard,
  type OnboardCard,
  type OnboardDetect,
  type OnboardApplyResult,
} from "../api";
import {
  type CardState,
  initialCardState,
  selectTier,
  setMetered,
  setPrice,
  allReady,
  applyPayload,
  tierLabel,
  tierProvenance,
  windowsHint,
} from "../onboarding";
import Card from "../ui/Card";
import Button from "../ui/Button";

// Onboarding (Stage 2): the confirm panel that drives the same /api/onboard
// logic for friends. Honesty is structural — Claude's tier is never
// pre-selected, an ambiguous Codex Pro ($100/$200) forces a choice, prices are
// disclosed list defaults shown as editable, and "metered" writes no plan.
// Non-blocking overlay (skippable, re-openable from the header "plans" button).
export default function Onboarding({
  detect,
  onApplied,
  onClose,
}: {
  detect: OnboardDetect;
  onApplied: (r: OnboardApplyResult) => void;
  onClose: () => void;
}) {
  const [states, setStates] = useState<CardState[]>(() => detect.cards.map(initialCardState));
  const [applying, setApplying] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const update = (i: number, next: CardState) =>
    setStates((prev) => prev.map((s, j) => (j === i ? next : s)));

  const apply = async () => {
    setApplying(true);
    setErr(null);
    try {
      const r = await applyOnboard(applyPayload(states));
      onApplied(r); // parent refetches + closes
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setApplying(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/50 p-4 sm:p-8"
      role="dialog"
      aria-modal="true"
      aria-label="Set up your subscription plans"
    >
      <Card
        className="w-full max-w-2xl"
        title="set up your plans"
        actions={
          <Button size="sm" variant="subtle" onClick={onClose} disabled={applying}>
            close
          </Button>
        }
      >
        <p className="mb-4 text-sm text-secondary">
          Tell tatitok your subscription so plan-covered usage bills $0 and shows its
          API-equivalent value. Prices are published list defaults — edit if your bill differs.
        </p>

        <div className="space-y-3">
          {detect.cards.map((card, i) => (
            <CardRow
              key={card.provider_arg}
              card={card}
              state={states[i]}
              onChange={(s) => update(i, s)}
              disabled={applying}
            />
          ))}
        </div>

        {err && (
          <div
            className="mt-4 rounded-[8px] border-[0.5px] p-2 text-xs"
            style={{ borderColor: "var(--color-danger)", color: "var(--color-danger)" }}
          >
            {err}
          </div>
        )}

        <div className="mt-5 flex items-center justify-between">
          <span className="text-[11px] text-faint">tier prices: {detect.snapshot_version}</span>
          <div className="flex gap-2">
            <Button variant="subtle" onClick={onClose} disabled={applying}>
              skip for now
            </Button>
            <Button variant="primary" onClick={apply} disabled={applying || !allReady(states)}>
              {applying ? "applying + repricing…" : "apply"}
            </Button>
          </div>
        </div>
      </Card>
    </div>
  );
}

function CardRow({
  card,
  state,
  onChange,
  disabled,
}: {
  card: OnboardCard;
  state: CardState;
  onChange: (s: CardState) => void;
  disabled: boolean;
}) {
  const detLabel =
    card.provider_arg === "codex"
      ? card.ambiguous
        ? "Codex Pro detected — pick $100 or $200"
        : card.detected_tier
          ? `detected: ${tierLabel(card.detected_tier)}`
          : "tier not detected"
      : "choose your plan — not auto-detectable";
  const hint = windowsHint(card);

  return (
    <div className="rounded-[10px] border-[0.5px] border-hairline bg-inset p-3">
      <div className="mb-2 flex items-baseline justify-between">
        <span className="text-sm text-primary">{card.plan_name}</span>
        <span className="text-[11px] text-faint">
          {card.provider} · {detLabel}
        </span>
      </div>

      <div className="flex flex-wrap gap-1.5">
        {card.tiers.map((t) => {
          const active = !state.metered && state.tier === t.tier;
          const detected =
            card.provider_arg === "codex" && !card.ambiguous && t.tier !== "" && t.tier === card.detected_tier;
          return (
            <Button
              key={t.tier}
              size="sm"
              variant={active ? "primary" : "default"}
              active={active}
              disabled={disabled}
              title={tierProvenance(card, t.tier)}
              onClick={() => onChange(selectTier(card, state, t.tier))}
            >
              {tierLabel(t.tier)}
              {t.price_usd === "0" ? "" : ` · $${t.price_usd}`}
              {detected ? " ✓" : ""}
            </Button>
          );
        })}
        <Button
          size="sm"
          variant={state.metered ? "primary" : "default"}
          active={state.metered}
          disabled={disabled}
          title="I pay per token / API key — writes no plan; this harness stays api_price"
          onClick={() => onChange(setMetered({ ...state, tier: "" }, true))}
        >
          metered
        </Button>
      </div>

      <div className="mt-2 text-[11px] text-tertiary">
        {state.metered
          ? "metered — no plan written; this harness stays api_price (billed at list rates)"
          : state.tier
            ? tierProvenance(card, state.tier)
            : "pick a tier, or choose metered"}
        {hint ? ` · ${hint}` : ""}
      </div>

      {!state.metered && state.tier && state.tier !== "free" && (
        <label className="mt-2 flex flex-wrap items-center gap-2 text-xs text-tertiary">
          <span>monthly price</span>
          <span className="text-faint">$</span>
          <input
            type="text"
            inputMode="decimal"
            value={state.price}
            disabled={disabled}
            onChange={(e) => onChange(setPrice(state, e.target.value))}
            aria-label={`${card.plan_name} monthly price in USD`}
            className="w-20 rounded-[6px] border-[0.5px] border-hairline bg-app px-2 py-1 text-right text-primary tabular-nums outline-none"
          />
          <span className="text-faint">/mo · published list price — edit if your bill differs (tax, annual, promos)</span>
        </label>
      )}
    </div>
  );
}
