// Package datagen generates test data from a specification's own schemas (F-10.1
// to F-10.5, BE-8).
//
// **Bulk generation is deliberately not an AI feature** (ai-architecture.md 2). A
// hundred records from a schema is a loop with a seeded random source: it is free,
// instant, and identical every time. The same hundred records from a model cost money,
// take seconds, and are occasionally wrong in ways nobody notices until a test asserts
// against them. A model earns its place on the handful of fields where semantics
// matter — a plausible Indian address, a GSTIN that looks like a GSTIN — and the faker
// is always the fallback (BE-8.2).
//
// Three properties hold throughout:
//
//   - **Deterministic.** The same seed and the same schema produce byte-identical
//     records. That is what makes a failing test reproducible: "it passed on my
//     machine" usually means "my data was different".
//   - **Schema-constrained.** Type, format, pattern, enum, and the length and range
//     bounds are honoured, because data that violates the schema tests the validator
//     rather than the endpoint.
//   - **Never real, and never a valid payment instrument.** Card numbers come only
//     from published test ranges, and a test asserts it (BE-8.4).
package datagen
