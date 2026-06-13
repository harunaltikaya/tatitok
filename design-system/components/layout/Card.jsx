import React from "react";

/**
 * Card — the flat surface primitive: solid fill, a single 0.5px hairline
 * border, soft radius, no shadow. Optional sentence-case title with right-
 * aligned meta/actions. The building block for every panel and stat tile.
 */
export function Card({ title, actions, padding = 16, className = "", style, children, ...rest }) {
  return (
    <section
      className={className}
      style={{
        background: "var(--surface-card)",
        border: "0.5px solid var(--border-hairline)",
        borderRadius: "var(--radius-lg)",
        padding,
        ...style,
      }}
      {...rest}
    >
      {(title || actions) && (
        <header
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--space-2)",
            marginBottom: "var(--space-3)",
          }}
        >
          {title && (
            <h2
              style={{
                margin: 0,
                fontSize: "var(--text-xs)",
                fontWeight: "var(--weight-medium)",
                letterSpacing: "var(--tracking-label)",
                color: "var(--text-tertiary)",
                whiteSpace: "nowrap",
                flex: "0 0 auto",
              }}
            >
              {title}
            </h2>
          )}
          {actions && <div style={{ marginLeft: "auto", flex: "0 0 auto", display: "flex", alignItems: "center", gap: 4 }}>{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}
