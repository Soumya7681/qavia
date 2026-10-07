// Package security holds the curated payload library and the probe machinery for
// security testing (F-11.3, F-11.4, BE-9.2 to BE-9.4).
//
// The single most important line in this package is the one that is not here: **there
// is no code path where a model produces a payload string.** The library is a reviewed
// constant, versioned like any other code, and the agent's only job is to choose which
// endpoint and which parameter to point a library entry at (BE-9.3.2). That split is
// deliberate and it is a safety property, not a style choice — a model that invented
// attack strings would be a model whose output nobody reviewed being sent, unmodified,
// at somebody's server.
//
// The second most important property is that none of this fires by accident. Security
// testing is disabled by default, enabled per project by a lead, and the first run
// against a new host needs an explicit confirmation naming that exact host — because
// pointed at a machine you do not own, these requests are indistinguishable from an
// attack (BE-9.5, gate G3).
package security
