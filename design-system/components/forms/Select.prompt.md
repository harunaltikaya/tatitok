Select — native select with hairline border and quiet chevron. Used for the timezone picker and group-by dimension.

```jsx
<Select value={tz} onChange={e => setTz(e.target.value)}
  options={["UTC", "Europe/Istanbul", "America/New_York"]} aria-label="timezone" />
```
