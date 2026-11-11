# Optional endpoint capture (#12)

Capture is off by default. Cooperative base-URL routing remains the supported
path. An opt-in adapter may install a PAC configuration scoped to exact reviewed
AI hosts and paths. Other traffic passes untouched. A versioned local allowlist
is operator configuration, never learned from intercepted requests.

TLS interception requires an explicitly installed local CA. Keep its private key
in OS-protected local storage; never export it in configuration or journals.
Visor owns eventual proxy/CA installation and removal UX. Conductor owns request
routing and AIuditor review. Linux desktop proxy settings and environment variables,
Windows WinINET/WinHTTP policy, and macOS network proxy settings differ; an
installer must record original settings and restore only its own changes.

A PAC-only first spike avoids installing a CA. CA interception is a separate
platform adapter. It must refuse ambiguous existing corporate interception.
Process-scoped rules apply only where authenticated process identity is available;
an HTTP header is not process identity. Pinned certificates and custom trust
stores can bypass or reject interception. Report an unsupported TLS path and
recommend cooperative routing or an explicit exemption. Do not claim total capture.

Captured requests follow classification, AIuditor allow/redact/block, and eligible
destination selection. Unchanged third-party traffic goes directly to its original
provider; only Hypermesh model traffic goes to the gateway. Provider credentials
are bound to their original endpoint. Redaction is a reviewed replacement body.
Personal capture may use an explicit fail-open availability policy; enterprise
policy chooses the fail posture. This does not change the runtime's AIuditor
fail-closed default. Clearly display when capture is unavailable or bypassed.

Default logs contain bounded destination/decision/token metadata, never bodies or
keys. Debug body recording must be local, explicit, time-bounded, and removable.
Dream proposals may change routing, caching, and indexing only. AIuditor safety
and RBAC remain authoritative. No arbitrary process termination, network isolation,
or general HTTPS interception is part of this design. OS installers and browser
backend protocol adapters are not implemented by the current proxy.
