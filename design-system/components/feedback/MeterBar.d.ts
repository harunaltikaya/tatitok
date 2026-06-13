import React from "react";

export interface MeterBarProps {
  /** Current usage. */
  value: number;
  /** Cap / maximum. */
  max: number;
  /** Force a fill color; omit to auto-escalate positive → amber (≥90%) → red (≥100%). */
  tone?: "positive" | "warning" | "danger" | "subscription" | "metered" | "local" | "free";
  /** Track height in px. @default 6 */
  height?: number;
  /** Render value/max labels above the track. */
  showLabel?: boolean;
  /** Formatter for the labels (e.g. the `usd` helper). */
  format?: (v: number) => string;
  style?: React.CSSProperties;
}

/**
 * MeterBar — thin plan-window usage meter against a cap. Flat track, no glow.
 */
export function MeterBar(props: MeterBarProps): JSX.Element;
