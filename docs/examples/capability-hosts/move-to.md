---
id: verb/move-to
type: capability
---
# move-to

Illustrative fixture; no capability is enabled by this note. The repeated-digit
limit hashes are placeholders, not approved controller definitions.

```capability-json
{
  "schema": 1,
  "scope": "org/example/site/lab",
  "owner": "example-owner",
  "revision": "r1",
  "id": "verb/move-to",
  "kind": "verb",
  "uses": [
    {
      "id": "noun/sim-arm",
      "revision": "r1"
    }
  ],
  "limits": [
    {
      "id": "controller/workspace",
      "revision": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
    },
    {
      "id": "controller/joint-limits",
      "revision": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
    }
  ],
  "inputs": {
    "pose": {
      "type": "vector3",
      "required": true,
      "unit": "meter",
      "axes": [
        {
          "minimum": -1,
          "maximum": 1
        },
        {
          "minimum": -1,
          "maximum": 1
        },
        {
          "minimum": 0,
          "maximum": 1
        }
      ]
    }
  },
  "preconditions": [
    "sim-mode",
    "workspace-clear",
    "controller-ready"
  ],
  "effects": [
    "pose-changed"
  ],
  "locks": [
    "sim-arm/motion"
  ],
  "timeout_ms": 2000,
  "freshness_ms": 100
}
```

[[noun/sim-arm]]
