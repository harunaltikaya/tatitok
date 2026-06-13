import React from "react";

export interface IconButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** @default "md" */
  size?: "sm" | "md";
  /** Toggled/selected state (brand-green active wash). */
  active?: boolean;
  /** Accessible name — also used as the tooltip title. */
  label?: string;
  /** A Unicode glyph (⤢ ✕ − +) or an SVG icon node. */
  children?: React.ReactNode;
}

/**
 * IconButton — square, quiet control for panel chrome and toolbar glyphs.
 */
export function IconButton(props: IconButtonProps): JSX.Element;
