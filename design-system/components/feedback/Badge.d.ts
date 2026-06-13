import React from "react";

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** `exact`/`derived`/`estimated` map to the accuracy palette; `tag` is a
   *  square cost-basis chip; `positive`/`danger`/`neutral` are status tones. */
  tone?: "neutral" | "exact" | "derived" | "estimated" | "positive" | "danger" | "tag";
  /** Show a leading class dot (only for exact/derived/estimated). */
  dot?: boolean;
  children?: React.ReactNode;
}

/**
 * Badge — small pill encoding accuracy class, cost-basis tags, or status.
 */
export function Badge(props: BadgeProps): JSX.Element;
