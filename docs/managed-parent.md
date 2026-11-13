# Managed private-socket server

A package launcher may start `conductor serve --socket --parent-pid PID` with
its own process ID. Conductor requires that ID to match its actual OS parent
both before reading configuration and when starting the socket server. The flag
is refused outside private-socket mode; an init or invalid parent is refused.

During service, Conductor checks the OS parent relation every 100 ms. A changed
parent cancels the server context, closes its active connections, and removes the
socket using the existing private-server shutdown path. Normal signals retain
the same cleanup. The check uses the parent relation, not a search for a live
process with that numeric ID, so an unrelated reused PID cannot keep it active.

This flag does not grant desktop permissions or isolate a child from permissions
already held by its launcher. Native launchers must remain separate from the
permission-bearing desktop application.
