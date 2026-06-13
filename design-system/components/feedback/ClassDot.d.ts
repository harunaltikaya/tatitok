import React from "react";

export type EconomicClass = "subscription" | "metered" | "local" | "free";

/**
 * @startingPoint section="Feedback" subtitle="Class dot, accuracy badge, plan meter" viewport="700x170"
 */
export interface ClassDotProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** Economic class hue, a status hue, or any CSS color string.
   *  @default "neutral" */
  tone?: EconomicClass | "positive" | "neutral" | "live" | "danger" | string;
  /** Diameter in px. @default 8 */
  size?: number;
  /** Animate a soft liveness pulse (use for the SSE-connected indicator). */
  pulse?: boolean;
}

/**
 * ClassDot — the small filled circle that encodes the economic class of usage,
 * liveness, or status. Color carries the meaning; there is no glyph.
 */
export function ClassDot(props: ClassDotProps): JSX.Element;
