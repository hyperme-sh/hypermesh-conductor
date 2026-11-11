# Nightly process checks

The central scheduled workflow in FyberLabs/infra owns Conductor integration and
end-to-end execution. Its `conductor-process` adapter checks out current main,
builds with the Go version in go.mod, and runs the Infra-owned process harness.
It uses disposable loopback model and AIuditor servers and a temporary journal.

The cases cover coding tool calls and the next tool-result turn, streaming,
policy denial and output limits before dispatch, replay rejection after process
restart, and prompt/credential privacy. Results feed the central JUnit report
and failure issue reconciliation. A closed failure recurring creates a new issue.

PR CI runs Go unit/contract tests and builds packages on debrunner. No integration
suite is triggered by a PR, configuration edit, manual dispatch, or release.
The central nightly concurrency group serializes the batch and cancels superseded
runs. No detached Conductor workflow is started.

Strix Halo (#14), provider-specific OCR (#15), and actual provider behavior require
additional central adapters. The isolated process checks make no claim about
physical hardware or model quality. No savings numbers are asserted.
