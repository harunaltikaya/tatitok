ClassDot — the small filled circle that encodes the economic class of usage (the system's signature legend marker) and liveness/status.

```jsx
<ClassDot tone="subscription" /> plan-included
<ClassDot tone="metered" /> metered API
<ClassDot tone="local" /> local
<ClassDot tone="free" /> free
<ClassDot tone="live" size={9} pulse /> live
```

`tone` accepts an economic class, a status (`positive`/`live`/`danger`), or any CSS color. `pulse` animates the SSE-connected indicator.
