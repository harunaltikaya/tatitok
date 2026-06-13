MeterBar — thin plan-window usage meter against a cap; auto-escalates positive → amber → red.

```jsx
<MeterBar value={week.equiv} max={cap} showLabel format={usd} />
<MeterBar value={92} max={100} height={6} />
```

Omit `tone` to auto-color by fill ratio; set it to lock an economic-class hue.
