import React from "react";

export interface SwitchProps {
  checked?: boolean;
  onChange?: (e: React.ChangeEvent<HTMLInputElement>) => void;
  /** Inline label text/node. */
  label?: React.ReactNode;
  "aria-label"?: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Switch — compact toggle for binary view options (include estimates,
 * dark/light). On = brand-green track.
 */
export function Switch(props: SwitchProps): JSX.Element;
