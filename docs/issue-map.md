# Conductor issue implementation map

The token-spend epic (#3) is specified by these maintained contracts:

| Issue | Owning contract / runtime |
| --- | --- |
| #2 | [Compaction](compaction.md), internal/compact and internal/proxy/compact.go |
| #4 | [Routing](routing.md), internal/routing |
| #5 | [Notes](notes.md), internal/notes |
| #6 | [Planning](planning.md), design contract |
| #7 | [Task graph](tasks.md), design contract |
| #8 | [Measurement](measurement.md), internal/metrics and internal/usage |
| #10 | [Rules](rules.md), internal/rules |
| #12 | [Capture](capture.md), design contract |
| #13 | [Routing](routing.md), internal/capabilities; nightly remains pending |
| #14 | [Nightly](nightly.md), physical fixture mapping remains pending |
| #15 | [Media](media.md), native OCR adapter and recording-stub contract; model quality remains unmeasured |
| #16 | internal/config/recipe.go and internal/proxy/task_budget.go; immutable recipe reload and authenticated effective configuration |
| #29 | internal/proxy/plane_reservation.go; cross-service reconciliation qualification remains outstanding |
| #36 | [Routing](routing.md), bounded AIuditor-authorized refusal fallback |
| #37 | [Routing](routing.md), origin binding and destination journal; CLI capture migration remains outstanding |
| #41 | [Reasoning](reasoning.md), provider policy design contract |

Design issues do not imply shipped runtime commands. Isolated tests do not qualify
physical devices or third-party providers. Bench owns quality comparisons and all
unmeasured savings remain unknown. This table is implementation navigation, not
an assertion that every issue is ready to close.
