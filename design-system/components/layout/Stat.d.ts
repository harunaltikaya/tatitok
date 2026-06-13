import React from "react";

export interface StatProps {
  /** Quiet sentence-case label above the number. */
  label?: React.ReactNode;
  /** The figure — pre-formatted with the system's number format. */
  value?: React.ReactNode;
  /** Secondary line under the number (e.g. "141.46M tokens"). */
  sub?: React.ReactNode;
  /** Render the value in the savings green — use for value-extracted. */
  positive?: boolean;
  /** Number scale. @default "md" */
  size?: "md" | "lg" | "hero";
  /** Append the unpriced-floor marker (*). */
  unpriced?: boolean;
  /** Tooltip for the marker. */
  unpricedTitle?: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Stat — a quiet label over a large tabular number with an optional sub-line
 * and honesty marker. `positive` colors the value the savings green.
 */
export function Stat(props: StatProps): JSX.Element;
