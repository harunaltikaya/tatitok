// Mark — the tatitok brand mark: the `[ • ]` viewfinder bracket with the
// brand-green dot, inline so the brackets theme via currentColor and the
// dot stays the sampled brand green. Faithful port of the DS mark.

export default function Mark({ size = 26 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 120 120"
      fill="none"
      aria-hidden="true"
      style={{ display: "block" }}
    >
      <g stroke="currentColor" strokeWidth="12" strokeLinecap="round" strokeLinejoin="round">
        <path d="M50 16 H18 V104 H50" />
        <path d="M70 16 H102 V104 H70" />
      </g>
      <circle cx="60" cy="60" r="14" fill="var(--brand-green)" />
    </svg>
  );
}
