import React from "react";

export interface SelectOption {
  value: string;
  label: string;
}

/**
 * @startingPoint section="Forms" subtitle="Select + Switch controls" viewport="700x150"
 */
export interface SelectProps {
  value?: string;
  onChange?: (e: React.ChangeEvent<HTMLSelectElement>) => void;
  /** Options as strings or {value,label}. Ignored if children are passed. */
  options?: (string | SelectOption)[];
  "aria-label"?: string;
  className?: string;
  style?: React.CSSProperties;
  children?: React.ReactNode;
}

/**
 * Select — a native <select> with the system's hairline border and a quiet
 * chevron (timezone picker, group-by).
 */
export function Select(props: SelectProps): JSX.Element;
