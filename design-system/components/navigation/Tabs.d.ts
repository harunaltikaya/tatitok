import React from "react";

export interface TabItem {
  value: string;
  label: React.ReactNode;
}

/**
 * @startingPoint section="Navigation" subtitle="Tabs + removable filter chip" viewport="700x150"
 */
export interface TabsProps {
  /** Tabs as strings or {value,label}. */
  tabs?: (string | TabItem)[];
  value?: string;
  onChange?: (value: string) => void;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Tabs — quiet top-level page switcher; active tab uses a faint surface wash.
 */
export function Tabs(props: TabsProps): JSX.Element;
