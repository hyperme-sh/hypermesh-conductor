# Machine capability review

`conductor capability-review --endpoint https://AUTHORITY/api/v1/aiuditor/capabilities/review`
reads one machine-signed request from stdin. The enrolled host supplies that
request without exporting its private signing key. See AIuditor's
[host capability contract](https://github.com/hyperme-sh/aiuditor/blob/main/docs/host-capabilities.md)
for owner opt-in and the version 1 signature format.

Conductor verifies the signature and bounded validity, sends the request over
HTTPS without redirects, and checks the returned call ID, full request digest,
expiry and decision. Malformed, oversized, changed, expired or unavailable
responses fail closed. Only a current `allow` exits successfully. A valid `deny`
is printed as a structured decision and exits nonzero. Transport/protocol errors
print a fixed error without request bodies or authority response details. There
is no bearer token, local allow stub, cached grant or modification fallback.

This command reviews one call; it does not execute tools. The executor must
retain the exact argument bytes and capability graph, acquire controller locks,
refresh local preconditions, enforce physical limits and recheck the decision's
expiry at dispatch. It must also prevent duplicate execution across retries and
restarts. The host's WireGuard peer/interface checks remain mandatory. A key in
the signed JSON does not establish that a network packet arrived over WireGuard.
The host adapter and tool dispatcher are separate work under Visor #99.
