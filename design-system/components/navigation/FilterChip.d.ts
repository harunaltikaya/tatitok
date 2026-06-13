import React from "react";

export interface FilterChipProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Facet dimension label (provider, harness, model…). */
  dim?: string;
  /** The active value. */
  value: React.ReactNode;
  /** Called when the chip is clicked (removes the filter). */
  onRemove?: () => void;
}

/**
 * FilterChip — removable active-facet chip. Uses the neutral brand-green
 * accent, never a class hue.
 */
export function FilterChip(props: FilterChipProps): JSX.Element;
