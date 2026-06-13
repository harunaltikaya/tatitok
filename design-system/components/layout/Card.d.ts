import React from "react";

/**
 * @startingPoint section="Layout" subtitle="Flat card surface + Stat number" viewport="700x180"
 */
export interface CardProps extends React.HTMLAttributes<HTMLElement> {
  /** Optional sentence-case label rendered as the card header. */
  title?: React.ReactNode;
  /** Right-aligned header content (IconButtons, badges, meta). */
  actions?: React.ReactNode;
  /** Inner padding in px. @default 16 */
  padding?: number;
  children?: React.ReactNode;
}

/**
 * Card — the flat surface primitive: solid fill, one 0.5px hairline border,
 * soft radius, no shadow.
 */
export function Card(props: CardProps): JSX.Element;
