import React from "react";

/**
 * @startingPoint section="Buttons" subtitle="Text button — default, subtle, primary, active" viewport="700x150"
 */
export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Visual weight. `default` = hairline-bordered surface, `subtle` = ghost,
   *  `primary` = brand-green fill (use sparingly — one primary action). */
  variant?: "default" | "subtle" | "primary";
  /** Control height. @default "md" */
  size?: "sm" | "md";
  /** Toggled/selected state — renders the low-key brand-green active style.
   *  Used for range presets (7d / 30d / 90d / all). */
  active?: boolean;
  children?: React.ReactNode;
}

/**
 * Button — the system's text button. Labels are sentence case, never ALL-CAPS.
 */
export function Button(props: ButtonProps): JSX.Element;
