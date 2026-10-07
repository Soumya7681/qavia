// Package targets decides where a run is allowed to connect (F-7.7, F-7.8).
//
// This is the platform's SSRF boundary. A target URL arrives from a user, and the
// generated suite that will use it is model-written code; between them there is
// nothing that can be trusted to stay inside the network it was pointed at. So the
// check happens here, twice, on the server:
//
//   - **Before enqueue.** The host must be on the project's allowlist, and the
//     addresses it resolves to must be public unless the project explicitly opted
//     into a private staging target. A rejection is audited and names the fix.
//   - **At dial time**, in a net.Dialer.Control hook. Between a resolve and a
//     connect a DNS record can change, and a resolve-then-dial check would connect
//     to whatever the second lookup returned. The Control hook sees the address the
//     kernel is about to connect to, which is the only moment the check cannot be
//     raced (BE-4.7.3).
//
// The runner gets the same answer expressed as firewall rules: the addresses this
// package approved are the only ones its chain accepts (internal/runner/network.go).
package targets
