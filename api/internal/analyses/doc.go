// Package analyses owns failure analyses, their evidence, and the stability score
// (F-9.1 to F-9.6).
//
// The whole package exists to answer one question honestly: why did this test fail.
// A model can produce a fluent answer to that in a second, and a fluent wrong answer
// is worse than no answer, because somebody will act on it. So three rules are
// enforced here rather than trusted to a prompt:
//
//   - **Evidence or nothing.** An analysis has to cite what it read: a log line
//     range, a response field, a source location. Every reference is checked against
//     the artifact it points at before the row is written, and a fabricated citation
//     is a rejection with the error fed back into the retry (BE-5.3).
//   - **The stability score is arithmetic.** It comes from re-run behaviour and, when
//     a repository is connected, from commit overlap. It is never a number the model
//     reported about itself, and when neither signal exists the field is null and the
//     API says why (BE-5.4).
//   - **Read-only tools.** The agent reads logs, response bodies, and source files,
//     all of which are untrusted content. It gets no tool that writes anything
//     (BE-5.2.3).
//
// The feedback half is small and load-bearing: a thumb up or down per analysis,
// stored with the prompt version, so the next prompt change is argued with numbers
// (BE-5.8).
package analyses
