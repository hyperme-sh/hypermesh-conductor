---
id: verb/summarize-counters
type: capability
---
# summarize-counters

Illustrative fixture; no capability is enabled by this note. The repeated-digit
limit hashes are placeholders, not approved controller definitions.

```capability-json
{
  "schema": 1,
  "scope": "org/example/site/lab",
  "owner": "example-owner",
  "revision": "r1",
  "id": "verb/summarize-counters",
  "kind": "verb",
  "uses": [
    {
      "id": "noun/network",
      "revision": "r1"
    }
  ],
  "limits": [
    {
      "id": "policy/read-only",
      "revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ],
  "inputs": {
    "window_seconds": {
      "type": "integer",
      "required": true,
      "minimum": 1,
      "maximum": 60
    }
  },
  "preconditions": [
    "read-grant-valid",
    "telemetry-fresh"
  ],
  "effects": [
    "aggregate-summary"
  ],
  "locks": [
    "network/counters"
  ],
  "timeout_ms": 5000,
  "freshness_ms": 1000
}
```

[[noun/network]]
